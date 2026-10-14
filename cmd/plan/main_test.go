package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/api"
	"github.com/mglass/nowfile/internal/store"
)

var ctx = context.Background()

func TestParseUntil(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2026, 10, 13, 12, 4, 10, 0, ny)
	ok := []struct{ in, want string }{
		{"13:00", "2026-10-13T13:00:00-04:00"},
		{"23:59", "2026-10-13T23:59:00-04:00"},
		{"30m", "2026-10-13T12:34:10-04:00"},
		{"2h", "2026-10-13T14:04:10-04:00"},
		{"1h30m", "2026-10-13T13:34:10-04:00"},
		{"2026-10-13T13:00:00-04:00", "2026-10-13T13:00:00-04:00"},
		{"2026-10-14T00:00:00Z", "2026-10-14T00:00:00Z"},
		{" 13:00 ", "2026-10-13T13:00:00-04:00"},
	}
	for _, c := range ok {
		got, err := parseUntil(c.in, now)
		if err != nil || got.Format(time.RFC3339) != c.want {
			t.Errorf("parseUntil(%q) = %s, %v; want %s", c.in, got.Format(time.RFC3339), err, c.want)
		}
	}
	for _, in := range []string{
		"9:30",                      // passed today
		"12:04",                     // this minute, already passed
		"2026-10-13T12:00:00-04:00", // past RFC 3339
		"0m", "-5m",
		"24:00", "13:60", "1300", "13:0", "1pm", "tomorrow", "",
	} {
		if got, err := parseUntil(in, now); err == nil {
			t.Errorf("parseUntil(%q) = %v, want error", in, got)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		var out strings.Builder
		a := &app{stdout: &out, configPath: filepath.Join(t.TempDir(), "config")}
		if err := a.run(ctx, []string{arg}); err != nil {
			t.Fatalf("plan %s: %v", arg, err)
		}
		if got := out.String(); got != "dev\n" {
			t.Errorf("plan %s = %q, want %q", arg, got, "dev\n")
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nowfile", "config")
	if c, err := loadConfig(path); err != nil || c != (config{}) {
		t.Fatalf("missing config = %+v, %v", c, err)
	}
	want := config{BaseURL: "https://nowfile.org", Token: "nf_abc", Name: "mike"}
	if err := saveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, %v", fi.Mode(), err)
	}
	if got, err := loadConfig(path); err != nil || got != want {
		t.Errorf("load = %+v, %v; want %+v", got, err, want)
	}
	os.WriteFile(path, []byte("# comment\n\n base_url = http://localhost:8080/ \ntoken=nf_x\nextra=1\n"), 0o600)
	if got, _ := loadConfig(path); got != (config{BaseURL: "http://localhost:8080", Token: "nf_x"}) {
		t.Errorf("hand-written config = %+v", got)
	}
	os.WriteFile(path, []byte("token nf_x\n"), 0o600)
	if _, err := loadConfig(path); err == nil {
		t.Error("malformed line accepted")
	}
}

type env struct {
	st    *store.Store
	srv   *httptest.Server
	user  store.User
	token string
	now   time.Time
	app   *app
	out   *strings.Builder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{st: st, now: time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC), out: &strings.Builder{}}
	st.Now = func() time.Time { return e.now }
	if e.user, err = st.CreateUserHeld(ctx, "mike", "mike@example.com"); err != nil {
		t.Fatal(err)
	}
	if e.token, _, err = st.CreateToken(ctx, e.user.ID, "cli"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Register(mux, st)
	mux.HandleFunc("GET /mike.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("nowfile/0\nStatus: via https\n"))
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	e.app = &app{
		stdin:      bufio.NewReader(strings.NewReader("")),
		stdout:     e.out,
		client:     e.srv.Client(),
		now:        func() time.Time { return e.now },
		configPath: filepath.Join(t.TempDir(), "config"),
	}
	return e
}

func (e *env) login(t *testing.T) {
	t.Helper()
	if err := e.app.run(ctx, []string{"login", "--base-url", e.srv.URL, "--token", e.token, "--name", "Mike"}); err != nil {
		t.Fatalf("login: %v", err)
	}
}

func TestSetAndClear(t *testing.T) {
	e := newEnv(t)
	if err := e.app.run(ctx, []string{"hello"}); err == nil || !strings.Contains(err.Error(), "plan login") {
		t.Errorf("before login: %v", err)
	}
	e.login(t)
	if c, _ := loadConfig(e.app.configPath); c.Name != "mike" || c.Token != e.token || c.BaseURL != e.srv.URL {
		t.Errorf("saved config = %+v", c)
	}

	if err := e.app.run(ctx, []string{"--until", "30m", "lunch,", "back", "at", "1"}); err != nil {
		t.Fatal(err)
	}
	slots, _ := e.st.Slots(ctx, e.user.ID)
	if len(slots) != 1 || slots[0].Value != "lunch, back at 1" || !slots[0].Until.Equal(e.now.Add(30*time.Minute)) || slots[0].Source != "cli" {
		t.Fatalf("slots = %+v", slots)
	}

	// A server-side validation error comes back as the server's message.
	err := e.app.run(ctx, []string{"one\ntwo"})
	if err == nil || !strings.Contains(err.Error(), "one line") {
		t.Errorf("multi-line: %v", err)
	}

	if err := e.app.run(ctx, []string{"--clear"}); err != nil {
		t.Fatal(err)
	}
	if slots, _ := e.st.Slots(ctx, e.user.ID); len(slots) != 0 {
		t.Errorf("after clear: %+v", slots)
	}
	if err := e.app.run(ctx, []string{"--clear", "x"}); err == nil {
		t.Error("--clear with a line accepted")
	}
	if err := e.app.run(ctx, []string{}); err == nil {
		t.Error("empty status accepted")
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	e := newEnv(t)
	err := e.app.run(ctx, []string{"login", "--base-url", e.srv.URL, "--token", "nf_wrong", "--name", "mike"})
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("bad token: %v", err)
	}
	if _, err := os.Stat(e.app.configPath); err == nil {
		t.Error("config written for a bad token")
	}
}

func TestLoginPrompts(t *testing.T) {
	e := newEnv(t)
	e.app.stdin = bufio.NewReader(strings.NewReader(e.srv.URL + "\n" + e.token + "\nmike\n"))
	if err := e.app.run(ctx, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadConfig(e.app.configPath); c != (config{BaseURL: e.srv.URL, Token: e.token, Name: "mike"}) {
		t.Errorf("config = %+v", c)
	}
}

// fakeFinger answers one query per connection with a fixed document and
// records what it was asked.
func fakeFinger(t *testing.T) (port int, queries chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	queries = make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			queries <- line
			c.Write([]byte("nowfile/0\r\nStatus: via finger\r\n"))
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, queries
}

func TestShowFinger(t *testing.T) {
	e := newEnv(t)
	port, queries := fakeFinger(t)
	if err := e.app.run(ctx, []string{"show", "mike@127.0.0.1", "--port", strconv.Itoa(port)}); err != nil {
		t.Fatal(err)
	}
	if got := <-queries; got != "mike\r\n" {
		t.Errorf("query = %q", got)
	}
	if e.out.String() != "nowfile/0\nStatus: via finger\n" {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestShowFallsBackToHTTPS(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	e.out.Reset()
	// Nothing listens on this port, so finger fails and show falls back to
	// base_url, which names the same host.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := e.app.run(ctx, []string{"show", "--port", strconv.Itoa(closed)}); err != nil {
		t.Fatal(err)
	}
	if e.out.String() != "nowfile/0\nStatus: via https\n" {
		t.Errorf("output = %q", e.out.String())
	}
}

func TestShowArgs(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"show"}, // no target and no config
		{"show", "mike"},
		{"show", "@host"},
		{"show", "a@b@c"},
		{"show", "a@b", "extra"},
	} {
		if err := e.app.run(ctx, args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
