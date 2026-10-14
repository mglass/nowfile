package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/ics"
	"github.com/mglass/nowfile/internal/store"
)

var ctx = context.Background()

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNormalizeURL(t *testing.T) {
	ok := map[string]string{
		"webcal://p01-calendars.icloud.com/published/2/abc":    "https://p01-calendars.icloud.com/published/2/abc",
		"WEBCAL://example.com/a.ics":                           "https://example.com/a.ics",
		"webcals://example.com/a.ics":                          "https://example.com/a.ics",
		"  https://calendar.google.com/x/basic.ics  ":          "https://calendar.google.com/x/basic.ics",
		"http://example.com/a.ics#frag":                        "http://example.com/a.ics",
		"https://outlook.office365.com/owa/calendar/r/cal.ics": "https://outlook.office365.com/owa/calendar/r/cal.ics",
	}
	for in, want := range ok {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                                   "Paste",
		"https://mike:hunter2@cal.example/x": "username or password",
		"https://mike@cal.example/x":         "username or password",
		"ftp://example.com/a.ics":            "https:// or webcal://",
		"caldav.example.com/cal":             "https:// or webcal://",
		"https:///nohost":                    "no host",
		"https://example.com/" + strings.Repeat("a", 2100): "2048",
	}
	for in, want := range bad {
		_, err := NormalizeURL(in)
		var e *Error
		if !errors.As(err, &e) || !strings.Contains(e.Msg, want) {
			t.Errorf("%q: err %v, want message containing %q", in, err, want)
		}
	}
}

func TestLineAndPreviewText(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := ts("2026-10-14T15:30:00Z") // Wednesday 11:30 in New York
	sp := func(a, b string) ics.Span { return ics.Span{Start: ts(a), End: ts(b)} }

	cases := []struct {
		name  string
		spans []ics.Span
		loc   *time.Location
		line  string
		text  string
	}{
		{"same day", []ics.Span{sp("2026-10-14T15:00:00Z", "2026-10-14T19:00:00Z")}, ny, "Busy until 15:00", "Busy until 15:00"},
		{"ends tomorrow", []ics.Span{sp("2026-10-14T15:00:00Z", "2026-10-15T13:00:00Z")}, ny, "Busy until Thu 09:00", ""},
		{"ends next month", []ics.Span{sp("2026-10-14T15:00:00Z", "2026-11-02T14:00:00Z")}, ny, "Busy until Nov 2 09:00", ""},
		{"no zone set", []ics.Span{sp("2026-10-14T15:00:00Z", "2026-10-14T19:00:00Z")}, time.UTC, "Busy until 19:00 UTC", ""},
		{"next later", []ics.Span{sp("2026-10-14T18:00:00Z", "2026-10-14T19:00:00Z")}, ny, "", "Nothing current, next busy at 14:00."},
		{"next tomorrow", []ics.Span{sp("2026-10-15T13:00:00Z", "2026-10-15T14:00:00Z")}, ny, "", "Nothing current, next busy at Thu 09:00."},
		{"nothing", nil, ny, "", "Nothing current, and nothing busy in the next week."},
		{"just ended", []ics.Span{sp("2026-10-14T14:00:00Z", "2026-10-14T15:30:00Z")}, ny, "", "Nothing current, and nothing busy in the next week."},
	}
	for _, c := range cases {
		p := Describe(c.spans, c.loc, now)
		if p.Line != c.line || (c.text != "" && p.Text != c.text) {
			t.Errorf("%s: line %q text %q; want line %q text %q", c.name, p.Line, p.Text, c.line, c.text)
		}
		if c.line != "" && !p.Until.Equal(c.spans[0].End) {
			t.Errorf("%s: until %s", c.name, p.Until)
		}
	}
}

// feed is a test calendar server with an ETag and a switchable failure.
type feed struct {
	mu       sync.Mutex
	body     string
	etag     string
	status   int // non-zero forces this status
	requests int
	lastINM  string
}

func (f *feed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.lastINM = r.Header.Get("If-None-Match")
	if f.status != 0 {
		http.Error(w, "nope", f.status)
		return
	}
	if f.etag != "" && f.lastINM == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", f.etag)
	w.Header().Set("Content-Type", "text/calendar")
	fmt.Fprint(w, f.body)
}

func (f *feed) set(fn func(*feed)) { f.mu.Lock(); fn(f); f.mu.Unlock() }
func (f *feed) get() (int, string) { f.mu.Lock(); defer f.mu.Unlock(); return f.requests, f.lastINM }

const meetings = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:1\r\nSUMMARY:SECRET Board meeting about layoffs\r\nDTSTART:20261014T150000Z\r\nDTEND:20261014T160000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:2\r\nSUMMARY:SECRET 1:1 with Dana\r\nDTSTART:20261014T160000Z\r\nDTEND:20261014T163000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:3\r\nSUMMARY:SECRET Dentist\r\nDTSTART:20261014T190000Z\r\nDTEND:20261014T200000Z\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestFetchErrors(t *testing.T) {
	f := NewFetcher(true)
	cases := []struct {
		h    http.HandlerFunc
		want string
	}{
		{func(w http.ResponseWriter, r *http.Request) { http.Error(w, "", 401) }, "needs a password"},
		{func(w http.ResponseWriter, r *http.Request) { http.Error(w, "", 403) }, "needs a password"},
		{func(w http.ResponseWriter, r *http.Request) { http.Error(w, "", 404) }, "404 Not Found"},
		{func(w http.ResponseWriter, r *http.Request) { http.Error(w, "", 502) }, "502 Bad Gateway"},
		{func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>Sign in</html>") }, "did not return a calendar"},
		{func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "BEGIN:VCALENDAR\r\n"+strings.Repeat("X-PAD:"+strings.Repeat("x", 1000)+"\r\n", 1100))
		}, "larger than 1 MB"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(c.h)
		_, _, err := f.Refresh(ctx, srv.URL, CalendarState{}, time.UTC, ts("2026-10-14T15:30:00Z"))
		srv.Close()
		if err == nil || !strings.Contains(Message(err), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) }))
	defer slow.Close()
	quick := NewFetcher(true)
	quick.client.Timeout = 100 * time.Millisecond
	if _, _, err := quick.Refresh(ctx, slow.URL, CalendarState{}, time.UTC, time.Now()); err == nil || !strings.Contains(Message(err), "did not answer") {
		t.Errorf("timeout: %v", err)
	}
}

func TestPrivateAddressRefused(t *testing.T) {
	fd := &feed{body: meetings}
	srv := httptest.NewServer(fd)
	defer srv.Close()
	_, _, err := NewFetcher(false).Refresh(ctx, srv.URL, CalendarState{}, time.UTC, time.Now())
	if err == nil || !strings.Contains(Message(err), "private network") {
		t.Fatalf("loopback fetch: %v", err)
	}
	if n, _ := fd.get(); n != 0 {
		t.Errorf("request reached the server")
	}
	for _, a := range []string{"10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "fe80::1", "0.0.0.0", "::ffff:127.0.0.1"} {
		ip, _ := parseAddr(a)
		if publicAddr(ip) {
			t.Errorf("%s counted as public", a)
		}
	}
	for _, a := range []string{"8.8.8.8", "17.253.144.10", "2001:4860:4860::8888"} {
		ip, _ := parseAddr(a)
		if !publicAddr(ip) {
			t.Errorf("%s counted as private", a)
		}
	}
}

type env struct {
	st    *store.Store
	clock *clock
	feed  *feed
	srv   *httptest.Server
	r     *Runner
	user  store.User
	logs  *strings.Builder
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: ts("2026-10-14T15:30:00Z")}
	st.Now = c.Now
	u, err := st.CreateUserHeld(ctx, "mike", "mike@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTimeZone(ctx, u.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	fd := &feed{body: meetings, etag: `"v1"`}
	srv := httptest.NewServer(fd)
	t.Cleanup(srv.Close)
	e := &env{st: st, clock: c, feed: fd, srv: srv, user: u, logs: &strings.Builder{}}
	e.r = NewRunner(st, NewFetcher(true))
	var mu sync.Mutex
	e.r.Logf = func(f string, a ...any) { mu.Lock(); fmt.Fprintf(e.logs, f+"\n", a...); mu.Unlock() }
	return e
}

func (e *env) calendar(t *testing.T) (value string, until time.Time, source string, set time.Time, ok bool) {
	t.Helper()
	slots, err := e.st.Slots(ctx, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		if s.Key == "Calendar" {
			return s.Value, s.Until, s.Source, s.Set, true
		}
	}
	return "", time.Time{}, "", time.Time{}, false
}

func (e *env) source(t *testing.T) store.Source {
	t.Helper()
	src, err := e.st.Source(ctx, e.user.ID, KindCalendar)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestRunnerLifecycle(t *testing.T) {
	e := newEnv(t)

	// Nothing to do with no sources.
	e.r.RunOnce(ctx)
	if _, _, _, _, ok := e.calendar(t); ok {
		t.Fatal("slot written with no source")
	}

	// Preview saves the source disabled and shows the merged line in the
	// user's zone. A disabled source publishes nothing.
	p, src, err := PreviewCalendar(ctx, e.st, e.r.fetch, e.user.ID, e.srv.URL, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Line != "Busy until 12:30" || src.Enabled {
		t.Fatalf("preview = %+v, enabled %v", p, src.Enabled)
	}
	e.r.RunOnce(ctx)
	if _, _, _, _, ok := e.calendar(t); ok {
		t.Fatal("disabled source published")
	}

	// Enabled: the next pass publishes from the preview's spans without a
	// fetch, since the preview just fetched.
	if err := e.st.SetSourceEnabled(ctx, e.user.ID, KindCalendar, true); err != nil {
		t.Fatal(err)
	}
	e.r.RunOnce(ctx)
	v, until, source, set, ok := e.calendar(t)
	if !ok || v != "Busy until 12:30" || !until.Equal(ts("2026-10-14T16:30:00Z")) || source != "calendar" {
		t.Fatalf("slot = %q until %s source %q ok %v", v, until, source, ok)
	}
	if n, _ := e.feed.get(); n != 1 {
		t.Errorf("requests = %d, want 1 (the preview)", n)
	}

	// A minute later nothing changed, so nothing is rewritten.
	e.clock.Add(time.Minute)
	e.r.RunOnce(ctx)
	if _, _, _, set2, _ := e.calendar(t); !set2.Equal(set) {
		t.Errorf("unchanged line rewritten: set %s then %s", set, set2)
	}

	// Five minutes on, the feed is fetched conditionally and answers 304.
	e.clock.Add(FetchEvery)
	e.r.RunOnce(ctx)
	if n, inm := e.feed.get(); n != 2 || inm != `"v1"` {
		t.Errorf("conditional fetch: requests %d, If-None-Match %q", n, inm)
	}
	if v, _, _, _, _ := e.calendar(t); v != "Busy until 12:30" {
		t.Errorf("after 304: %q", v)
	}

	// After the busy span ends the slot is gone, cleared by the runner.
	e.clock.Add(time.Hour) // 16:36Z
	e.r.RunOnce(ctx)
	if _, _, _, _, ok := e.calendar(t); ok {
		t.Error("slot still set after the meeting")
	}
	if pv := ViewCalendar(e.source(t), mustNY(t), e.clock.Now()); pv.Preview.Text != "Nothing current, next busy at 15:00." {
		t.Errorf("view = %q", pv.Preview.Text)
	}

	// Into the 19:00Z meeting, then the feed starts failing. The slot is
	// kept, and the error appears only after three failures in a row.
	e.clock.Add(2*time.Hour + 30*time.Minute) // 19:06Z
	e.r.RunOnce(ctx)
	if v, _, _, _, _ := e.calendar(t); v != "Busy until 16:00" {
		t.Fatalf("second meeting: %q", v)
	}
	e.feed.set(func(f *feed) { f.status = 500 })
	for i := 1; i <= 3; i++ {
		e.clock.Add(FetchEvery)
		e.r.RunOnce(ctx)
		src := e.source(t)
		if i < 3 && src.LastError != "" {
			t.Errorf("failure %d: error shown early: %q", i, src.LastError)
		}
		if i == 3 && !strings.Contains(src.LastError, "500") {
			t.Errorf("failure 3: last_error = %q", src.LastError)
		}
		if v, _, _, _, _ := e.calendar(t); v != "Busy until 16:00" {
			t.Errorf("failure %d: slot = %q", i, v)
		}
	}
	// It recovers on the next good fetch.
	e.feed.set(func(f *feed) { f.status = 0 })
	e.clock.Add(FetchEvery)
	e.r.RunOnce(ctx)
	if src := e.source(t); src.LastError != "" || src.LastRun.IsZero() {
		t.Errorf("after recovery: %+v", src)
	}

	// No subject ever reached the document or the logs.
	doc, _ := e.st.Document(ctx, e.user.ID, e.clock.Now())
	for _, s := range doc.Slots {
		if strings.Contains(s.Value, "SECRET") || !strings.HasPrefix(s.Value, "Busy until ") {
			t.Errorf("slot value %q", s.Value)
		}
	}
	if strings.Contains(e.logs.String(), "SECRET") || strings.Contains(e.logs.String(), e.srv.URL) {
		t.Errorf("logs leak content or the URL:\n%s", e.logs)
	}
}

func TestRunnerLeavesOtherWritersAlone(t *testing.T) {
	e := newEnv(t)
	e.clock.Add(-time.Hour) // 14:30Z, before any meeting
	if _, _, err := PreviewCalendar(ctx, e.st, e.r.fetch, e.user.ID, e.srv.URL, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.st.SetSourceEnabled(ctx, e.user.ID, KindCalendar, true)
	if _, err := e.st.WriteSlot(ctx, e.user.ID, store.SlotWrite{Key: "Calendar", Value: "On a plane", Source: "api"}); err != nil {
		t.Fatal(err)
	}
	e.r.RunOnce(ctx)
	if v, _, src, _, _ := e.calendar(t); v != "On a plane" || src != "api" {
		t.Errorf("runner cleared another writer's slot: %q %q", v, src)
	}
}

func TestRunnerSkipsEditedSource(t *testing.T) {
	e := newEnv(t)
	if _, _, err := PreviewCalendar(ctx, e.st, e.r.fetch, e.user.ID, e.srv.URL, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.st.SetSourceEnabled(ctx, e.user.ID, KindCalendar, true)
	src := e.source(t)
	// The user previews a different URL between the runner's read and its
	// write: the stale run must not publish.
	if _, err := e.st.PutSource(ctx, e.user.ID, KindCalendar, `{"url":"https://other.example/x.ics"}`, `{}`); err != nil {
		t.Fatal(err)
	}
	e.r.runCalendar(ctx, src)
	if _, _, _, _, ok := e.calendar(t); ok {
		t.Error("stale run published")
	}
}

func mustNY(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func parseAddr(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	return a.Unmap(), err
}
