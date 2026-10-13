// Package store persists nowfile accounts, static sections, slots, tokens,
// and magic links in SQLite, and assembles each user's document. Document
// and DocumentByName are the only paths that produce a document for
// emitting; they run format.Compose, so expired slots never leave the store.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/username"

	_ "modernc.org/sqlite"
)

// ReleaseHold is how long a deleted account's name stays unavailable.
const ReleaseHold = 90 * 24 * time.Hour

// TokenPrefix starts every plaintext API token, so tokens are recognisable.
const TokenPrefix = "nf_"

// Errors returned by Store methods. Validation failures wrap
// format.ErrInvalid, username.ErrInvalid, username.ErrReserved, or
// ErrInvalidEmail instead.
var (
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name taken")
	ErrNameHeld     = errors.New("name held after account deletion")
	ErrEmailTaken   = errors.New("email already has an account")
	ErrInvalidEmail = errors.New("invalid email")
	ErrSuspended    = errors.New("account suspended")
	// ErrLinkInvalid covers unknown, used, and expired magic links alike.
	ErrLinkInvalid = errors.New("sign-in link invalid or expired")
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a handle on the nowfile database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	// Now returns the current time for timestamps the store writes and for
	// expiry checks it makes itself. It defaults to time.Now; tests replace
	// it. Values are stored in UTC at second precision.
	Now func() time.Time
}

// User is an account. Deleted accounts are never returned.
type User struct {
	ID        int64
	Name      string // canonical lowercase username
	Email     string // lowercased
	Created   time.Time
	LastSeen  time.Time // zero if never
	Directory bool      // listed by an empty finger query
	Suspended bool
}

// Static is the part of the document the user writes by hand.
type Static struct {
	Name     string
	Headline string
	Photo    string
	Links    []string
	Plan     string
}

// Token is a per-device API token. The plaintext is never stored.
type Token struct {
	ID       int64
	UserID   int64
	Label    string
	Created  time.Time
	LastUsed time.Time // zero if never
}

// Open opens or creates the database at path, creating the parent
// directory if needed, and applies pending migrations. The database runs in
// WAL mode with a busy timeout and foreign keys on, and write transactions
// take the write lock immediately.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s := &Store{db: db, Now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies every embedded migration newer than schema_version, each
// in its own transaction. Files are named NNNN_description.sql.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := filepath.Base(name)
		n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("store: migration %s: bad name", base)
		}
		if n <= current {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM schema_version; INSERT INTO schema_version (version) VALUES (?)`, n)
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %s: %w", base, err)
		}
	}
	return nil
}

// tx runs fn in a transaction, committing if it returns nil.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) now() time.Time { return s.Now().UTC().Truncate(time.Second) }

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// nullTime stores the zero time as NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

func parseTime(v sql.NullString) (time.Time, error) {
	if !v.Valid || v.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v.String)
}

// --- users ---

// CreateUser creates an account. The name goes through username.Validate
// and is stored lowercased; the email is trimmed and lowercased. It fails
// with ErrNameTaken if a live account has the name, ErrNameHeld if the name
// is in its post-deletion hold, and ErrEmailTaken if a live account has the
// email.
func (s *Store) CreateUser(ctx context.Context, name, email string) (User, error) {
	name, err := username.Validate(name)
	if err != nil {
		return User{}, err
	}
	email, err = normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	now := s.now()
	u := User{Name: name, Email: email, Created: now}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE name = ? AND deleted_at IS NULL`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrNameTaken
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM released_names WHERE name = ? AND available_after > ?`, name, fmtTime(now)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrNameHeld
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ? AND deleted_at IS NULL`, email).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrEmailTaken
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO users (name, email, created) VALUES (?, ?, ?)`, name, email, fmtTime(now))
		if err != nil {
			return err
		}
		if u.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO static (user_id) VALUES (?)`, u.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM released_names WHERE name = ?`, name)
		return err
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

func normalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(e, '@')
	if at < 1 || at == len(e)-1 || len(e) > 254 || strings.ContainsAny(e, " \t\r\n<>,;\"") {
		return "", fmt.Errorf("%w: %q", ErrInvalidEmail, email)
	}
	return e, nil
}

const userCols = `id, name, email, created, last_seen, directory, suspended`

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created, lastSeen sql.NullString
	err := row.Scan(&u.ID, &u.Name, &u.Email, &created, &lastSeen, &u.Directory, &u.Suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if u.Created, err = parseTime(created); err != nil {
		return User{}, err
	}
	if u.LastSeen, err = parseTime(lastSeen); err != nil {
		return User{}, err
	}
	return u, nil
}

// UserByName returns the live account with name, compared
// case-insensitively, or ErrNotFound. Suspended accounts are returned with
// Suspended set.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name = ? AND deleted_at IS NULL`, strings.ToLower(name)))
}

// UserByEmail returns the live account with email, compared
// case-insensitively, or ErrNotFound.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ? AND deleted_at IS NULL`, strings.ToLower(strings.TrimSpace(email))))
}

// UserByID returns the live account with id, or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ? AND deleted_at IS NULL`, id))
}

// exec runs one statement in a transaction and returns ErrNotFound if it
// touched no rows.
func (s *Store) exec(ctx context.Context, query string, args ...any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TouchLastSeen records activity on the account now. Sign-in should call
// it; LookupToken does it on every API use.
func (s *Store) TouchLastSeen(ctx context.Context, id int64) error {
	return s.exec(ctx, `UPDATE users SET last_seen = ? WHERE id = ? AND deleted_at IS NULL`, fmtTime(s.now()), id)
}

// SetDirectory opts the account in or out of the public user list.
func (s *Store) SetDirectory(ctx context.Context, id int64, listed bool) error {
	return s.exec(ctx, `UPDATE users SET directory = ? WHERE id = ? AND deleted_at IS NULL`, listed, id)
}

// Suspend suspends the live account with name. A suspended account's
// document is not served and its tokens and sign-in links stop working.
func (s *Store) Suspend(ctx context.Context, name string) error {
	return s.exec(ctx, `UPDATE users SET suspended = 1 WHERE name = ? AND deleted_at IS NULL`, strings.ToLower(name))
}

// Release lifts a suspension on the live account with name.
func (s *Store) Release(ctx context.Context, name string) error {
	return s.exec(ctx, `UPDATE users SET suspended = 0 WHERE name = ? AND deleted_at IS NULL`, strings.ToLower(name))
}

// DeleteUser soft-deletes the account: it sets deleted_at, removes the
// account's static section, slots, tokens, and sign-in links, and holds the
// name for ReleaseHold. The users row is kept.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	now := s.now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ? AND deleted_at IS NULL`, id).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM slots WHERE user_id = ?`,
			`DELETE FROM tokens WHERE user_id = ?`,
			`DELETE FROM magic_links WHERE user_id = ?`,
			`DELETE FROM static WHERE user_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET deleted_at = ?, directory = 0 WHERE id = ?`, fmtTime(now), id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO released_names (name, available_after) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET available_after = excluded.available_after`,
			name, fmtTime(now.Add(ReleaseHold)))
		return err
	})
}

// DirectoryNames returns the names of live, unsuspended accounts that opted
// into the directory, sorted.
func (s *Store) DirectoryNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM users WHERE directory = 1 AND suspended = 0 AND deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// --- static ---

// GetStatic returns the account's static section. Links is never nil.
func (s *Store) GetStatic(ctx context.Context, userID int64) (Static, error) {
	var st Static
	var links string
	err := s.db.QueryRowContext(ctx, `SELECT s.name, s.headline, s.photo, s.links, s.plan
		FROM static s JOIN users u ON u.id = s.user_id
		WHERE s.user_id = ? AND u.deleted_at IS NULL`, userID).
		Scan(&st.Name, &st.Headline, &st.Photo, &links, &st.Plan)
	if errors.Is(err, sql.ErrNoRows) {
		return Static{}, ErrNotFound
	}
	if err != nil {
		return Static{}, err
	}
	if err := json.Unmarshal([]byte(links), &st.Links); err != nil {
		return Static{}, fmt.Errorf("store: links for user %d: %w", userID, err)
	}
	if st.Links == nil {
		st.Links = []string{}
	}
	return st, nil
}

// PutStatic replaces the account's static section. It is validated with
// format.Validate (at most format.MaxLinks links, single-line values, line
// length); failures wrap format.ErrInvalid. Plan line endings are
// normalised to LF and trailing newlines dropped.
func (s *Store) PutStatic(ctx context.Context, userID int64, st Static) error {
	st.Plan = strings.TrimRight(strings.ReplaceAll(st.Plan, "\r\n", "\n"), "\n")
	if st.Links == nil {
		st.Links = []string{}
	}
	doc := format.Document{Version: format.CurrentVersion, Name: st.Name, Headline: st.Headline, Photo: st.Photo, Links: st.Links, Plan: st.Plan}
	if err := format.Validate(doc); err != nil {
		return err
	}
	links, err := json.Marshal(st.Links)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO static (user_id, name, headline, photo, links, plan) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET name = excluded.name, headline = excluded.headline,
				photo = excluded.photo, links = excluded.links, plan = excluded.plan`,
			userID, st.Name, st.Headline, st.Photo, string(links), st.Plan)
		return err
	})
}

// liveUser returns ErrNotFound unless userID is a live account.
func liveUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- slots ---

// PutSlot sets one slot, replacing any previous value for the key, with
// set time Now. key must be one of format.KnownSlotKeys (any case; stored
// canonical). A zero until means no expiry; an until at or before Now is
// rejected. Validation failures wrap format.ErrInvalid. It returns the slot
// as stored.
func (s *Store) PutSlot(ctx context.Context, userID int64, key, value string, until time.Time, source string) (format.Slot, error) {
	canon, ok := format.CanonicalSlotKey(key)
	if !ok {
		return format.Slot{}, fmt.Errorf("%w: unknown slot key %q", format.ErrInvalid, key)
	}
	now := s.now()
	if !until.IsZero() {
		until = until.UTC().Truncate(time.Second)
		if !until.After(now) {
			return format.Slot{}, fmt.Errorf("%w: until is not in the future", format.ErrInvalid)
		}
	}
	slot := format.Slot{Key: canon, Value: value, Set: now, Until: until, Source: source}
	if err := format.Validate(format.Document{Version: format.CurrentVersion, Slots: []format.Slot{slot}}); err != nil {
		return format.Slot{}, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO slots (user_id, key, value, set_at, until, source) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, key) DO UPDATE SET value = excluded.value, set_at = excluded.set_at,
				until = excluded.until, source = excluded.source`,
			userID, canon, value, fmtTime(now), nullTime(until), source)
		return err
	})
	if err != nil {
		return format.Slot{}, err
	}
	return slot, nil
}

// DeleteSlot clears one slot. Clearing a slot that is not set is not an
// error. Unknown keys wrap format.ErrInvalid.
func (s *Store) DeleteSlot(ctx context.Context, userID int64, key string) error {
	canon, ok := format.CanonicalSlotKey(key)
	if !ok {
		return fmt.Errorf("%w: unknown slot key %q", format.ErrInvalid, key)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM slots WHERE user_id = ? AND key = ?`, userID, canon)
		return err
	})
}

// Slots returns the account's unexpired slots as of Now, in
// format.KnownSlotKeys order. Expired rows stay in the table until
// overwritten but are never returned.
func (s *Store) Slots(ctx context.Context, userID int64) ([]format.Slot, error) {
	slots, err := s.slots(ctx, userID)
	if err != nil {
		return nil, err
	}
	return format.Compose(format.Document{Slots: slots}, s.Now()).Slots, nil
}

// slots returns every stored slot for the user, expired or not.
func (s *Store) slots(ctx context.Context, userID int64) ([]format.Slot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, set_at, until, source FROM slots WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []format.Slot
	for rows.Next() {
		var sl format.Slot
		var set, until sql.NullString
		if err := rows.Scan(&sl.Key, &sl.Value, &set, &until, &sl.Source); err != nil {
			return nil, err
		}
		if sl.Set, err = parseTime(set); err != nil {
			return nil, err
		}
		if sl.Until, err = parseTime(until); err != nil {
			return nil, err
		}
		slots = append(slots, sl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, k := range format.KnownSlotKeys {
		order[k] = i
	}
	sort.SliceStable(slots, func(i, j int) bool { return order[slots[i].Key] < order[slots[j].Key] })
	return slots, nil
}

// --- documents ---

// Document assembles the account's document from its static section and
// slots and composes it at now, dropping expired slots. Every reader
// (finger, HTTP text, JSON, HTML, API) should get its document here. It
// returns ErrNotFound for unknown or deleted accounts and ErrSuspended for
// suspended ones; callers that must not reveal the difference should treat
// both alike.
func (s *Store) Document(ctx context.Context, userID int64, now time.Time) (format.Document, error) {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return format.Document{}, err
	}
	return s.document(ctx, u, now)
}

// DocumentByName is Document for a username, compared case-insensitively.
func (s *Store) DocumentByName(ctx context.Context, name string, now time.Time) (format.Document, error) {
	u, err := s.UserByName(ctx, name)
	if err != nil {
		return format.Document{}, err
	}
	return s.document(ctx, u, now)
}

func (s *Store) document(ctx context.Context, u User, now time.Time) (format.Document, error) {
	if u.Suspended {
		return format.Document{}, ErrSuspended
	}
	st, err := s.GetStatic(ctx, u.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return format.Document{}, err
	}
	slots, err := s.slots(ctx, u.ID)
	if err != nil {
		return format.Document{}, err
	}
	doc := format.Document{
		Version:  format.CurrentVersion,
		Name:     st.Name,
		Headline: st.Headline,
		Photo:    st.Photo,
		Links:    st.Links,
		Slots:    slots,
		Plan:     st.Plan,
	}
	return format.Compose(doc, now), nil
}

// --- tokens ---

// HashToken returns the hex sha256 of a plaintext token or link, the form
// the database stores.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateToken creates a named API token for the account. The plaintext is
// returned once and only its hash is stored.
func (s *Store) CreateToken(ctx context.Context, userID int64, label string) (string, Token, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 64 || strings.ContainsAny(label, "\r\n") {
		return "", Token{}, fmt.Errorf("%w: token label must be 1 to 64 characters on one line", format.ErrInvalid)
	}
	secret, err := randomSecret()
	if err != nil {
		return "", Token{}, err
	}
	plain := TokenPrefix + secret
	t := Token{UserID: userID, Label: label, Created: s.now()}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO tokens (user_id, label, hash, created) VALUES (?, ?, ?, ?)`,
			userID, label, HashToken(plain), fmtTime(t.Created))
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return "", Token{}, err
	}
	return plain, t, nil
}

// LookupToken authenticates a plaintext bearer token: it hashes it, finds
// the token, and records last_used on the token and last_seen on the
// account. It returns ErrNotFound for unknown or revoked tokens or deleted
// accounts, and ErrSuspended if the account is suspended. This is the
// spec's TokenByHash; it takes the plaintext so no caller handles hashes.
func (s *Store) LookupToken(ctx context.Context, plaintext string) (Token, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return Token{}, ErrNotFound
	}
	now := s.now()
	var t Token
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var created sql.NullString
		var suspended bool
		err := tx.QueryRowContext(ctx, `SELECT t.id, t.user_id, t.label, t.created, u.suspended
			FROM tokens t JOIN users u ON u.id = t.user_id
			WHERE t.hash = ? AND u.deleted_at IS NULL`, HashToken(plaintext)).
			Scan(&t.ID, &t.UserID, &t.Label, &created, &suspended)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if suspended {
			return ErrSuspended
		}
		if t.Created, err = parseTime(created); err != nil {
			return err
		}
		t.LastUsed = now
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET last_used = ? WHERE id = ?`, fmtTime(now), t.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET last_seen = ? WHERE id = ?`, fmtTime(now), t.UserID)
		return err
	})
	if err != nil {
		return Token{}, err
	}
	return t, nil
}

// Tokens lists the account's tokens, oldest first.
func (s *Store) Tokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, label, created, last_used FROM tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var created, lastUsed sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Label, &created, &lastUsed); err != nil {
			return nil, err
		}
		if t.Created, err = parseTime(created); err != nil {
			return nil, err
		}
		if t.LastUsed, err = parseTime(lastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken deletes one of the account's tokens. It returns ErrNotFound
// if the token does not exist or belongs to another account.
func (s *Store) RevokeToken(ctx context.Context, userID, tokenID int64) error {
	return s.exec(ctx, `DELETE FROM tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
}

// --- magic links ---

// CreateMagicLink creates a single-use sign-in secret for the account that
// is valid until expires. The plaintext goes in the emailed URL; only its
// hash is stored.
func (s *Store) CreateMagicLink(ctx context.Context, userID int64, expires time.Time) (string, error) {
	plain, err := randomSecret()
	if err != nil {
		return "", err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO magic_links (hash, user_id, expires) VALUES (?, ?, ?)`,
			HashToken(plain), userID, fmtTime(expires))
		return err
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// ConsumeMagicLink marks a sign-in secret used and returns its account.
// It succeeds at most once per link. Unknown, used, and expired links all
// return ErrLinkInvalid; a link for a suspended account is consumed and
// returns ErrSuspended; for a deleted account, ErrLinkInvalid.
func (s *Store) ConsumeMagicLink(ctx context.Context, token string) (int64, error) {
	now := s.now()
	var userID int64
	var suspended bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE magic_links SET used = 1
			WHERE hash = ? AND used = 0 AND expires > ? RETURNING user_id`,
			HashToken(token), fmtTime(now)).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLinkInvalid
		}
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT suspended FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&suspended)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLinkInvalid
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	if suspended {
		return 0, ErrSuspended
	}
	return userID, nil
}
