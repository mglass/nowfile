package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/username"
)

var ctx = context.Background()

// clock is a settable time source for Store.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *clock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func newClock(s string) *clock       { t, _ := time.Parse(time.RFC3339, s); return &clock{t: t} }

func open(t *testing.T) (*Store, *clock) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := newClock("2026-10-13T16:00:00Z")
	s.Now = c.Now
	return s, c
}

func mustUser(t *testing.T, s *Store, name, email string) User {
	t.Helper()
	u, err := s.CreateUserHeld(ctx, name, email) // admin path, so fixtures may use "mike"
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return u
}

func TestOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustUser(t, s, "mike", "mike@example.com")
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != len(names) {
		t.Fatalf("schema_version = %d, %v; want %d", v, err, len(names))
	}
	var mode string
	s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q", mode)
	}
	if _, err := s.UserByName(ctx, "mike"); err != nil {
		t.Errorf("data lost on reopen: %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	s, _ := open(t)
	u := mustUser(t, s, "Mike", " Mike@Example.COM ")
	if u.Name != "mike" || u.Email != "mike@example.com" || u.ID == 0 {
		t.Errorf("got %+v", u)
	}
	if !u.Created.Equal(newClock("2026-10-13T16:00:00Z").t) {
		t.Errorf("created = %v", u.Created)
	}
	cases := []struct {
		name, email string
		err         error
	}{
		{"MIKE", "other@example.com", ErrNameTaken},
		{"other", "MIKE@example.com", ErrEmailTaken},
		{"admin", "a@example.com", username.ErrReserved},
		{"x", "a@example.com", username.ErrInvalid},
		{"good", "not-an-email", ErrInvalidEmail},
		{"good", "@example.com", ErrInvalidEmail},
	}
	for _, c := range cases {
		if _, err := s.CreateUser(ctx, c.name, c.email); !errors.Is(err, c.err) {
			t.Errorf("CreateUser(%q, %q) = %v, want %v", c.name, c.email, err, c.err)
		}
	}
	for _, get := range []func() (User, error){
		func() (User, error) { return s.UserByName(ctx, "MiKe") },
		func() (User, error) { return s.UserByEmail(ctx, "MIKE@example.com") },
		func() (User, error) { return s.UserByID(ctx, u.ID) },
	} {
		got, err := get()
		if err != nil || got.ID != u.ID || got.Name != "mike" {
			t.Errorf("lookup = %+v, %v", got, err)
		}
	}
	if _, err := s.UserByName(ctx, "nobody-here"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
}

func TestUserFlags(t *testing.T) {
	s, c := open(t)
	a := mustUser(t, s, "alice", "a@example.com")
	b := mustUser(t, s, "bob", "b@example.com")
	mustUser(t, s, "carol", "c@example.com")

	c.Add(time.Hour)
	if err := s.TouchLastSeen(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserByID(ctx, a.ID)
	if !got.LastSeen.Equal(c.Now()) {
		t.Errorf("last_seen = %v", got.LastSeen)
	}

	s.SetDirectory(ctx, a.ID, true)
	s.SetDirectory(ctx, b.ID, true)
	names, err := s.DirectoryNames(ctx)
	if err != nil || strings.Join(names, ",") != "alice,bob" {
		t.Errorf("directory = %v, %v", names, err)
	}

	if err := s.Suspend(ctx, "BOB"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.UserByName(ctx, "bob")
	if !got.Suspended {
		t.Error("not suspended")
	}
	if names, _ := s.DirectoryNames(ctx); strings.Join(names, ",") != "alice" {
		t.Errorf("suspended user listed: %v", names)
	}
	if _, err := s.DocumentByName(ctx, "bob", c.Now()); !errors.Is(err, ErrSuspended) {
		t.Errorf("suspended document: %v", err)
	}
	if err := s.Release(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DocumentByName(ctx, "bob", c.Now()); err != nil {
		t.Errorf("released document: %v", err)
	}
	if err := s.Suspend(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("suspend unknown: %v", err)
	}
}

func TestDeleteUserHoldsName(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	s.PutSlot(ctx, u.ID, "Status", "hi", time.Time{}, "")
	tok, _, _ := s.CreateToken(ctx, u.ID, "laptop")
	s.SetDirectory(ctx, u.ID, true)

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := s.UserByName(ctx, "mike"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted user found: %v", err)
	}
	if _, err := s.DocumentByName(ctx, "mike", c.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted document: %v", err)
	}
	if _, err := s.LookupToken(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted user's token works: %v", err)
	}
	if names, _ := s.DirectoryNames(ctx); len(names) != 0 {
		t.Errorf("deleted user listed: %v", names)
	}

	// Email is free immediately; the name is held for 90 days.
	other := mustUser(t, s, "someone", "mike@example.com")
	if _, err := s.CreateUser(ctx, "mike", "new@example.com"); !errors.Is(err, ErrNameHeld) {
		t.Errorf("held name: %v", err)
	}
	c.Add(ReleaseHold - time.Second)
	if _, err := s.CreateUser(ctx, "mike", "new@example.com"); !errors.Is(err, ErrNameHeld) {
		t.Errorf("one second before release: %v", err)
	}
	c.Add(time.Second)
	again := mustUser(t, s, "mike", "new@example.com")
	if again.ID == u.ID || again.ID == other.ID {
		t.Errorf("reused id %d", again.ID)
	}
	if slots, _ := s.Slots(ctx, again.ID); len(slots) != 0 {
		t.Errorf("new account inherited slots: %v", slots)
	}
}

func TestStatic(t *testing.T) {
	s, _ := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	st, err := s.GetStatic(ctx, u.ID)
	if err != nil || st.Links == nil || st.Name != "" {
		t.Fatalf("empty static = %+v, %v", st, err)
	}
	want := Static{Name: "Mike Glass", Headline: "Builds tools.", Photo: "https://example.com/m.jpg",
		Links: []string{"https://a.example", "https://b.example"}, Plan: "Line one.\r\nLine two.\n\n"}
	if err := s.PutStatic(ctx, u.ID, want); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetStatic(ctx, u.ID)
	if got.Name != want.Name || got.Photo != want.Photo || strings.Join(got.Links, " ") != strings.Join(want.Links, " ") || got.Plan != "Line one.\nLine two." {
		t.Errorf("got %+v", got)
	}

	bad := want
	bad.Links = []string{"1", "2", "3", "4", "5", "6"}
	if err := s.PutStatic(ctx, u.ID, bad); !errors.Is(err, format.ErrInvalid) {
		t.Errorf("six links: %v", err)
	}
	bad = want
	bad.Headline = strings.Repeat("x", 300)
	if err := s.PutStatic(ctx, u.ID, bad); !errors.Is(err, format.ErrInvalid) {
		t.Errorf("long headline: %v", err)
	}
	if err := s.PutStatic(ctx, 9999, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestSlots(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	now := c.Now()

	if _, err := s.PutSlot(ctx, u.ID, "focus", "Work", time.Time{}, "shortcuts"); err != nil {
		t.Fatal(err)
	}
	sl, err := s.PutSlot(ctx, u.ID, "STATUS", "Lunch", now.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if sl.Key != "Status" || !sl.Set.Equal(now) || !sl.Until.Equal(now.Add(time.Hour)) {
		t.Errorf("stored %+v", sl)
	}
	slots, _ := s.Slots(ctx, u.ID)
	if len(slots) != 2 || slots[0].Key != "Status" || slots[1].Key != "Focus" || slots[1].Source != "shortcuts" {
		t.Errorf("slots = %+v", slots)
	}

	// Overwrite.
	s.PutSlot(ctx, u.ID, "Status", "Back", time.Time{}, "cli")
	slots, _ = s.Slots(ctx, u.ID)
	if slots[0].Value != "Back" || !slots[0].Until.IsZero() || slots[0].Source != "cli" {
		t.Errorf("overwrite = %+v", slots[0])
	}

	for _, c := range []struct {
		key, value string
		until      time.Time
	}{
		{"Mood", "ok", time.Time{}},
		{"Name", "x", time.Time{}},
		{"Status", "", time.Time{}},
		{"Status", "two\nlines", time.Time{}},
		{"Status", strings.Repeat("x", 250), time.Time{}},
		{"Status", "past", now.Add(-time.Minute)},
		{"Status", "now", now},
	} {
		if _, err := s.PutSlot(ctx, u.ID, c.key, c.value, c.until, ""); !errors.Is(err, format.ErrInvalid) {
			t.Errorf("PutSlot(%q, %q, %v) = %v, want ErrInvalid", c.key, c.value, c.until, err)
		}
	}

	if err := s.DeleteSlot(ctx, u.ID, "status"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSlot(ctx, u.ID, "status"); err != nil {
		t.Errorf("delete absent slot: %v", err)
	}
	if err := s.DeleteSlot(ctx, u.ID, "bogus"); !errors.Is(err, format.ErrInvalid) {
		t.Errorf("delete bogus: %v", err)
	}
	slots, _ = s.Slots(ctx, u.ID)
	if len(slots) != 1 || slots[0].Key != "Focus" {
		t.Errorf("after delete = %+v", slots)
	}
	if _, err := s.PutSlot(ctx, 9999, "Status", "x", time.Time{}, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

// TestDocumentNeverEmitsExpired is the store half of the invariant: a slot
// past its until is gone from the document without anyone deleting it.
func TestDocumentNeverEmitsExpired(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	s.PutStatic(ctx, u.ID, Static{Name: "Mike Glass", Headline: "Builds tools.", Plan: "Reply within a day."})
	start := c.Now()
	s.PutSlot(ctx, u.ID, "Listening", "Song", start.Add(5*time.Minute), "lastfm")
	s.PutSlot(ctx, u.ID, "Status", "Lunch, back at 1", start.Add(time.Hour), "")
	s.PutSlot(ctx, u.ID, "Focus", "Work", time.Time{}, "")

	cases := []struct {
		at       time.Duration
		keys     string
		headline string
	}{
		{0, "Status,Focus,Listening", "Lunch, back at 1"},
		{5 * time.Minute, "Status,Focus", "Lunch, back at 1"},
		{time.Hour, "Focus", "Work"},
		{24 * time.Hour, "Focus", "Work"},
	}
	for _, tc := range cases {
		now := start.Add(tc.at)
		c.Set(now)
		for _, get := range []func() (format.Document, error){
			func() (format.Document, error) { return s.Document(ctx, u.ID, now) },
			func() (format.Document, error) { return s.DocumentByName(ctx, "MIKE", now) },
		} {
			doc, err := get()
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, sl := range doc.Slots {
				keys = append(keys, sl.Key)
				if sl.Expired(now) {
					t.Errorf("at +%v: expired %s emitted", tc.at, sl.Key)
				}
			}
			if strings.Join(keys, ",") != tc.keys {
				t.Errorf("at +%v: keys %v, want %s", tc.at, keys, tc.keys)
			}
			if h := format.Headline(doc); h != tc.headline {
				t.Errorf("at +%v: headline %q, want %q", tc.at, h, tc.headline)
			}
			if doc.Version != "0" || doc.Name != "Mike Glass" || doc.Plan != "Reply within a day." {
				t.Errorf("static: %+v", doc)
			}
			if err := format.Validate(doc); err != nil {
				t.Errorf("composed document invalid: %v", err)
			}
		}
		slots, _ := s.Slots(ctx, u.ID)
		if len(slots) != strings.Count(tc.keys, ",")+1 {
			t.Errorf("at +%v: Slots returned %d", tc.at, len(slots))
		}
	}

	doc, _ := s.Document(ctx, u.ID, start)
	want := "nowfile/0\nName: Mike Glass\nHeadline: Builds tools.\n\n" +
		"Status: Lunch, back at 1\n  set: 2026-10-13T16:00:00Z\n  until: 2026-10-13T17:00:00Z\n" +
		"Focus: Work\n  set: 2026-10-13T16:00:00Z\n" +
		"Listening: Song\n  set: 2026-10-13T16:00:00Z\n  until: 2026-10-13T16:05:00Z\n  source: lastfm\n" +
		"\nPlan:\nReply within a day.\n"
	if got := format.Render(doc, true); got != want {
		t.Errorf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestTokens(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	other := mustUser(t, s, "other", "o@example.com")

	plain, tok, err := s.CreateToken(ctx, u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, TokenPrefix) || len(plain) < 40 || tok.Label != "laptop" {
		t.Errorf("token %q %+v", plain, tok)
	}
	var stored int
	s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash = ?`, plain).Scan(&stored)
	if stored != 0 {
		t.Error("plaintext stored")
	}
	if _, _, err := s.CreateToken(ctx, u.ID, " "); !errors.Is(err, format.ErrInvalid) {
		t.Errorf("empty label: %v", err)
	}

	c.Add(time.Minute)
	got, err := s.LookupToken(ctx, plain)
	if err != nil || got.ID != tok.ID || got.UserID != u.ID || !got.LastUsed.Equal(c.Now()) {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if usr, _ := s.UserByID(ctx, u.ID); !usr.LastSeen.Equal(c.Now()) {
		t.Errorf("last_seen not updated: %v", usr.LastSeen)
	}
	list, _ := s.Tokens(ctx, u.ID)
	if len(list) != 1 || !list[0].LastUsed.Equal(c.Now()) {
		t.Errorf("tokens = %+v", list)
	}
	for _, bad := range []string{"", "nf_nope", plain + "x", HashToken(plain)} {
		if _, err := s.LookupToken(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("LookupToken(%q) = %v", bad, err)
		}
	}

	s.Suspend(ctx, "mike")
	if _, err := s.LookupToken(ctx, plain); !errors.Is(err, ErrSuspended) {
		t.Errorf("suspended: %v", err)
	}
	s.Release(ctx, "mike")

	if err := s.RevokeToken(ctx, other.ID, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke other's token: %v", err)
	}
	if err := s.RevokeToken(ctx, u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token works: %v", err)
	}
}

func TestMagicLinks(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")

	link, err := s.CreateMagicLink(ctx, u.ID, c.Now().Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ConsumeMagicLink(ctx, link)
	if err != nil || id != u.ID {
		t.Fatalf("consume = %d, %v", id, err)
	}
	if _, err := s.ConsumeMagicLink(ctx, link); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("second use: %v", err)
	}
	if _, err := s.ConsumeMagicLink(ctx, "garbage"); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("unknown: %v", err)
	}

	expiring, _ := s.CreateMagicLink(ctx, u.ID, c.Now().Add(15*time.Minute))
	c.Add(15 * time.Minute)
	if _, err := s.ConsumeMagicLink(ctx, expiring); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("expired: %v", err)
	}

	susp, _ := s.CreateMagicLink(ctx, u.ID, c.Now().Add(time.Minute))
	s.Suspend(ctx, "mike")
	if _, err := s.ConsumeMagicLink(ctx, susp); !errors.Is(err, ErrSuspended) {
		t.Errorf("suspended: %v", err)
	}

	if _, err := s.CreateMagicLink(ctx, 9999, c.Now().Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

// TestConsumeMagicLinkConcurrent checks single use under contention.
func TestConsumeMagicLinkConcurrent(t *testing.T) {
	s, c := open(t)
	u := mustUser(t, s, "mike", "mike@example.com")
	link, _ := s.CreateMagicLink(ctx, u.ID, c.Now().Add(time.Minute))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConsumeMagicLink(ctx, link); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrLinkInvalid) {
				t.Errorf("consume: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("link consumed %d times", wins)
	}
}
