package web

import (
	"encoding/xml"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/subs"
)

// rssLimit is how many changes a feed carries.
const rssLimit = 50

// rss is an RSS 2.0 document.
type rss struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Self          rssSelf   `xml:"atom:link"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	TTL           int       `xml:"ttl"`
	Items         []rssItem `xml:"item"`
}

type rssSelf struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title   string  `xml:"title"`
	Link    string  `xml:"link"`
	PubDate string  `xml:"pubDate"`
	GUID    rssGUID `xml:"guid"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// feed serves GET /{name}.rss: one item per Status or Calendar change,
// newest first, the last rssLimit of them. The title is the line as it was
// set, or "Status cleared" for a clear, since one history row cannot say
// what the headline fell back to. The guid is built from user, key, and set
// time; set times are to the second, so of several changes to one key in
// one second only the last, the one readers saw, becomes an item.
func (s *Server) feed(w http.ResponseWriter, r *http.Request, name string, doc format.Document) {
	u, err := s.st.UserByName(r.Context(), name)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	changes, err := s.st.Changes(r.Context(), u.ID, subs.Keys, rssLimit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pageURL := s.cfg.BaseURL + "/" + name
	title := name
	if doc.Name != "" {
		title = doc.Name + " (" + name + ")"
	}
	desc := doc.Headline
	if desc == "" {
		desc = "Status and calendar changes for " + name + " on nowfile."
	}
	f := rss{Version: "2.0", Atom: "http://www.w3.org/2005/Atom", Channel: rssChannel{
		Title:       title + " on nowfile",
		Link:        pageURL,
		Description: desc,
		Self:        rssSelf{Href: pageURL + ".rss", Rel: "self", Type: "application/rss+xml"},
		TTL:         1,
	}}
	seen := map[string]bool{}
	for _, c := range changes {
		set := c.Set.UTC().Format(time.RFC3339)
		guid := s.fingerHost() + ":" + name + ":" + c.Key + ":" + set
		if seen[guid] {
			continue // newest first, so the one kept is the last write
		}
		seen[guid] = true
		t := c.Key + ": " + c.Value
		if c.Value == "" {
			t = c.Key + " cleared"
		}
		f.Channel.Items = append(f.Channel.Items, rssItem{
			Title:   t,
			Link:    pageURL,
			PubDate: c.Set.UTC().Format(time.RFC1123Z),
			GUID:    rssGUID{IsPermaLink: "false", Value: guid},
		})
	}
	if len(changes) > 0 {
		f.Channel.LastBuildDate = changes[0].Set.UTC().Format(time.RFC1123Z)
	}
	out, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Write([]byte(xml.Header))
	w.Write(out)
	w.Write([]byte("\n"))
}

// directoryEntry is one row of /directory.
type directoryEntry struct {
	Username, Name, Current string
}

// directory serves GET /directory: accounts that opted in, alphabetical,
// with the one line format.Headline picks.
func (s *Server) directory(w http.ResponseWriter, r *http.Request) {
	names, err := s.st.DirectoryNames(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.st.Now()
	var rows []directoryEntry
	for _, n := range names {
		doc, err := s.st.DocumentByName(r.Context(), n, now)
		if err != nil {
			continue // deleted or suspended since the list was read
		}
		rows = append(rows, directoryEntry{Username: n, Name: doc.Name, Current: format.Headline(doc)})
	}
	u, _ := s.currentUser(r)
	p := page{Title: "Directory", User: u, Data: rows}
	if u != nil {
		p.CSRF = s.csrfToken(w, r)
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.render(w, http.StatusOK, "directory", p)
}

// subsForm parses a small form for the subscription routes. They take no
// CSRF token: they act on no one's session, so a forged post can do no
// more than a direct one, and a mail provider's one-click unsubscribe
// carries no cookie. Public pages therefore set no cookie on readers.
func subsForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	return true
}

// subscribe serves POST /subscribe/{name} from the form on a user's page.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	if !subsForm(w, r) {
		return
	}
	name := strings.ToLower(r.PathValue("name"))
	u, err := s.st.UserByName(r.Context(), name)
	if err != nil || u.Suspended {
		s.notFound(w, r, "html")
		return
	}
	err = s.subs.Subscribe(r.Context(), u, r.PostForm.Get("email"), clientIP(r))
	switch {
	case errors.Is(err, subs.ErrRateLimited):
		s.message(w, r, http.StatusTooManyRequests, "Slow down", "Too many subscribe requests from here. Try again in an hour.")
		return
	case errors.Is(err, store.ErrInvalidEmail):
		s.message(w, r, http.StatusUnprocessableEntity, "Check the address", "That email address does not look right. Go back and try again.")
		return
	case err != nil:
		log.Printf("web: subscribe to %s: %v", name, err)
	}
	s.message(w, r, http.StatusOK, "Check your email",
		"If that address can be subscribed, a confirmation link is on its way. Nothing is sent about "+name+" until you open it and press Confirm. It expires in 24 hours.")
}

// subAction is the data for the confirm and unsubscribe pages: a heading,
// a sentence, and one button that posts back to the same URL.
type subAction struct {
	Heading, Text, Button, Action string
}

// confirmPage serves GET /subscribe/confirm/{token}. The link only shows a
// button, so a mail scanner that opens links cannot confirm on the
// reader's behalf, which would defeat double opt-in.
func (s *Server) confirmPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, http.StatusOK, "subaction", page{Title: "Confirm subscription", Data: subAction{
		Heading: "Confirm subscription",
		Text:    "Get an email when this nowfile changes, at most one every 15 minutes. Every email has a one-click unsubscribe link.",
		Button:  "Confirm",
		Action:  r.URL.Path,
	}})
}

// confirm serves POST /subscribe/confirm/{token}.
func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !subsForm(w, r) {
		return
	}
	sub, err := s.subs.Confirm(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrLinkInvalid) {
		s.message(w, r, http.StatusBadRequest, "Link expired", "That confirmation link is invalid, used, or expired. Subscribe again from the page.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := "this page"
	if u, err := s.st.UserByID(r.Context(), sub.UserID); err == nil {
		name = u.Name + "'s nowfile"
	}
	s.message(w, r, http.StatusOK, "Subscribed", "You will get an email when "+name+" changes. Each one has a link to unsubscribe.")
}

// unsubscribePage serves GET /unsubscribe/{token}: one button, so
// a link scanner cannot unsubscribe anyone (RFC 8058 asks the same of the
// List-Unsubscribe URL).
func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	sub, err := s.st.SubscriberByUnsub(r.Context(), r.PathValue("token"))
	if err != nil {
		s.message(w, r, http.StatusOK, "Unsubscribed", "That address is not subscribed. Nothing more will be sent.")
		return
	}
	name := "this page"
	if u, err := s.st.UserByID(r.Context(), sub.UserID); err == nil {
		name = u.Name + "'s nowfile"
	}
	s.render(w, http.StatusOK, "subaction", page{Title: "Unsubscribe", Data: subAction{
		Heading: "Unsubscribe",
		Text:    "Stop email about " + name + ".",
		Button:  "Unsubscribe",
		Action:  r.URL.Path,
	}})
}

// unsubscribe serves POST /unsubscribe/{token}, from the button and
// from a mail provider's List-Unsubscribe-Post one-click request alike.
// It succeeds whether or not the subscription still exists.
func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !subsForm(w, r) {
		return
	}
	if err := s.st.Unsubscribe(r.Context(), r.PathValue("token")); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}
	s.message(w, r, http.StatusOK, "Unsubscribed", "Done. Nothing more will be sent to that address about this page.")
}

// clientIP is the address the per-IP subscribe limit counts. Behind a
// local reverse proxy (Caddy on the public server, which sets
// X-Forwarded-For to the client it saw) it is the last X-Forwarded-For
// entry; a request straight from the internet cannot set it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if addr.IsLoopback() || addr.IsPrivate() {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if fwd, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return fwd.String()
			}
		}
	}
	return addr.String()
}
