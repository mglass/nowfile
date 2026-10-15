package web

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/subs"
)

// rssDoc is the RSS 2.0 shape the feed test checks, read back with
// encoding/xml independently of the types that wrote it.
type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel []struct {
		Title []string `xml:"title"`
		// "link" matches atom:link too; the namespace tells them apart.
		Link []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
			Href    string `xml:"href,attr"`
			Rel     string `xml:"rel,attr"`
		} `xml:"link"`
		Description   []string `xml:"description"`
		LastBuildDate string   `xml:"lastBuildDate"`
		Items         []struct {
			Title   []string `xml:"title"`
			Link    string   `xml:"link"`
			PubDate string   `xml:"pubDate"`
			GUID    struct {
				IsPermaLink string `xml:"isPermaLink,attr"`
				Value       string `xml:",chardata"`
			} `xml:"guid"`
		} `xml:"item"`
	} `xml:"channel"`
}

func TestRSSFeed(t *testing.T) {
	e := newEnv(t)
	u, err := e.st.CreateUser(ctx, "alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	e.st.PutStatic(ctx, u.ID, store.Static{Name: "Alice & Co", Headline: "Makes <things>."})
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Status", Value: "Lunch, back at 1"})
	e.clock.Add(time.Minute)
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Focus", Value: "Work"}) // not in the feed
	until := e.clock.Now().Add(time.Hour).Format(time.RFC3339)
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Calendar", Value: "Busy until 17:00", Until: until, Source: "calendar"})
	e.clock.Add(time.Minute)
	e.st.ClearSlot(ctx, u.ID, "Status")

	r := e.get("/Alice.rss")
	if r.code != 200 {
		t.Fatalf("rss: %d %s", r.code, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := r.header.Get("Cache-Control"); cc != "public, max-age=60" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if !strings.HasPrefix(r.body, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("no XML declaration: %.60q", r.body)
	}
	var doc rssDoc
	if err := xml.Unmarshal([]byte(r.body), &doc); err != nil {
		t.Fatalf("parse: %v\n%s", err, r.body)
	}
	// RSS 2.0: version 2.0, exactly one channel with exactly one each of
	// title, link, and description; every item has a title or description.
	if doc.Version != "2.0" || len(doc.Channel) != 1 {
		t.Fatalf("version %q, %d channels", doc.Version, len(doc.Channel))
	}
	ch := doc.Channel[0]
	var links, self []string
	for _, l := range ch.Link {
		switch l.XMLName.Space {
		case "":
			links = append(links, l.Value)
		case "http://www.w3.org/2005/Atom":
			self = append(self, l.Rel+" "+l.Href)
		}
	}
	if len(ch.Title) != 1 || len(links) != 1 || len(ch.Description) != 1 {
		t.Fatalf("channel title/link/description counts: %d %d %d\n%s", len(ch.Title), len(links), len(ch.Description), r.body)
	}
	if ch.Title[0] != "Alice & Co (alice) on nowfile" || links[0] != e.srv.URL+"/alice" || ch.Description[0] != "Makes <things>." {
		t.Errorf("channel = %q %q %q", ch.Title, links, ch.Description)
	}
	if len(self) != 1 || self[0] != "self "+e.srv.URL+"/alice.rss" {
		t.Errorf("atom:link = %q", self)
	}
	want := []string{"Status cleared", "Calendar: Busy until 17:00", "Status: Lunch, back at 1"}
	if len(ch.Items) != len(want) {
		t.Fatalf("items = %d, want %d\n%s", len(ch.Items), len(want), r.body)
	}
	seen := map[string]bool{}
	for i, it := range ch.Items {
		if len(it.Title) != 1 || it.Title[0] != want[i] {
			t.Errorf("item %d title = %q, want %q", i, it.Title, want[i])
		}
		if _, err := time.Parse(time.RFC1123Z, it.PubDate); err != nil {
			t.Errorf("item %d pubDate %q: %v", i, it.PubDate, err)
		}
		if it.GUID.IsPermaLink != "false" || it.GUID.Value == "" || seen[it.GUID.Value] {
			t.Errorf("item %d guid = %+v", i, it.GUID)
		}
		seen[it.GUID.Value] = true
		if it.Link != e.srv.URL+"/alice" {
			t.Errorf("item %d link = %q", i, it.Link)
		}
	}
	if ch.Items[2].PubDate != "Tue, 13 Oct 2026 16:00:00 +0000" || ch.Items[2].GUID.Value != "127.0.0.1:alice:Status:2026-10-13T16:00:00Z" {
		t.Errorf("first change = %+v", ch.Items[2])
	}
	if ch.LastBuildDate != ch.Items[0].PubDate {
		t.Errorf("lastBuildDate %q, newest item %q", ch.LastBuildDate, ch.Items[0].PubDate)
	}

	if r := e.get("/nobody.rss"); r.code != 404 {
		t.Errorf("unknown user rss: %d", r.code)
	}
	if r := e.get("/alice"); !strings.Contains(r.body, `type="application/rss+xml"`) || !strings.Contains(r.body, `href="/alice.rss"`) {
		t.Errorf("user page lacks the feed link")
	}
}

func TestRSSLast50(t *testing.T) {
	e := newEnv(t)
	u, _ := e.st.CreateUser(ctx, "alice", "alice@example.com")
	for i := 0; i < 60; i++ {
		e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Status", Value: "n" + strings.Repeat("x", i)})
		e.clock.Add(time.Second)
	}
	var doc rssDoc
	if err := xml.Unmarshal([]byte(e.get("/alice.rss").body), &doc); err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Channel[0].Items); n != 50 {
		t.Fatalf("items = %d, want 50", n)
	}
}

func TestDirectoryPage(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"zed", "amy", "hidden"} {
		u, err := e.st.CreateUser(ctx, n, n+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if n != "hidden" {
			e.st.SetDirectory(ctx, u.ID, true)
		}
		e.st.PutStatic(ctx, u.ID, store.Static{Name: strings.ToUpper(n), Headline: n + " static"})
	}
	e.st.WriteSlot(ctx, e.user("zed").ID, store.SlotWrite{Key: "Status", Value: "Zed is out"})
	r := e.get("/directory")
	if r.code != 200 {
		t.Fatalf("directory: %d", r.code)
	}
	amy, zed := strings.Index(r.body, `href="/amy"`), strings.Index(r.body, `href="/zed"`)
	if amy < 0 || zed < 0 || amy > zed {
		t.Fatalf("not alphabetical or missing: amy %d zed %d", amy, zed)
	}
	if strings.Contains(r.body, "hidden") {
		t.Error("lists a user who did not opt in")
	}
	if !strings.Contains(r.body, "amy static") || !strings.Contains(r.body, "Zed is out") || strings.Contains(r.body, "zed static") {
		t.Errorf("headlines wrong:\n%s", r.body)
	}
}

var (
	confirmRE = regexp.MustCompile(`/subscribe/confirm/[A-Za-z0-9_-]+`)
	unsubRE   = regexp.MustCompile(`/unsubscribe/[A-Za-z0-9_-]+`)
)

func TestSubscribeConfirmUnsubscribe(t *testing.T) {
	e := newEnv(t)
	e.signIn("alice")
	reader := e.newClient() // signed out, no cookies

	page := e.do(reader, "GET", "/alice", nil)
	if !strings.Contains(page.body, `action="/subscribe/alice"`) {
		t.Fatal("no subscribe form on the user page")
	}
	if len(page.header.Values("Set-Cookie")) != 0 {
		t.Errorf("user page set a cookie on a reader: %q", page.header.Values("Set-Cookie"))
	}

	r := e.do(reader, "POST", "/subscribe/alice", url.Values{"email": {"reader@example.com"}})
	if r.code != 200 || !strings.Contains(r.body, "Check your email") {
		t.Fatalf("subscribe: %d %s", r.code, r.body)
	}
	m := e.mail.last(t)
	if m.To != "reader@example.com" {
		t.Fatalf("confirmation to %q", m.To)
	}
	confirm := confirmRE.FindString(m.Text)

	// Opening the link does not confirm, so a link scanner cannot.
	if r := e.do(reader, "GET", confirm, nil); r.code != 200 || !strings.Contains(r.body, `action="`+confirm+`"`) {
		t.Fatalf("confirm page: %d %s", r.code, r.body)
	}
	if n, _ := e.st.CountSubscribers(ctx, e.user("alice").ID); n != 0 {
		t.Fatal("GET confirmed the subscription")
	}
	if r := e.do(reader, "POST", confirm, url.Values{}); r.code != 200 || !strings.Contains(r.body, "Subscribed") {
		t.Fatalf("confirm: %d %s", r.code, r.body)
	}
	if r := e.do(reader, "POST", confirm, url.Values{}); r.code != 400 {
		t.Fatalf("second confirm: %d", r.code)
	}

	// The owner sees the count and nothing else.
	acct := e.get("/account")
	if !strings.Contains(acct.body, "1 person follows") || strings.Contains(acct.body, "reader@example.com") {
		t.Fatalf("account page subscriber section wrong")
	}

	// A Status change is in the next morning's digest, with the one-click
	// headers. Alice has no time zone, so that is 08:00 UTC.
	e.st.WriteSlot(ctx, e.user("alice").ID, store.SlotWrite{Key: "Status", Value: "Hello readers"})
	e.clock.Add(16 * time.Hour)
	if err := subs.New(e.st, e.mail, e.srv.URL).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	n := e.mail.last(t)
	if n.To != "reader@example.com" || !strings.Contains(n.Text, "Hello readers") {
		t.Fatalf("notification = %+v", n)
	}
	unsub := unsubRE.FindString(n.Text)
	if n.Headers["List-Unsubscribe"] != "<"+e.srv.URL+unsub+">" {
		t.Fatalf("List-Unsubscribe = %q, link %q", n.Headers["List-Unsubscribe"], unsub)
	}

	// GET shows a button; the provider's one-click POST, with no cookies
	// and no CSRF token, unsubscribes.
	if r := e.do(reader, "GET", unsub, nil); r.code != 200 || !strings.Contains(r.body, `action="`+unsub+`"`) {
		t.Fatalf("unsubscribe page: %d %s", r.code, r.body)
	}
	oneClick := e.newClient()
	if r := e.do(oneClick, "POST", unsub, url.Values{"List-Unsubscribe": {"One-Click"}}); r.code != 200 {
		t.Fatalf("one-click: %d %s", r.code, r.body)
	}
	if n, _ := e.st.CountSubscribers(ctx, e.user("alice").ID); n != 0 {
		t.Fatal("still subscribed")
	}
	if r := e.do(oneClick, "POST", unsub, url.Values{}); r.code != 200 {
		t.Fatalf("repeat unsubscribe: %d", r.code)
	}
}

func TestSubscribeErrors(t *testing.T) {
	e := newEnv(t)
	e.st.CreateUser(ctx, "alice", "alice@example.com")
	if r := e.post("/subscribe/nobody", url.Values{"email": {"a@example.com"}}); r.code != 404 {
		t.Errorf("unknown page: %d", r.code)
	}
	if r := e.post("/subscribe/alice", url.Values{"email": {"nope"}}); r.code != 422 {
		t.Errorf("bad address: %d", r.code)
	}
	var last int
	for i := 0; i < 6; i++ {
		last = e.post("/subscribe/alice", url.Values{"email": {"r" + strings.Repeat("x", i) + "@example.com"}}).code
	}
	// The bad-address attempt counted too, so the sixth is the seventh.
	if last != http.StatusTooManyRequests {
		t.Errorf("over the IP limit: %d", last)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.5:1234", "", "203.0.113.5"},
		{"203.0.113.5:1234", "198.51.100.1", "203.0.113.5"}, // not from a proxy: ignored
		{"127.0.0.1:1234", "198.51.100.1", "198.51.100.1"},
		{"172.17.0.1:1234", "10.0.0.1, 198.51.100.2", "198.51.100.2"}, // docker bridge, last hop wins
		{"127.0.0.1:1234", "garbage", "127.0.0.1"},
		{"[::1]:1234", "2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%s, %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}

func TestRSSSameSecondKeepsLast(t *testing.T) {
	e := newEnv(t)
	u, _ := e.st.CreateUser(ctx, "alice", "alice@example.com")
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Status", Value: "typo"})
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Status", Value: "fixed"})
	e.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Calendar", Value: "Busy until 17:00"})
	var doc rssDoc
	if err := xml.Unmarshal([]byte(e.get("/alice.rss").body), &doc); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range doc.Channel[0].Items {
		titles = append(titles, it.Title...)
	}
	if strings.Join(titles, "|") != "Calendar: Busy until 17:00|Status: fixed" {
		t.Fatalf("titles = %q", titles)
	}
}
