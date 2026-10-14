package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mglass/nowfile/internal/sources"
)

// The env clock starts at 2026-10-13T16:00:00Z; this meeting runs to
// 17:00Z, which is 13:00 in New York and 18:00 in London.
const accountICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:1\r\nSUMMARY:SECRET Interview with Acme\r\nDTSTART:20261013T153000Z\r\nDTEND:20261013T170000Z\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestAccountSources(t *testing.T) {
	e := newEnv(t)
	e.signIn("ann")
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/locked.ics" {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, accountICS)
	}))
	defer feed.Close()
	act := func(action string, kv ...string) resp {
		t.Helper()
		f := url.Values{"csrf": {e.csrf("/account")}, "action": {action}}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return e.post("/account", f)
	}
	txt := func() string { t.Helper(); return e.get("/ann.txt").body }

	page := e.get("/account").body
	if !strings.Contains(page, "<h2>Sources</h2>") || !strings.Contains(page, "data-tz") || strings.Contains(page, "Switch on") {
		t.Fatalf("empty Sources section wrong:\n%s", page)
	}

	// The real fetcher will not reach loopback.
	if r := act("calendar-preview", "url", feed.URL+"/cal.ics"); r.code != 422 || !strings.Contains(r.body, "private network") {
		t.Errorf("loopback preview: %d", r.code)
	}
	e.site.fetch = sources.NewFetcher(true)

	if r := act("calendar-enable"); r.code != 422 || !strings.Contains(r.body, "Preview a calendar before") {
		t.Errorf("enable before preview: %d", r.code)
	}
	for in, want := range map[string]string{
		"https://mike:pw@cal.example/x.ics": "username or password",
		feed.URL + "/locked.ics":            "needs a password",
		"ftp://cal.example/x.ics":           "https:// or webcal://",
	} {
		r := act("calendar-preview", "url", in)
		if r.code != 422 || !strings.Contains(r.body, want) {
			t.Errorf("%s: %d, want message %q", in, r.code, want)
		}
		if !strings.Contains(r.body, `value="`+in+`"`) {
			t.Errorf("%s: typed URL not kept in the field", in)
		}
	}
	if _, err := e.st.Source(ctx, e.user("ann").ID, sources.KindCalendar); err == nil {
		t.Fatal("a failed preview saved a source")
	}

	// A good preview shows the exact line and sets the zone from the
	// browser, but publishes nothing.
	r := act("calendar-preview", "url", feed.URL+"/cal.ics", "tz", "America/New_York")
	if r.code != 200 || !strings.Contains(r.body, "Preview: <code>Calendar: Busy until 13:00</code>") || !strings.Contains(r.body, "Switch on") {
		t.Fatalf("preview: %d\n%s", r.code, r.body)
	}
	if tz, _ := e.st.TimeZone(ctx, e.user("ann").ID); tz != "America/New_York" {
		t.Errorf("tz = %q", tz)
	}
	if strings.Contains(txt(), "Calendar:") {
		t.Fatal("published before switched on")
	}

	// Switched on, it shows at once, as busy state only.
	if r := act("calendar-enable"); r.code != 200 || !strings.Contains(r.body, "Switch off") {
		t.Fatalf("enable: %d", r.code)
	}
	doc := txt()
	if !strings.Contains(doc, "Calendar: Busy until 13:00\n  set: ") || !strings.Contains(doc, "until: 2026-10-13T17:00:00Z") || !strings.Contains(doc, "source: calendar") {
		t.Errorf("document after enable:\n%s", doc)
	}
	for _, p := range []string{"/mike", "/ann.txt", "/mike.json", "/account"} {
		if strings.Contains(e.get(p).body, "SECRET") {
			t.Errorf("%s leaks the event subject", p)
		}
	}

	// Off clears it.
	act("calendar-disable")
	if strings.Contains(txt(), "Calendar:") {
		t.Error("still published after switching off")
	}

	// Time zone field: unknown names refused, a good one changes the clock.
	if r := act("timezone", "tz", "Mars/Olympus_Mons"); r.code != 422 || !strings.Contains(r.body, "Unknown time zone") {
		t.Errorf("bad zone: %d", r.code)
	}
	if r := act("timezone", "tz", "Europe/London"); r.code != 200 {
		t.Errorf("good zone: %d", r.code)
	}
	act("calendar-enable")
	if doc := txt(); !strings.Contains(doc, "Calendar: Busy until 18:00\n") {
		t.Errorf("after zone change:\n%s", doc)
	}
	if r := e.get("/account"); !strings.Contains(r.body, `value="Europe/London"`) || !strings.Contains(r.body, "Right now: <code>Calendar: Busy until 18:00</code>") {
		t.Errorf("account page after reload:\n%s", r.body)
	}

	// Editing the URL means previewing again, which switches it off until
	// the new one is switched on.
	if r := act("calendar-preview", "url", "webcal"+strings.TrimPrefix(feed.URL, "http")+"/other.ics"); r.code != 422 {
		// webcal:// becomes https://, which the plain-HTTP test server
		// cannot answer; the old source must be untouched.
		t.Errorf("webcal preview against plain HTTP: %d", r.code)
	}
	if !strings.Contains(txt(), "Calendar:") {
		t.Error("failed edit unpublished the calendar")
	}
	act("calendar-preview", "url", feed.URL+"/other.ics")
	if strings.Contains(txt(), "Calendar:") {
		t.Error("new URL published before being switched on")
	}

	// Remove takes the section back to empty.
	act("calendar-enable")
	if r := act("calendar-remove"); r.code != 200 || strings.Contains(r.body, "Switch on") || strings.Contains(r.body, "Switch off") {
		t.Errorf("remove: %d", r.code)
	}
	if strings.Contains(txt(), "Calendar:") {
		t.Error("still published after remove")
	}
}
