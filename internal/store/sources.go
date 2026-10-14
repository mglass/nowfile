package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Source is a server-side writer the user has added, such as a calendar
// feed. Config is JSON the source kind owns; State is JSON the runner owns.
// A source publishes only while Enabled, and it can only be enabled after
// PutSource, which the account page calls with a preview the user has seen.
type Source struct {
	ID        int64
	UserID    int64
	Kind      string
	Config    string
	Enabled   bool
	LastRun   time.Time // zero if never
	LastError string    // shown on the account page; empty when healthy
	Created   time.Time
	State     string
}

const sourceCols = `id, user_id, kind, config, enabled, last_run, last_error, created, state`

func scanSource(scan func(...any) error) (Source, error) {
	var src Source
	var lastRun, created sql.NullString
	err := scan(&src.ID, &src.UserID, &src.Kind, &src.Config, &src.Enabled, &lastRun, &src.LastError, &created, &src.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	if err != nil {
		return Source{}, err
	}
	if src.LastRun, err = parseTime(lastRun); err != nil {
		return Source{}, err
	}
	if src.Created, err = parseTime(created); err != nil {
		return Source{}, err
	}
	return src, nil
}

// Source returns the account's source of kind, or ErrNotFound.
func (s *Store) Source(ctx context.Context, userID int64, kind string) (Source, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE user_id = ? AND kind = ?`, userID, kind)
	return scanSource(row.Scan)
}

// PutSource adds or replaces the account's source of kind with config and
// the state its preview produced, and clears last_error. A new source starts
// disabled. Replacing an enabled source keeps it enabled only if config is
// unchanged, so a new URL is never published before it has been previewed.
func (s *Store) PutSource(ctx context.Context, userID int64, kind, config, state string) (Source, error) {
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := liveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sources (user_id, kind, config, enabled, last_run, last_error, created, state)
			VALUES (?, ?, ?, 0, ?, '', ?, ?)
			ON CONFLICT (user_id, kind) DO UPDATE SET
				enabled = CASE WHEN sources.config = excluded.config THEN sources.enabled ELSE 0 END,
				config = excluded.config, last_run = excluded.last_run, last_error = '', state = excluded.state`,
			userID, kind, config, fmtTime(now), fmtTime(now), state)
		return err
	})
	if err != nil {
		return Source{}, err
	}
	return s.Source(ctx, userID, kind)
}

// SetSourceEnabled switches the account's source of kind on or off. It
// returns ErrNotFound if there is no such source.
func (s *Store) SetSourceEnabled(ctx context.Context, userID int64, kind string, enabled bool) error {
	return s.exec(ctx, `UPDATE sources SET enabled = ? WHERE user_id = ? AND kind = ?`, enabled, userID, kind)
}

// DeleteSource removes the account's source of kind. Removing a source that
// does not exist is not an error.
func (s *Store) DeleteSource(ctx context.Context, userID int64, kind string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE user_id = ? AND kind = ?`, userID, kind)
		return err
	})
}

// EnabledSources returns every enabled source of kind whose account is live
// and not suspended, by id.
func (s *Store) EnabledSources(ctx context.Context, kind string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.user_id, s.kind, s.config, s.enabled, s.last_run, s.last_error, s.created, s.state
		FROM sources s JOIN users u ON u.id = s.user_id
		WHERE s.kind = ? AND s.enabled = 1 AND u.deleted_at IS NULL AND u.suspended = 0
		ORDER BY s.id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// RecordSourceRun saves the outcome of a runner pass over src: its new state
// and error, and lastRun if it is not zero. It applies only if the source is
// still enabled with the config the runner read, and returns ErrNotFound
// otherwise, so a run that raced an edit, a disable, or a removal leaves no
// trace and its caller should not publish.
func (s *Store) RecordSourceRun(ctx context.Context, src Source, lastRun time.Time, lastError, state string) error {
	if lastRun.IsZero() {
		return s.exec(ctx, `UPDATE sources SET last_error = ?, state = ? WHERE id = ? AND config = ? AND enabled = 1`,
			lastError, state, src.ID, src.Config)
	}
	return s.exec(ctx, `UPDATE sources SET last_run = ?, last_error = ?, state = ? WHERE id = ? AND config = ? AND enabled = 1`,
		fmtTime(lastRun), lastError, state, src.ID, src.Config)
}

// TimeZone returns the account's IANA time zone name, or "" if not set.
func (s *Store) TimeZone(ctx context.Context, userID int64) (string, error) {
	var tz string
	err := s.db.QueryRowContext(ctx, `SELECT s.tz FROM static s JOIN users u ON u.id = s.user_id
		WHERE s.user_id = ? AND u.deleted_at IS NULL`, userID).Scan(&tz)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return tz, err
}

// SetTimeZone sets the account's time zone. name must be "" or a zone the
// Go time zone database knows; "Local" is refused because it means the
// server's zone, not the user's.
func (s *Store) SetTimeZone(ctx context.Context, userID int64, name string) error {
	if name != "" {
		if _, err := LoadTimeZone(name); err != nil {
			return err
		}
	}
	return s.exec(ctx, `UPDATE static SET tz = ? WHERE user_id = ? AND user_id IN (SELECT id FROM users WHERE deleted_at IS NULL)`, name, userID)
}

// LoadTimeZone loads an IANA zone name a user gave. Failures wrap
// ErrInvalidTimeZone.
func LoadTimeZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 64 {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimeZone, name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimeZone, name)
	}
	return loc, nil
}

// ErrInvalidTimeZone is a time zone name the database does not know.
var ErrInvalidTimeZone = errors.New("unknown time zone")
