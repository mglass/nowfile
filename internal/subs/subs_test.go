package subs

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/store"
)

var ctx = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *clock) Set(v string) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type mailbox struct {
	mu   sync.Mutex
	msgs []mail.Message
	fail bool
}

func (m *mailbox) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("smtp down")
	}
	m.msgs = append(m.msgs, msg)
	return nil
}

func (m *mailbox) all() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail.Message(nil), m.msgs...)
}

type env struct {
	path  string
	st    *store.Store
	clock *clock
	mb    *mailbox
	svc   *Service
	user  store.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	start, _ := time.Parse(time.RFC3339, "2026-10-13T16:00:00Z")
	c := &clock{t: start}
	st.Now = c.Now
	u, err := st.CreateUserHeld(ctx, "mike", "mike@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutStatic(ctx, u.ID, store.Static{Name: "Mike Glass", Headline: "Builds tools."}); err != nil {
		t.Fatal(err)
	}
	mb := &mailbox{}
	return &env{path: path, st: st, clock: c, mb: mb, svc: New(st, mb, "https://nowfile.test"), user: u}
}

// restart closes the store and opens it again under a new Service, as a
// server restart would.
func (e *env) restart(t *testing.T) {
	t.Helper()
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.Now = e.clock.Now
	e.st = st
	e.svc = New(st, e.mb, "https://nowfile.test")
}

var linkRE = regexp.MustCompile(`https://nowfile\.test/subscribe/confirm/(\S+)`)

// subscribe runs the whole opt-in and returns the confirmed subscriber.
func (e *env) subscribe(t *testing.T, email string) store.Subscriber {
	t.Helper()
	before := len(e.mb.all())
	if err := e.svc.Subscribe(ctx, e.user, email, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	msgs := e.mb.all()
	if len(msgs) != before+1 {
		t.Fatalf("confirmation mails = %d", len(msgs)-before)
	}
	m := linkRE.FindStringSubmatch(msgs[len(msgs)-1].Text)
	if m == nil {
		t.Fatalf("no confirm link in %q", msgs[len(msgs)-1].Text)
	}
	sub, err := e.svc.Confirm(ctx, m[1])
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func (e *env) status(t *testing.T, v string) {
	t.Helper()
	var err error
	if v == "" {
		err = e.st.ClearSlot(ctx, e.user.ID, "Status")
	} else {
		_, err = e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Status", Value: v, Source: "web"})
	}
	if err != nil {
		t.Fatal(err)
	}
}

// statusAt sets the clock to at (RFC 3339) and writes the Status.
func (e *env) statusAt(t *testing.T, at, v string) {
	t.Helper()
	e.clock.Set(at)
	e.status(t, v)
}

// tickAt sets the clock to at (RFC 3339) and runs one notifier pass.
func (e *env) tickAt(t *testing.T, at string) []mail.Message {
	t.Helper()
	e.clock.Set(at)
	return e.tick(t)
}

func (e *env) tick(t *testing.T) []mail.Message {
	t.Helper()
	before := len(e.mb.all())
	if err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	return e.mb.all()[before:]
}

func TestConfirmationMail(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.Subscribe(ctx, e.user, " Reader@Example.com ", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	m := e.mb.all()[0]
	if m.To != "reader@example.com" || !strings.Contains(m.Subject, "mike") || !linkRE.MatchString(m.Text) {
		t.Fatalf("confirmation = %+v", m)
	}
	if err := e.svc.Subscribe(ctx, e.user, "not an address", "192.0.2.1"); !errors.Is(err, store.ErrInvalidEmail) {
		t.Fatalf("bad address: %v", err)
	}
}

func TestDigestContent(t *testing.T) {
	e := newEnv(t)
	sub := e.subscribe(t, "reader@example.com")

	// The owner has no time zone, so the digest is at 08:00 UTC.
	e.statusAt(t, "2026-10-13T17:00:00Z", "Lunch, back at 1")
	e.clock.Set("2026-10-13T18:00:00Z")
	if _, err := e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Calendar", Value: "Busy until 19:00", Until: "2026-10-13T19:00:00Z", Source: "calendar"}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set("2026-10-13T18:30:00Z")
	if _, err := e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Focus", Value: "Work", Source: "web"}); err != nil {
		t.Fatal(err)
	}
	e.statusAt(t, "2026-10-13T20:00:00Z", "")
	e.statusAt(t, "2026-10-14T07:30:00Z", "Writing")

	if got := e.tickAt(t, "2026-10-14T07:59:59Z"); len(got) != 0 {
		t.Fatalf("mail before 08:00: %+v", got)
	}
	// A change at 08:00 belongs to tomorrow's digest.
	e.statusAt(t, "2026-10-14T08:00:00Z", "After the cut")
	got := e.tickAt(t, "2026-10-14T08:00:30Z")
	if len(got) != 1 {
		t.Fatalf("digests = %d", len(got))
	}
	m := got[0]
	unsub := "https://nowfile.test/unsubscribe/" + sub.Unsub
	want := "Mike Glass (mike) now:\n\n  After the cut\n\n" +
		"Changes from Tue 13 Oct 08:00 to Wed 14 Oct 08:00 (UTC):\n\n" +
		"  Tue 13 Oct 17:00  Status: Lunch, back at 1\n" +
		"  Tue 13 Oct 18:00  Calendar: Busy until 19:00\n" +
		"  Tue 13 Oct 20:00  Status: cleared\n" +
		"  Wed 14 Oct 07:30  Status: Writing\n" +
		"\nhttps://nowfile.test/mike\n\n" +
		"You get one email a day from this page, at 08:00 its owner's time, and none on a day without changes.\n" +
		"Unsubscribe with one click: " + unsub + "\n"
	if m.To != "reader@example.com" || m.Subject != "mike: After the cut" || m.Text != want {
		t.Fatalf("digest to %q subject %q text:\n%s\nwant:\n%s", m.To, m.Subject, m.Text, want)
	}
	if m.Headers["List-Unsubscribe"] != "<"+unsub+">" || m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %+v", m.Headers)
	}
	if got := e.tickAt(t, "2026-10-14T20:00:00Z"); len(got) != 0 {
		t.Fatalf("second digest the same day: %+v", got)
	}
	got = e.tickAt(t, "2026-10-15T08:00:00Z")
	if len(got) != 1 || !strings.Contains(got[0].Text, "  Wed 14 Oct 08:00  Status: After the cut\n") || strings.Contains(got[0].Text, "Writing") {
		t.Fatalf("next day's digest = %+v", got)
	}
}

func TestDigestSkipsDayWithoutChanges(t *testing.T) {
	e := newEnv(t)
	e.status(t, "Before")
	e.subscribe(t, "reader@example.com")
	// Focus is not mailed about.
	e.clock.Set("2026-10-13T18:00:00Z")
	if _, err := e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Focus", Value: "Work", Source: "web"}); err != nil {
		t.Fatal(err)
	}
	if got := e.tickAt(t, "2026-10-14T08:00:00Z"); len(got) != 0 {
		t.Fatalf("mail on a day without changes: %+v", got)
	}
	// The quiet day still counts as dealt with: a change just after 08:00
	// waits for tomorrow instead of going out on the next tick.
	e.statusAt(t, "2026-10-14T08:05:00Z", "Out")
	if got := e.tickAt(t, "2026-10-14T08:10:00Z"); len(got) != 0 {
		t.Fatalf("mail after a skipped digest: %+v", got)
	}
	got := e.tickAt(t, "2026-10-15T08:00:00Z")
	if len(got) != 1 || !strings.Contains(got[0].Text, "Wed 14 Oct 08:05  Status: Out") {
		t.Fatalf("next digest = %+v", got)
	}
}

func TestDigestOncePerDayAcrossRestart(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "a@example.com")
	e.subscribe(t, "b@example.com")
	e.statusAt(t, "2026-10-13T17:00:00Z", "Hello")
	if got := e.tickAt(t, "2026-10-14T08:00:00Z"); len(got) != 2 {
		t.Fatalf("digests = %d", len(got))
	}
	e.restart(t)
	if got := e.tickAt(t, "2026-10-14T08:01:00Z"); len(got) != 0 {
		t.Fatalf("digest sent again after restart: %+v", got)
	}
	// A restart that missed 08:00 still sends that day's digest, once.
	e.statusAt(t, "2026-10-14T09:00:00Z", "Later")
	e.restart(t)
	if got := e.tickAt(t, "2026-10-15T11:00:00Z"); len(got) != 2 {
		t.Fatalf("late digests = %d", len(got))
	}
	e.restart(t)
	if got := e.tickAt(t, "2026-10-15T11:01:00Z"); len(got) != 0 {
		t.Fatalf("late digest sent again: %+v", got)
	}
}

func TestDigestTimeZoneBoundary(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetTimeZone(ctx, e.user.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	e.subscribe(t, "reader@example.com") // 12:00 on Tue 13 Oct in New York
	// 23:30 on Tue 13 Oct in New York, already Wed 14 Oct in UTC.
	e.statusAt(t, "2026-10-14T03:30:00Z", "Late night")
	// 08:00 UTC is 04:00 in New York: not yet.
	if got := e.tickAt(t, "2026-10-14T08:00:00Z"); len(got) != 0 {
		t.Fatalf("digest at 08:00 UTC: %+v", got)
	}
	if got := e.tickAt(t, "2026-10-14T11:59:59Z"); len(got) != 0 {
		t.Fatalf("digest before 08:00 New York: %+v", got)
	}
	got := e.tickAt(t, "2026-10-14T12:00:00Z")
	if len(got) != 1 {
		t.Fatalf("digests = %d", len(got))
	}
	if !strings.Contains(got[0].Text, "Changes from Tue 13 Oct 08:00 to Wed 14 Oct 08:00 (America/New_York)") ||
		!strings.Contains(got[0].Text, "  Tue 13 Oct 23:30  Status: Late night\n") {
		t.Fatalf("digest = %s", got[0].Text)
	}
	// 23:30 local on Wed 14 Oct is in Thursday's digest, not Wednesday's.
	e.statusAt(t, "2026-10-15T03:30:00Z", "Later still")
	if got := e.tickAt(t, "2026-10-15T08:00:00Z"); len(got) != 0 {
		t.Fatalf("digest at 08:00 UTC: %+v", got)
	}
	got = e.tickAt(t, "2026-10-15T12:00:00Z")
	if len(got) != 1 || !strings.Contains(got[0].Text, "  Wed 14 Oct 23:30  Status: Later still\n") || strings.Contains(got[0].Text, "Late night") {
		t.Fatalf("second digest = %+v", got)
	}
}

func TestDigestWindowAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// US clocks go back on Sun 1 Nov 2026: that window is 25 hours.
	now, _ := time.Parse(time.RFC3339, "2026-11-01T15:00:00Z")
	from, at := DigestWindow(now, ny)
	if f, a := from.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339); f != "2026-10-31T12:00:00Z" || a != "2026-11-01T13:00:00Z" {
		t.Fatalf("window = %s to %s", f, a)
	}
	// Before 08:00 local the latest digest is yesterday's.
	now, _ = time.Parse(time.RFC3339, "2026-11-01T12:30:00Z")
	if _, at := DigestWindow(now, ny); at.UTC().Format(time.RFC3339) != "2026-10-31T12:00:00Z" {
		t.Fatalf("at = %s", at.UTC())
	}
}

func TestFailedSendRetries(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	e.statusAt(t, "2026-10-13T17:00:00Z", "Hello")
	e.clock.Set("2026-10-14T08:00:00Z")
	e.mb.mu.Lock()
	e.mb.fail = true
	e.mb.mu.Unlock()
	if err := e.svc.Tick(ctx); err == nil {
		t.Fatal("tick with failing mail returned nil")
	}
	e.mb.mu.Lock()
	e.mb.fail = false
	e.mb.mu.Unlock()
	if got := e.tickAt(t, "2026-10-14T08:00:30Z"); len(got) != 1 {
		t.Fatalf("retry mails = %d", len(got))
	}
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("mailed again after retry: %+v", got)
	}
}

func TestUnsubscribeStopsMail(t *testing.T) {
	e := newEnv(t)
	gone := e.subscribe(t, "gone@example.com")
	e.subscribe(t, "stays@example.com")
	if err := e.st.Unsubscribe(ctx, gone.Unsub); err != nil {
		t.Fatal(err)
	}
	e.statusAt(t, "2026-10-13T17:00:00Z", "Hello")
	got := e.tickAt(t, "2026-10-14T08:00:00Z")
	if len(got) != 1 || got[0].To != "stays@example.com" {
		t.Fatalf("mails after unsubscribe = %+v", got)
	}
}

func TestSuspendedPageNotMailed(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	e.statusAt(t, "2026-10-13T17:00:00Z", "Hello")
	if err := e.st.Suspend(ctx, "mike"); err != nil {
		t.Fatal(err)
	}
	if got := e.tickAt(t, "2026-10-14T08:00:00Z"); len(got) != 0 {
		t.Fatalf("mail for suspended page: %+v", got)
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	// Per address: three confirmation mails a day, then silence, across IPs.
	for i := 0; i < 5; i++ {
		ip := "192.0.2." + string(rune('1'+i))
		if err := e.svc.Subscribe(ctx, e.user, "victim@example.com", ip); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.mb.all()); n != PerAddr {
		t.Fatalf("mails to one address = %d, want %d", n, PerAddr)
	}
	e.clock.Add(PerAddrWindow)
	if err := e.svc.Subscribe(ctx, e.user, "victim@example.com", "192.0.2.9"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.mb.all()); n != PerAddr+1 {
		t.Fatalf("after a day = %d", n)
	}

	// Per IP: five attempts an hour, then ErrRateLimited.
	for i := 0; i < PerIP; i++ {
		if err := e.svc.Subscribe(ctx, e.user, "r"+string(rune('a'+i))+"@example.com", "198.51.100.7"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Subscribe(ctx, e.user, "z@example.com", "198.51.100.7"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over IP limit: %v", err)
	}
	e.clock.Add(PerIPWindow)
	if err := e.svc.Subscribe(ctx, e.user, "z@example.com", "198.51.100.7"); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestAlreadySubscribedSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	n := len(e.mb.all())
	if err := e.svc.Subscribe(ctx, e.user, "reader@example.com", "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if len(e.mb.all()) != n {
		t.Fatal("mailed an address that is already subscribed")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("héllo", 2); got != "h…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Fatalf("truncate = %q", got)
	}
}
