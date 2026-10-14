// Package sources runs the server-side writers a user switches on from the
// account page. Phase 2 has one, the calendar: it fetches an ICS feed,
// computes busy time with internal/ics, and writes the Calendar slot through
// store.WriteSlot as "Busy until 15:00". It never writes, stores, or logs an
// event's subject, and it never logs a feed URL, since many are secret links.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // users' zones must load even where the host has no zoneinfo

	"github.com/mglass/nowfile/internal/ics"
	"github.com/mglass/nowfile/internal/store"
)

// KindCalendar is the sources.kind of a calendar feed.
const KindCalendar = "calendar"

// SlotSource is the source token on slots the calendar writes.
const SlotSource = "calendar"

const (
	// FetchTimeout bounds one fetch, connection to last byte.
	FetchTimeout = 10 * time.Second
	// MaxBody is the largest calendar accepted.
	MaxBody = 1 << 20
	// FetchEvery is how often an enabled calendar is fetched.
	FetchEvery = 5 * time.Minute
	// FailuresBeforeError is how many fetches in a row must fail before the
	// account page shows an error.
	FailuresBeforeError = 3

	// Busy time is computed for this window around the fetch. A feed that
	// keeps answering 304 is fetched in full once the window's end is less
	// than refreshAhead away, so the cached spans never run out.
	lookBehind   = 2 * 24 * time.Hour
	lookAhead    = 8 * 24 * time.Hour
	refreshAhead = 7 * 24 * time.Hour
)

// Error is a failure with a message fit to show the user as is.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func userErr(f string, args ...any) error { return &Error{fmt.Sprintf(f, args...)} }

// Message returns the user-facing text for err: its own message if it is an
// *Error, and a generic line otherwise.
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return "Could not read that calendar."
}

// CalendarConfig is a calendar source's sources.config.
type CalendarConfig struct {
	URL string `json:"url"`
}

// CalendarState is a calendar source's sources.state. It holds the busy
// spans from the last good fetch, never the feed itself.
type CalendarState struct {
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"last_modified,omitempty"`
	Fetched      time.Time  `json:"fetched"`  // last fetch attempt
	From         time.Time  `json:"from"`     // window the spans cover
	To           time.Time  `json:"to"`       //
	Spans        []ics.Span `json:"spans"`    // merged busy spans
	Failures     int        `json:"failures"` // fetches failed in a row
}

// NormalizeURL checks a calendar URL a user pasted and returns the URL to
// fetch: webcal:// and webcals:// become https://. URLs with a username or
// password are refused, because nowfile does not do authenticated CalDAV.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", userErr("Paste your calendar's ICS link.")
	}
	if len(raw) > 2048 {
		return "", userErr("That URL is longer than 2048 characters.")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", userErr("That does not look like a URL.")
	}
	switch strings.ToLower(u.Scheme) {
	case "webcal", "webcals", "https":
		u.Scheme = "https"
	case "http":
		u.Scheme = "http"
	default:
		return "", userErr("Use an https:// or webcal:// calendar link.")
	}
	if u.User != nil {
		return "", userErr("That URL has a username or password in it. nowfile cannot read calendars that need a password; use the calendar's public or secret ICS link instead.")
	}
	if u.Hostname() == "" {
		return "", userErr("That URL has no host.")
	}
	u.Fragment = ""
	return u.String(), nil
}

// Fetcher fetches calendar feeds. It refuses addresses on loopback, private,
// link-local, and other non-public networks unless AllowPrivate was set at
// construction, which tests and self-hosters with a LAN calendar need.
type Fetcher struct {
	client *http.Client
}

var errPrivate = errors.New("non-public address")

// NewFetcher returns a Fetcher with the FetchTimeout and redirect limits.
func NewFetcher(allowPrivate bool) *Fetcher {
	dialer := &net.Dialer{Timeout: FetchTimeout}
	if !allowPrivate {
		// Checked on the resolved address at connect time, so DNS answers
		// and redirects cannot reach an internal address either.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errPrivate
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddr(ip.Unmap()) {
				return errPrivate
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 nil, // a proxy would dial for us, around the address check
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   FetchTimeout,
		ResponseHeaderTimeout: FetchTimeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Fetcher{client: &http.Client{
		Transport: tr,
		Timeout:   FetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.User != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
				return errors.New("bad redirect")
			}
			return nil
		},
	}}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func publicAddr(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip) &&
		!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// fetch GETs the feed with conditional headers. It returns a nil body and
// notModified on 304.
func (f *Fetcher) fetch(ctx context.Context, rawURL, etag, lastModified string) (body []byte, notModified bool, newETag, newLM string, err error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, "", "", userErr("That does not look like a URL.")
	}
	req.Header.Set("User-Agent", "nowfile calendar (+https://nowfile.org)")
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, errPrivate):
			return nil, false, "", "", userErr("That address is on a private network, which nowfile cannot reach.")
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			return nil, false, "", "", userErr("The calendar server did not answer within %d seconds.", int(FetchTimeout/time.Second))
		case isDNS(err):
			return nil, false, "", "", userErr("Could not find that host.")
		}
		return nil, false, "", "", userErr("Could not fetch that URL.")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, true, etag, lastModified, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, "", "", userErr("That calendar needs a password. nowfile can only read public or secret ICS links, not calendars that need signing in.")
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, false, "", "", userErr("That URL returned %d %s. Check the link.", resp.StatusCode, http.StatusText(resp.StatusCode))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, false, "", "", userErr("The calendar server answered %d %s.", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, "", "", userErr("The calendar server did not finish sending within %d seconds.", int(FetchTimeout/time.Second))
		}
		return nil, false, "", "", userErr("The connection dropped while reading the calendar.")
	}
	if len(body) > MaxBody {
		return nil, false, "", "", userErr("That calendar is larger than 1 MB.")
	}
	return body, false, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isDNS(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de)
}

// Refresh fetches the feed and returns the state to keep: new spans on 200,
// the previous spans on 304. A failed fetch returns prev with Fetched set
// and Failures counted, alongside the error. Warnings name events the parser
// skipped, by position only.
func (f *Fetcher) Refresh(ctx context.Context, rawURL string, prev CalendarState, loc *time.Location, now time.Time) (CalendarState, []string, error) {
	etag, lm := prev.ETag, prev.LastModified
	if prev.To.Before(now.Add(refreshAhead)) {
		etag, lm = "", "" // the cached window is running out; fetch in full
	}
	body, notModified, etag, lm, err := f.fetch(ctx, rawURL, etag, lm)
	if err != nil {
		prev.Fetched = now
		prev.Failures++
		return prev, nil, err
	}
	if notModified {
		prev.Fetched = now
		prev.Failures = 0
		return prev, nil, nil
	}
	from, to := now.Add(-lookBehind), now.Add(lookAhead)
	spans, warns, err := ics.Busy(body, loc, from, to)
	if err != nil {
		prev.Fetched = now
		prev.Failures++
		return prev, nil, userErr("That URL did not return a calendar. Use the ICS link, which usually ends in .ics.")
	}
	return CalendarState{ETag: etag, LastModified: lm, Fetched: now, From: from.UTC(), To: to.UTC(), Spans: spans}, warns, nil
}

// Line returns the Calendar slot value for now, and its until, or ok false
// when nothing is current. The line is busy state only.
func Line(spans []ics.Span, loc *time.Location, now time.Time) (value string, until time.Time, ok bool) {
	cur, ok := ics.Current(spans, now)
	if !ok {
		return "", time.Time{}, false
	}
	return "Busy until " + clockText(cur.End, loc, now), cur.End, true
}

// clockText formats t for a line read at now: "15:00" on the same local day,
// "Thu 15:00" within the week, "Oct 21 15:00" beyond, with " UTC" added
// when the user has not set a zone.
func clockText(t time.Time, loc *time.Location, now time.Time) string {
	lt, ln := t.In(loc), now.In(loc)
	var s string
	switch {
	case lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay():
		s = lt.Format("15:04")
	case lt.Sub(ln) < 6*24*time.Hour:
		s = lt.Format("Mon 15:04")
	default:
		s = lt.Format("Jan 2 15:04")
	}
	if loc.String() == "UTC" {
		s += " UTC"
	}
	return s
}

// Preview is what the account page shows for a calendar: the exact line it
// would publish now, or what it is waiting for.
type Preview struct {
	Line  string    // the slot value, empty when nothing is current
	Until time.Time // the slot's until when Line is set
	Text  string    // one sentence for the page
}

// Describe computes the preview from spans at now.
func Describe(spans []ics.Span, loc *time.Location, now time.Time) Preview {
	if line, until, ok := Line(spans, loc, now); ok {
		return Preview{Line: line, Until: until, Text: line}
	}
	if next, ok := ics.Next(spans, now); ok {
		return Preview{Text: "Nothing current, next busy at " + clockText(next.Start, loc, now) + "."}
	}
	return Preview{Text: "Nothing current, and nothing busy in the next week."}
}

// UserLocation returns the account's time zone, or UTC when it has none.
func UserLocation(ctx context.Context, st *store.Store, userID int64) (*time.Location, error) {
	tz, err := st.TimeZone(ctx, userID)
	if err != nil {
		return nil, err
	}
	if loc, err := store.LoadTimeZone(tz); err == nil {
		return loc, nil
	}
	return time.UTC, nil
}

// PreviewCalendar is the account page's Preview button: it checks and
// fetches rawURL, saves it as the account's calendar source with the spans
// it found (disabled if the URL is new), and returns the preview. Nothing is
// saved when it fails; the error is an *Error with a message for the user.
func PreviewCalendar(ctx context.Context, st *store.Store, f *Fetcher, userID int64, rawURL string, now time.Time) (Preview, store.Source, error) {
	u, err := NormalizeURL(rawURL)
	if err != nil {
		return Preview{}, store.Source{}, err
	}
	loc, err := UserLocation(ctx, st, userID)
	if err != nil {
		return Preview{}, store.Source{}, err
	}
	state, _, err := f.Refresh(ctx, u, CalendarState{}, loc, now)
	if err != nil {
		return Preview{}, store.Source{}, err
	}
	cfg, _ := json.Marshal(CalendarConfig{URL: u})
	stj, _ := json.Marshal(state)
	src, err := st.PutSource(ctx, userID, KindCalendar, string(cfg), string(stj))
	if err != nil {
		return Preview{}, store.Source{}, err
	}
	// A new URL leaves the source off; what the old one published goes too.
	if !src.Enabled {
		if err := Unpublish(ctx, st, userID); err != nil {
			return Preview{}, store.Source{}, err
		}
	}
	return Describe(state.Spans, loc, now), src, nil
}

// CalendarView is a calendar source decoded for the account page.
type CalendarView struct {
	URL     string
	Preview Preview
}

// ViewCalendar decodes src and computes its preview at now from the spans
// last fetched, without fetching.
func ViewCalendar(src store.Source, loc *time.Location, now time.Time) CalendarView {
	var cfg CalendarConfig
	var state CalendarState
	json.Unmarshal([]byte(src.Config), &cfg)
	json.Unmarshal([]byte(src.State), &state)
	return CalendarView{URL: cfg.URL, Preview: Describe(state.Spans, loc, now)}
}
