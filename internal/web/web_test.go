package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/config"
	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/store"
)

var ctx = context.Background()

// clock is a settable time source for Store.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// mailbox records sent mail.
type mailbox struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (m *mailbox) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

func (m *mailbox) last(t *testing.T) mail.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		t.Fatal("no mail sent")
	}
	return m.msgs[len(m.msgs)-1]
}

func (m *mailbox) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.msgs) }

type env struct {
	t     *testing.T
	st    *store.Store
	clock *clock
	mail  *mailbox
	srv   *httptest.Server
	c     *http.Client
	site  *Server
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
	mb := &mailbox{}
	e := &env{t: t, st: st, clock: c, mail: mb}
	// The handler needs BaseURL before the server URL is known, so it is
	// built behind a late-bound handler.
	var h http.Handler
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	s, err := New(config.Config{BaseURL: e.srv.URL}, st, mb)
	if err != nil {
		t.Fatal(err)
	}
	h = s
	e.site = s
	e.c = e.newClient()
	return e
}

func (e *env) newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type resp struct {
	code   int
	body   string
	header http.Header
}

func (e *env) do(c *http.Client, method, path string, form url.Values) resp {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, string(b), r.Header}
}

func (e *env) get(path string) resp { e.t.Helper(); return e.do(e.c, "GET", path, nil) }
func (e *env) post(path string, form url.Values) resp {
	e.t.Helper()
	return e.do(e.c, "POST", path, form)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// csrf loads path and returns the form's CSRF token.
func (e *env) csrf(path string) string {
	e.t.Helper()
	m := csrfRE.FindStringSubmatch(e.get(path).body)
	if m == nil {
		e.t.Fatalf("no csrf field on %s", path)
	}
	return m[1]
}

var linkRE = regexp.MustCompile(`/login/[A-Za-z0-9_-]+`)

// signIn signs up name and follows the emailed link, leaving e.c signed in.
func (e *env) signIn(name string) {
	e.t.Helper()
	r := e.post("/signup", url.Values{"csrf": {e.csrf("/signup")}, "name": {name}, "email": {name + "@example.com"}})
	if r.code != 200 || !strings.Contains(r.body, "Check your email") {
		e.t.Fatalf("signup %s: %d %s", name, r.code, r.body)
	}
	link := linkRE.FindString(e.mail.last(e.t).Text)
	if r := e.get(link); r.code != http.StatusSeeOther || r.header.Get("Location") != "/account" {
		e.t.Fatalf("consume link: %d %s", r.code, r.header.Get("Location"))
	}
}

func (e *env) user(name string) store.User {
	e.t.Helper()
	u, err := e.st.UserByName(ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func TestLandingAndHealth(t *testing.T) {
	e := newEnv(t)
	r := e.get("/")
	if r.code != 200 || !strings.Contains(r.body, "finger mike@nowfile.org") {
		t.Fatalf("landing: %d", r.code)
	}
	if r := e.get("/health"); r.code != 200 || r.body != "ok\n" {
		t.Fatalf("health: %d %q", r.code, r.body)
	}
	if r := e.get("/static/style.css"); r.code != 200 || !strings.Contains(r.body, "prefers-color-scheme") {
		t.Fatalf("style.css: %d", r.code)
	}
	csp := r.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestPublicPages(t *testing.T) {
	e := newEnv(t)
	u, err := e.st.CreateUser(ctx, "alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	err = e.st.PutStatic(ctx, u.ID, store.Static{
		Name: "Alice Example", Headline: "Makes things.", Photo: "https://example.com/a.jpg",
		Links: []string{"https://example.com"}, Plan: "Line one.\n<b>not bold</b>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutSlot(ctx, u.ID, "Status", "Lunch, back at 1", e.clock.Now().Add(time.Hour), "web"); err != nil {
		t.Fatal(err)
	}

	html := e.get("/alice")
	if html.code != 200 || !strings.HasPrefix(html.header.Get("Content-Type"), "text/html") {
		t.Fatalf("html: %d %s", html.code, html.header.Get("Content-Type"))
	}
	for _, want := range []string{
		`<meta property="og:title" content="Alice Example">`,
		`<meta property="og:description" content="Lunch, back at 1">`,
		`<meta property="og:image" content="https://example.com/a.jpg">`,
		`<pre class="plan">Line one.` + "\n" + `&lt;b&gt;not bold&lt;/b&gt;</pre>`,
		`finger alice@`,
	} {
		if !strings.Contains(html.body, want) {
			t.Errorf("html missing %q", want)
		}
	}

	txt := e.get("/alice.txt")
	if txt.code != 200 || txt.header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("txt: %d %s", txt.code, txt.header.Get("Content-Type"))
	}
	doc, _ := e.st.DocumentByName(ctx, "alice", e.clock.Now())
	if txt.body != format.Render(doc, true) {
		t.Errorf("txt body:\n%s\nwant:\n%s", txt.body, format.Render(doc, true))
	}

	js := e.get("/alice.json")
	if js.code != 200 || js.header.Get("Content-Type") != "application/json" {
		t.Fatalf("json: %d %s", js.code, js.header.Get("Content-Type"))
	}
	var got format.DocumentJSON
	if err := json.Unmarshal([]byte(js.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Alice Example" || got.Plan != doc.Plan || len(got.Slots) != 1 || got.Slots[0].Until != "2026-10-13T17:00:00Z" || got.Slots[0].Set != "2026-10-13T16:00:00Z" {
		t.Errorf("json = %+v", got)
	}

	// Case-insensitive lookup.
	if r := e.get("/Alice.txt"); r.code != 200 || r.body != txt.body {
		t.Errorf("/Alice.txt: %d", r.code)
	}
}

func TestNotFoundIsUniform(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"susp", "gone", "live"} {
		if _, err := e.st.CreateUser(ctx, n, n+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.Suspend(ctx, "susp"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteUser(ctx, e.user("gone").ID); err != nil {
		t.Fatal(err)
	}
	ref := map[string]resp{}
	for _, suffix := range []string{"", ".txt", ".json"} {
		ref[suffix] = e.get("/nobody-here" + suffix)
	}
	for _, name := range []string{"nobody-here", "susp", "gone", "admin", "x.y", "a%2Fb"} {
		for _, suffix := range []string{"", ".txt", ".json"} {
			r := e.get("/" + name + suffix)
			if r.code != 404 {
				t.Errorf("/%s%s: %d, want 404", name, suffix, r.code)
				continue
			}
			if r.body != ref[suffix].body {
				t.Errorf("/%s%s body differs from unknown user's:\n%s", name, suffix, r.body)
			}
		}
	}
	if r := e.get("/live"); r.code != 200 {
		t.Errorf("/live: %d", r.code)
	}
}

// TestRenderingsAgree checks that the HTML, text, and JSON show the same
// slots and drop expired ones without any write happening.
func TestRenderingsAgree(t *testing.T) {
	e := newEnv(t)
	u, err := e.st.CreateUser(ctx, "bob", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	now := e.clock.Now()
	put := func(key, value string, until time.Time) {
		if _, err := e.st.PutSlot(ctx, u.ID, key, value, until, "test"); err != nil {
			t.Fatal(err)
		}
	}
	put("Status", "Short-lived status", now.Add(30*time.Minute))
	put("Listening", "Harmonia's Dream", now.Add(2*time.Hour))
	put("Focus", "Work", time.Time{})

	check := func(want, gone []string) {
		t.Helper()
		html, txt, js := e.get("/bob").body, e.get("/bob.txt").body, e.get("/bob.json").body
		var got format.DocumentJSON
		if err := json.Unmarshal([]byte(js), &got); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, s := range got.Slots {
			keys = append(keys, s.Key)
		}
		var wantKeys []string
		for _, w := range want {
			wantKeys = append(wantKeys, strings.SplitN(w, "=", 2)[0])
		}
		if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("json slots = %v, want %v", keys, wantKeys)
		}
		for _, w := range want {
			k, v, _ := strings.Cut(w, "=")
			esc := strings.ReplaceAll(v, "'", "&#39;")
			if !strings.Contains(txt, k+": "+v+"\n") {
				t.Errorf("txt missing %s", w)
			}
			if !strings.Contains(html, "<dt>"+k+"</dt>") || !strings.Contains(html, esc) {
				t.Errorf("html missing %s", w)
			}
		}
		for _, g := range gone {
			k, v, _ := strings.Cut(g, "=")
			if strings.Contains(txt, v) || strings.Contains(html, v) || strings.Contains(js, v) ||
				strings.Contains(txt, k+":") || strings.Contains(html, "<dt>"+k+"</dt>") {
				t.Errorf("expired %s still served", g)
			}
		}
	}

	check([]string{"Status=Short-lived status", "Focus=Work", "Listening=Harmonia's Dream"}, nil)
	e.clock.Add(45 * time.Minute)
	check([]string{"Focus=Work", "Listening=Harmonia's Dream"}, []string{"Status=Short-lived status"})
	e.clock.Add(2 * time.Hour)
	check([]string{"Focus=Work"}, []string{"Status=Short-lived status", "Listening=Harmonia's Dream"})
}

func TestSignupValidation(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.CreateUser(ctx, "taken", "taken@example.com"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, email, want string
	}{
		{"admin", "a@example.com", "reserved"},
		{"login", "a@example.com", "reserved"},
		{"health", "a@example.com", "reserved"},
		{"9lives", "a@example.com", "must start with a letter"},
		{"a", "a@example.com", "2 to 32"},
		{"bad--name", "a@example.com", "two hyphens"},
		{"trailing-", "a@example.com", "hyphen"},
		{"under_score", "a@example.com", "letters, digits, and hyphens"},
		{"taken", "a@example.com", "taken"},
		{"Taken", "a@example.com", "taken"},
		{"newname", "not-an-email", "email address"},
	}
	for _, c := range cases {
		r := e.post("/signup", url.Values{"csrf": {e.csrf("/signup")}, "name": {c.name}, "email": {c.email}})
		if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, c.want) {
			t.Errorf("signup %q %q: %d, want 422 containing %q", c.name, c.email, r.code, c.want)
		}
	}
	if e.mail.count() != 0 {
		t.Errorf("rejected signups sent %d mails", e.mail.count())
	}

	// An email that already has an account gets a sign-in link for it and
	// the same page a new sign-up sees.
	r := e.post("/signup", url.Values{"csrf": {e.csrf("/signup")}, "name": {"another"}, "email": {"Taken@Example.com"}})
	if r.code != 200 || !strings.Contains(r.body, "Check your email") {
		t.Fatalf("existing email: %d", r.code)
	}
	if m := e.mail.last(t); m.To != "taken@example.com" || !strings.Contains(m.Text, "as taken:") {
		t.Errorf("mail = %+v", m)
	}
	if _, err := e.st.UserByName(ctx, "another"); err == nil {
		t.Error("second account created for one email")
	}

	// Mixed case is lowercased.
	r = e.post("/signup", url.Values{"csrf": {e.csrf("/signup")}, "name": {"Carol-B"}, "email": {"carol@example.com"}})
	if r.code != 200 {
		t.Fatalf("signup Carol-B: %d %s", r.code, r.body)
	}
	if e.user("carol-b").Email != "carol@example.com" {
		t.Error("carol-b not created")
	}
}

func TestMagicLinkFlow(t *testing.T) {
	e := newEnv(t)
	if r := e.get("/account"); r.code != http.StatusSeeOther || r.header.Get("Location") != "/login" {
		t.Fatalf("signed-out /account: %d %s", r.code, r.header.Get("Location"))
	}
	r := e.post("/signup", url.Values{"csrf": {e.csrf("/signup")}, "name": {"dana"}, "email": {"dana@example.com"}})
	if r.code != 200 {
		t.Fatalf("signup: %d", r.code)
	}
	m := e.mail.last(t)
	if m.To != "dana@example.com" || !strings.Contains(m.Text, e.srv.URL+"/login/") {
		t.Fatalf("mail = %+v", m)
	}
	link := linkRE.FindString(m.Text)

	req, _ := http.NewRequest("GET", e.srv.URL+link, nil)
	raw, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw.Body.Close()
	if raw.StatusCode != http.StatusSeeOther || raw.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("consume: %d %q", raw.StatusCode, raw.Header.Get("Referrer-Policy"))
	}
	var sess *http.Cookie
	for _, c := range raw.Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.MaxAge <= 0 {
		t.Fatalf("session cookie = %+v", sess)
	}
	if r := e.get("/account"); r.code != 200 || !strings.Contains(r.body, "/dana") {
		t.Fatalf("/account: %d", r.code)
	}

	// The link works once, even in another browser.
	if r := e.do(e.newClient(), "GET", link, nil); r.code != http.StatusBadRequest {
		t.Errorf("reused link: %d", r.code)
	}
	// Login by email sends a fresh link; unknown emails get the same page.
	other := e.newClient()
	tok := csrfRE.FindStringSubmatch(e.do(other, "GET", "/login", nil).body)[1]
	n := e.mail.count()
	known := e.do(other, "POST", "/login", url.Values{"csrf": {tok}, "email": {"DANA@example.com"}})
	unknown := e.do(other, "POST", "/login", url.Values{"csrf": {tok}, "email": {"nobody@example.com"}})
	if known.code != 200 || known.body != unknown.body {
		t.Errorf("login pages differ for known and unknown email")
	}
	if e.mail.count() != n+1 {
		t.Errorf("login sent %d mails, want 1", e.mail.count()-n)
	}
	// Links expire.
	e.clock.Add(linkTTL)
	if r := e.do(other, "GET", linkRE.FindString(e.mail.last(t).Text), nil); r.code != http.StatusBadRequest {
		t.Errorf("expired link: %d", r.code)
	}

	// Logout ends the session server-side.
	if r := e.post("/logout", url.Values{"csrf": {e.csrf("/account")}}); r.code != http.StatusSeeOther {
		t.Fatalf("logout: %d", r.code)
	}
	if _, err := e.st.SessionUser(ctx, sess.Value); err == nil {
		t.Error("session still valid after logout")
	}
	if r := e.get("/account"); r.code != http.StatusSeeOther {
		t.Errorf("/account after logout: %d", r.code)
	}
}

func TestSecureCookieOnHTTPS(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, _ := st.CreateUser(ctx, "erin", "erin@example.com")
	s, err := New(config.Config{BaseURL: "https://nowfile.org"}, st, &mailbox{})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := st.CreateMagicLink(ctx, u.ID, time.Now().Add(time.Minute))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/login/"+tok, nil))
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].Secure || !c[0].HttpOnly || c[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookies = %+v", c)
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	// Signed out: no token, wrong token, and a token without its cookie.
	if r := e.post("/signup", url.Values{"name": {"frank"}, "email": {"f@example.com"}}); r.code != http.StatusForbidden {
		t.Errorf("signup without csrf: %d", r.code)
	}
	tok := e.csrf("/signup")
	if r := e.post("/signup", url.Values{"csrf": {tok + "x"}, "name": {"frank"}, "email": {"f@example.com"}}); r.code != http.StatusForbidden {
		t.Errorf("signup with wrong csrf: %d", r.code)
	}
	if r := e.do(e.newClient(), "POST", "/login", url.Values{"csrf": {tok}, "email": {"f@example.com"}}); r.code != http.StatusForbidden {
		t.Errorf("login with token but no cookie: %d", r.code)
	}
	if _, err := e.st.UserByName(ctx, "frank"); err == nil {
		t.Error("account created without valid csrf")
	}

	// Signed in: the signed-out cookie token is not accepted.
	e.signIn("frank")
	for _, form := range []url.Values{
		{"action": {"status"}, "value": {"hacked"}, "expiry": {"none"}},
		{"action": {"status"}, "value": {"hacked"}, "expiry": {"none"}, "csrf": {tok}},
	} {
		if r := e.post("/account", form); r.code != http.StatusForbidden {
			t.Errorf("account post %v: %d", form, r.code)
		}
	}
	if r := e.post("/logout", url.Values{}); r.code != http.StatusForbidden {
		t.Errorf("logout without csrf: %d", r.code)
	}
	if slots, _ := e.st.Slots(ctx, e.user("frank").ID); len(slots) != 0 {
		t.Errorf("slots written without csrf: %+v", slots)
	}
	good := url.Values{"action": {"status"}, "value": {"ok"}, "expiry": {"none"}, "csrf": {e.csrf("/account")}}
	if r := e.post("/account", good); r.code != http.StatusSeeOther {
		t.Errorf("account post with csrf: %d", r.code)
	}
}

func TestAccountPage(t *testing.T) {
	e := newEnv(t)
	e.signIn("gwen")
	u := e.user("gwen")
	tok := e.csrf("/account")
	post := func(form url.Values) resp {
		t.Helper()
		form.Set("csrf", tok)
		return e.post("/account", form)
	}

	// Profile, including the Plan block.
	r := post(url.Values{"action": {"profile"}, "name": {"Gwen"}, "headline": {"Writes."}, "photo": {"https://example.com/g.png"},
		"link": {"https://a.example", "", "https://b.example", "", ""}, "plan": {"Back Monday.\r\nMaybe."}})
	if r.code != http.StatusSeeOther {
		t.Fatalf("profile: %d %s", r.code, r.body)
	}
	st, _ := e.st.GetStatic(ctx, u.ID)
	if st.Name != "Gwen" || len(st.Links) != 2 || st.Plan != "Back Monday.\nMaybe." {
		t.Errorf("static = %+v", st)
	}
	if r := post(url.Values{"action": {"profile"}, "link": {"javascript:alert(1)"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("javascript link: %d", r.code)
	}
	if r := post(url.Values{"action": {"profile"}, "link": {"https://1", "https://2", "https://3", "https://4", "https://5", "https://6"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("six links: %d", r.code)
	}

	// Status with a preset goes through WriteSlot with source "web".
	r = post(url.Values{"action": {"status"}, "value": {"Lunch"}, "expiry": {"30m"}})
	if r.code != http.StatusSeeOther {
		t.Fatalf("status: %d %s", r.code, r.body)
	}
	slots, _ := e.st.Slots(ctx, u.ID)
	if len(slots) != 1 || slots[0].Source != "web" || !slots[0].Until.Equal(e.clock.Now().Add(30*time.Minute)) {
		t.Errorf("slots = %+v", slots)
	}
	page := e.get("/account").body
	doc, _ := e.st.Document(ctx, u.ID, e.clock.Now())
	want := strings.ReplaceAll(format.Render(doc, true), "'", "&#39;")
	if !strings.Contains(page, `<pre class="doc">`+want+`</pre>`) {
		t.Errorf("account page does not show the served document:\n%s", want)
	}
	if r := post(url.Values{"action": {"status"}, "value": {"Later"}, "expiry": {"custom"}, "custom": {"2026-10-13T15:00"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("custom expiry in the past: %d", r.code)
	}
	if r := post(url.Values{"action": {"clear-status"}}); r.code != http.StatusSeeOther {
		t.Errorf("clear: %d", r.code)
	}
	if slots, _ := e.st.Slots(ctx, u.ID); len(slots) != 0 {
		t.Errorf("slots after clear = %+v", slots)
	}

	// Tokens: plaintext shown once, listed, revocable.
	r = post(url.Values{"action": {"token-create"}, "label": {"laptop"}})
	plain := regexp.MustCompile(`nf_[A-Za-z0-9_-]+`).FindString(r.body)
	if r.code != 200 || plain == "" {
		t.Fatalf("token create: %d", r.code)
	}
	if _, err := e.st.LookupToken(ctx, plain); err != nil {
		t.Fatalf("created token does not work: %v", err)
	}
	page = e.get("/account").body
	if strings.Contains(page, plain) || !strings.Contains(page, "laptop") {
		t.Error("token list wrong or plaintext shown again")
	}
	toks, _ := e.st.Tokens(ctx, u.ID)
	if r := post(url.Values{"action": {"token-revoke"}, "id": {itoa(toks[0].ID)}}); r.code != http.StatusSeeOther {
		t.Errorf("revoke: %d", r.code)
	}
	if _, err := e.st.LookupToken(ctx, plain); err == nil {
		t.Error("revoked token still works")
	}

	// Directory opt-in and out.
	post(url.Values{"action": {"directory"}, "directory": {"1"}})
	if !e.user("gwen").Directory {
		t.Error("directory not set")
	}
	post(url.Values{"action": {"directory"}})
	if e.user("gwen").Directory {
		t.Error("directory not cleared")
	}

	// Delete needs the confirmation step.
	r = post(url.Values{"action": {"delete"}})
	if r.code != 200 || !strings.Contains(r.body, "delete-confirm") {
		t.Fatalf("delete step 1: %d", r.code)
	}
	if r := post(url.Values{"action": {"delete-confirm"}, "confirm": {"wrong"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("delete with wrong confirm: %d", r.code)
	}
	if _, err := e.st.UserByName(ctx, "gwen"); err != nil {
		t.Fatal("account deleted without confirmation")
	}
	if r := post(url.Values{"action": {"delete-confirm"}, "confirm": {"gwen"}}); r.code != 200 {
		t.Errorf("delete confirm: %d", r.code)
	}
	if r := e.get("/gwen"); r.code != 404 {
		t.Errorf("/gwen after delete: %d", r.code)
	}
	if r := e.get("/account"); r.code != http.StatusSeeOther {
		t.Errorf("/account after delete: %d", r.code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestExpiry(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-13T20:30:00Z")
	cases := []struct {
		preset, custom, customUTC, tz, want string
	}{
		{"none", "", "", "", ""},
		{"30m", "", "", "", "2026-10-13T21:00:00Z"},
		{"1h", "", "", "", "2026-10-13T21:30:00Z"},
		{"eod", "", "", "", "2026-10-14T00:00:00Z"},
		{"eod", "", "", "-240", "2026-10-14T04:00:00Z"}, // 16:30 in UTC-4, midnight is 04:00Z
		{"eod", "", "", "600", "2026-10-14T14:00:00Z"},  // 06:30 on the 14th in UTC+10
		{"custom", "2026-10-13T22:00", "", "", "2026-10-13T22:00:00Z"},
		{"custom", "2026-10-13T22:00", "", "-240", "2026-10-14T02:00:00Z"},
		{"custom", "2026-10-13T22:00", "2026-10-14T01:00:00Z", "-240", "2026-10-14T01:00:00Z"},
	}
	for _, c := range cases {
		got, err := expiry(now, c.preset, c.custom, c.customUTC, c.tz)
		if err != nil {
			t.Errorf("%+v: %v", c, err)
			continue
		}
		if got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
	for _, bad := range []string{"custom", "forever"} {
		if _, err := expiry(now, bad, "", "", ""); err == nil {
			t.Errorf("expiry(%q) accepted", bad)
		}
	}
}

func TestAPIMounted(t *testing.T) {
	e := newEnv(t)
	if r := e.get("/api/v1/me"); r.code != http.StatusUnauthorized {
		t.Errorf("/api/v1/me without token: %d, want 401", r.code)
	}
}

// Message pages a signed-in user can land on still carry a working
// sign-out form.
func TestMessagePageSignOut(t *testing.T) {
	e := newEnv(t)
	e.signIn("hana")
	r := e.get("/nobody-here")
	m := csrfRE.FindStringSubmatch(r.body)
	if r.code != 404 || m == nil || m[1] != e.csrf("/account") {
		t.Fatalf("404 page sign-out csrf = %v", m)
	}
}
