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
	st    *store.Store
	clock *clock
	mb    *mailbox
	svc   *Service
	user  store.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
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
	return &env{st: st, clock: c, mb: mb, svc: New(st, mb, "https://nowfile.test"), user: u}
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

func TestNotifyOnHeadlineChange(t *testing.T) {
	e := newEnv(t)
	e.status(t, "Before")
	sub := e.subscribe(t, "reader@example.com")

	// Nothing new since confirming: no mail.
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("mail with no change: %+v", got)
	}

	e.status(t, "Lunch, back at 1")
	got := e.tick(t)
	if len(got) != 1 {
		t.Fatalf("mails = %d", len(got))
	}
	m := got[0]
	unsub := "https://nowfile.test/unsubscribe/" + sub.Unsub
	if m.To != "reader@example.com" || m.Subject != "mike: Lunch, back at 1" ||
		!strings.Contains(m.Text, "Mike Glass (mike) now:\n\n  Lunch, back at 1") ||
		!strings.Contains(m.Text, unsub) {
		t.Fatalf("notification = %+v", m)
	}
	if m.Headers["List-Unsubscribe"] != "<"+unsub+">" || m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %+v", m.Headers)
	}
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("second tick mailed again: %+v", got)
	}
}

func TestNotifyCollapsesWithinInterval(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	e.status(t, "One")
	if got := e.tick(t); len(got) != 1 {
		t.Fatalf("first change mails = %d", len(got))
	}
	e.clock.Add(time.Minute)
	e.status(t, "Two")
	e.clock.Add(time.Minute)
	e.status(t, "Three")
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("mailed inside the 15 minutes: %+v", got)
	}
	e.clock.Add(Interval - 2*time.Minute)
	got := e.tick(t)
	if len(got) != 1 || got[0].Subject != "mike: Three" {
		t.Fatalf("collapsed mail = %+v", got)
	}
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("extra mail: %+v", got)
	}
}

func TestNoMailWhenHeadlineUnchanged(t *testing.T) {
	e := newEnv(t)
	e.status(t, "Typed")
	e.subscribe(t, "reader@example.com")
	// A Calendar change under a typed Status leaves the headline alone.
	until := e.clock.Now().Add(time.Hour).Format(time.RFC3339)
	if _, err := e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Calendar", Value: "Busy until 17:00", Until: until, Source: "calendar"}); err != nil {
		t.Fatal(err)
	}
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("mail for unchanged headline: %+v", got)
	}
	// Clearing the Status shows the Calendar line: a change.
	e.status(t, "")
	got := e.tick(t)
	if len(got) != 1 || got[0].Subject != "mike: Busy until 17:00" {
		t.Fatalf("after clear = %+v", got)
	}
}

func TestFailedSendRetries(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	e.status(t, "Hello")
	e.mb.mu.Lock()
	e.mb.fail = true
	e.mb.mu.Unlock()
	if err := e.svc.Tick(ctx); err == nil {
		t.Fatal("tick with failing mail returned nil")
	}
	e.mb.mu.Lock()
	e.mb.fail = false
	e.mb.mu.Unlock()
	if got := e.tick(t); len(got) != 1 {
		t.Fatalf("retry mails = %d", len(got))
	}
}

func TestUnsubscribeStopsMail(t *testing.T) {
	e := newEnv(t)
	sub := e.subscribe(t, "reader@example.com")
	if err := e.st.Unsubscribe(ctx, sub.Unsub); err != nil {
		t.Fatal(err)
	}
	e.status(t, "Hello")
	if got := e.tick(t); len(got) != 0 {
		t.Fatalf("mail after unsubscribe: %+v", got)
	}
}

func TestSuspendedPageNotMailed(t *testing.T) {
	e := newEnv(t)
	e.subscribe(t, "reader@example.com")
	e.status(t, "Hello")
	if err := e.st.Suspend(ctx, "mike"); err != nil {
		t.Fatal(err)
	}
	if got := e.tick(t); len(got) != 0 {
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
