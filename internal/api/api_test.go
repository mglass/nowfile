package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

var ctx = context.Background()

// now is where every fixture's clock starts: 16:00 UTC.
var now = time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC)

type fixture struct {
	now   time.Time // the store's clock
	st    *store.Store
	srv   *httptest.Server
	user  store.User
	token string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{now: now, st: st}
	st.Now = func() time.Time { return f.now }
	u, err := st.CreateUserHeld(ctx, "mike", "mike@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutStatic(ctx, u.ID, store.Static{Name: "Mike Glass", Headline: "Builds tools.", Links: []string{"https://example.com"}}); err != nil {
		t.Fatal(err)
	}
	plain, _, err := st.CreateToken(ctx, u.ID, "Mike's laptop")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, st)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.srv, f.user, f.token = srv, u, plain
	return f
}

// do sends a request with the fixture's token unless tok is given.
func (f *fixture) do(t *testing.T, method, path, body string, tok ...string) (int, string) {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, f.srv.URL+path, nil)
	} else {
		r, err = http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	}
	if err != nil {
		t.Fatal(err)
	}
	bearer := f.token
	if len(tok) > 0 {
		bearer = tok[0]
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func (f *fixture) slots(t *testing.T) []format.Slot {
	t.Helper()
	s, err := f.st.Slots(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuth(t *testing.T) {
	f := setup(t)
	suspendedTok := func() string {
		u, err := f.st.CreateUser(ctx, "ann", "ann@example.com")
		if err != nil {
			t.Fatal(err)
		}
		p, _, err := f.st.CreateToken(ctx, u.ID, "phone")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.Suspend(ctx, "ann"); err != nil {
			t.Fatal(err)
		}
		return p
	}()
	cases := []struct{ name, tok string }{
		{"wrong token", "nf_not-a-real-token"},
		{"no prefix", "garbage"},
		{"suspended account", suspendedTok},
	}
	for _, path := range []string{"GET /api/v1/me", "PUT /api/v1/slots/Status", "DELETE /api/v1/slots/Status"} {
		method, p, _ := strings.Cut(path, " ")
		body := ""
		if method == "PUT" {
			body = `{"value":"hi"}`
		}
		r, _ := http.NewRequest(method, f.srv.URL+p, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s without token: %d %q", path, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
		for _, c := range cases {
			code, b := f.do(t, method, p, body, c.tok)
			if code != http.StatusUnauthorized || !strings.Contains(b, `"error"`) {
				t.Errorf("%s with %s: %d %s", path, c.name, code, b)
			}
		}
	}
	if len(f.slots(t)) != 0 {
		t.Error("unauthenticated write landed")
	}
}

func TestTokenLastUsed(t *testing.T) {
	f := setup(t)
	if code, _ := f.do(t, "GET", "/api/v1/me", ""); code != http.StatusOK {
		t.Fatalf("me: %d", code)
	}
	toks, err := f.st.Tokens(ctx, f.user.ID)
	if err != nil || len(toks) != 1 || !toks[0].LastUsed.Equal(now) {
		t.Errorf("tokens = %+v, %v; want last_used %v", toks, err, now)
	}
}

func TestPutValidation(t *testing.T) {
	f := setup(t)
	cases := []struct{ name, key, body, want string }{
		{"unknown key", "Mood", `{"value":"ok"}`, "unknown slot key"},
		{"static key", "Name", `{"value":"ok"}`, "unknown slot key"},
		{"not json", "Status", `value=ok`, "JSON"},
		{"unknown field", "Status", `{"value":"ok","colour":"red"}`, "JSON"},
		{"empty value", "Status", `{"value":""}`, "value is required"},
		{"missing value", "Status", `{}`, "value is required"},
		{"multi-line value", "Status", `{"value":"one\ntwo"}`, "one line"},
		{"blank value", "Status", `{"value":"   "}`, "value is required"},
		{"value too long", "Status", `{"value":"` + strings.Repeat("x", 200) + `"}`, "limit 200"},
		{"bad until", "Status", `{"value":"ok","until":"13:00"}`, "RFC 3339"},
		{"past until", "Status", `{"value":"ok","until":"2026-10-13T15:59:00Z"}`, "not in the future"},
		{"until now", "Status", `{"value":"ok","until":"2026-10-13T16:00:00Z"}`, "not in the future"},
		{"source with space", "Status", `{"value":"ok","source":"my phone"}`, "source"},
		{"source too long", "Status", `{"value":"ok","source":"` + strings.Repeat("s", 33) + `"}`, "source"},
		{"body too large", "Status", `{"value":"` + strings.Repeat("x", 5000) + `"}`, "JSON"},
	}
	for _, c := range cases {
		code, b := f.do(t, "PUT", "/api/v1/slots/"+c.key, c.body)
		var e struct{ Error string }
		if code != http.StatusUnprocessableEntity || json.Unmarshal([]byte(b), &e) != nil || !strings.Contains(e.Error, c.want) {
			t.Errorf("%s: %d %s; want 422 containing %q", c.name, code, b, c.want)
		}
	}
	if len(f.slots(t)) != 0 {
		t.Errorf("invalid writes landed: %+v", f.slots(t))
	}
	// Padding is trimmed, the same as on the account page.
	if code, b := f.do(t, "PUT", "/api/v1/slots/Status", `{"value":" hi "}`); code != http.StatusNoContent {
		t.Fatalf("padded value: %d %s", code, b)
	}
	if sl := f.slots(t); len(sl) != 1 || sl[0].Value != "hi" {
		t.Errorf("padded value stored as %+v", sl)
	}
}

func TestPutSuccess(t *testing.T) {
	f := setup(t)
	code, b := f.do(t, "PUT", "/api/v1/slots/status", `{"value":"Lunch, back at 1","until":"2026-10-13T13:00:00-04:00"}`)
	if code != http.StatusNoContent || b != "" {
		t.Fatalf("PUT: %d %q", code, b)
	}
	s := f.slots(t)
	if len(s) != 1 {
		t.Fatalf("slots = %+v", s)
	}
	want := format.Slot{Key: "Status", Value: "Lunch, back at 1", Set: now, Until: time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC), Source: "mike-s-laptop"}
	if got := s[0]; got.Key != want.Key || got.Value != want.Value || !got.Set.Equal(want.Set) || !got.Until.Equal(want.Until) || got.Source != want.Source {
		t.Errorf("slot = %+v, want %+v", got, want)
	}

	// Explicit source, no until, replaces the previous value.
	if code, b := f.do(t, "PUT", "/api/v1/slots/Focus", `{"value":"Work","source":"shortcuts"}`); code != http.StatusNoContent {
		t.Fatalf("PUT Focus: %d %s", code, b)
	}
	if code, b := f.do(t, "PUT", "/api/v1/slots/Focus", `{"value":"Personal","source":"shortcuts"}`); code != http.StatusNoContent {
		t.Fatalf("PUT Focus again: %d %s", code, b)
	}
	s = f.slots(t)
	if len(s) != 2 || s[1].Key != "Focus" || s[1].Value != "Personal" || !s[1].Until.IsZero() || s[1].Source != "shortcuts" {
		t.Errorf("slots = %+v", s)
	}
}

func TestDelete(t *testing.T) {
	f := setup(t)
	f.do(t, "PUT", "/api/v1/slots/Status", `{"value":"hi"}`)
	if code, b := f.do(t, "DELETE", "/api/v1/slots/STATUS", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", code, b)
	}
	if len(f.slots(t)) != 0 {
		t.Error("slot not cleared")
	}
	if code, _ := f.do(t, "DELETE", "/api/v1/slots/Status", ""); code != http.StatusNoContent {
		t.Errorf("DELETE of unset slot: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/v1/slots/Mood", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("DELETE unknown key: %d", code)
	}
}

func TestMe(t *testing.T) {
	f := setup(t)
	f.do(t, "PUT", "/api/v1/slots/Status", `{"value":"Lunch","until":"2026-10-13T16:30:00Z"}`)
	f.do(t, "PUT", "/api/v1/slots/Focus", `{"value":"Work"}`)

	get := func() format.DocumentJSON {
		t.Helper()
		code, b := f.do(t, "GET", "/api/v1/me", "")
		if code != http.StatusOK {
			t.Fatalf("me: %d %s", code, b)
		}
		var j format.DocumentJSON
		if err := json.Unmarshal([]byte(b), &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	j := get()
	if j.Version != "0" || j.Name != "Mike Glass" || j.Current != "Lunch" || len(j.Links) != 1 || len(j.Slots) != 2 {
		t.Fatalf("me = %+v", j)
	}
	if j.Slots[0].Until != "2026-10-13T16:30:00Z" || j.Slots[0].Set != "2026-10-13T16:00:00Z" || j.Slots[0].Source != "mike-s-laptop" {
		t.Errorf("Status slot = %+v", j.Slots[0])
	}

	// Past the Status expiry, the row is still in the table but must not be
	// emitted.
	f.now = f.now.Add(31 * time.Minute)
	j = get()
	if len(j.Slots) != 1 || j.Slots[0].Key != "Focus" || j.Current != "Work" {
		t.Errorf("after expiry: %+v", j)
	}
}

func TestSourceFromLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Mike's laptop":           "mike-s-laptop",
		"phone":                   "phone",
		"  CLI  ":                 "cli",
		"iPhone 16 Pro":           "iphone-16-pro",
		"🙂":                       "api",
		"":                        "api",
		strings.Repeat("ab-", 20): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab",
	} {
		if got := sourceFromLabel(in); got != want {
			t.Errorf("sourceFromLabel(%q) = %q, want %q", in, got, want)
		}
		if got := sourceFromLabel(in); len(got) > store.MaxSourceBytes {
			t.Errorf("sourceFromLabel(%q) too long: %q", in, got)
		}
	}
}
