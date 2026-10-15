// Package subs runs email subscriptions: a reader asks to follow a page,
// confirms with a one-use link, and then gets one digest a day at
// DigestHour in the page owner's time zone listing the previous day's
// changes, or nothing on a day without any. Every digest carries a
// one-click unsubscribe link and List-Unsubscribe headers. The store owns
// the tables; this package owns the flow, the mail text, the rate limits,
// and the notifier loop.
package subs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/store"
)

// Keys are the slots whose changes readers are told about, by RSS and by
// email. Focus and Listening move too often to mail anyone about.
var Keys = []string{"Status", "Calendar"}

const (
	// DigestHour is the local hour, in the page owner's time zone, at
	// which the day's digest goes out.
	DigestHour = 8
	// ConfirmTTL is how long a confirmation link works.
	ConfirmTTL = 24 * time.Hour
	// Poll is how often the notifier looks for due digests.
	Poll = 30 * time.Second
	// PruneEvery is how often old changes and expired pending
	// subscriptions are deleted, besides once at start.
	PruneEvery = 24 * time.Hour
)

// Rate limits on the subscribe form, so it cannot be used to mail a third
// party over and over. Both count every attempt, including ones that send
// nothing. An address gets at most three confirmation mails a day however
// many pages and IPs ask.
const (
	PerIP         = 5
	PerIPWindow   = time.Hour
	PerAddr       = 3
	PerAddrWindow = 24 * time.Hour
)

// ErrRateLimited means the requesting IP has used up its subscribe
// attempts for now.
var ErrRateLimited = errors.New("too many subscribe attempts")

// Service runs subscriptions against a store and a mail sender.
type Service struct {
	st      *store.Store
	mail    mail.Sender
	baseURL string
	byIP    *limiter
	byAddr  *limiter
}

// New returns a Service. baseURL is the public origin without a trailing
// slash, used to build links in mail.
func New(st *store.Store, m mail.Sender, baseURL string) *Service {
	return &Service{
		st:      st,
		mail:    m,
		baseURL: baseURL,
		byIP:    newLimiter(PerIP, PerIPWindow),
		byAddr:  newLimiter(PerAddr, PerAddrWindow),
	}
}

// Subscribe asks to subscribe email to u's page on behalf of a request
// from ip. Unless a limit is hit it mails a confirmation link that works
// once within ConfirmTTL. It returns ErrRateLimited if ip is over its
// limit and an error wrapping store.ErrInvalidEmail for a bad address.
// Every other outcome, including an address over its own limit or already
// subscribed, returns nil and the caller shows the same "check your email"
// answer, so the form does not reveal who follows whom.
func (s *Service) Subscribe(ctx context.Context, u store.User, email, ip string) error {
	now := s.st.Now()
	if !s.byIP.allow(ip, now) {
		return ErrRateLimited
	}
	norm := normalize(email)
	if !s.byAddr.allow(norm, now) {
		log.Printf("subs: confirmation for %s held back by the per-address limit", u.Name)
		return nil
	}
	token, err := secret()
	if err != nil {
		return err
	}
	unsub, err := secret()
	if err != nil {
		return err
	}
	err = s.st.PendSubscriber(ctx, u.ID, norm, store.HashToken(token), now.Add(ConfirmTTL), unsub)
	if errors.Is(err, store.ErrAlreadySubscribed) {
		return nil
	}
	if err != nil {
		return err
	}
	link := s.baseURL + "/subscribe/confirm/" + token
	return s.mail.Send(ctx, mail.Message{
		To:      norm,
		Subject: "Confirm your subscription to " + u.Name + " on nowfile",
		Text: fmt.Sprintf("Someone, hopefully you, asked to get an email when %s's nowfile changes:\n\n%s\n\n"+
			"To confirm, open this link and press Confirm:\n\n%s\n\n"+
			"The link works once and expires in 24 hours. If you did not ask for this, ignore this email and nothing more will come.\n",
			u.Name, s.pageURL(u.Name), link),
	})
}

// Confirm confirms the subscription whose confirmation secret is token. It
// returns store.ErrLinkInvalid for an unknown, used, or expired link.
func (s *Service) Confirm(ctx context.Context, token string) (store.Subscriber, error) {
	return s.st.ConfirmSubscriber(ctx, token)
}

func (s *Service) pageURL(name string) string { return s.baseURL + "/" + name }

// UnsubscribeURL is the link in every notification to sub.
func (s *Service) UnsubscribeURL(sub store.Subscriber) string {
	return s.baseURL + "/unsubscribe/" + sub.Unsub
}

// Run prunes, then sends due digests every Poll and prunes again
// every PruneEvery, until ctx is cancelled. It returns nil on shutdown;
// errors from a single pass are logged, not returned.
func (s *Service) Run(ctx context.Context) error {
	s.Prune(ctx)
	poll := time.NewTicker(Poll)
	defer poll.Stop()
	prune := time.NewTicker(PruneEvery)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				log.Printf("subs: notify: %v", err)
			}
		case <-prune.C:
			s.Prune(ctx)
		}
	}
}

// Prune deletes changes older than store.ChangeRetention and pending
// subscriptions whose link has expired, and logs what it did.
func (s *Service) Prune(ctx context.Context) {
	nc, err := s.st.PruneChanges(ctx)
	if err != nil {
		log.Printf("subs: prune changes: %v", err)
	}
	ns, err := s.st.PruneSubscribers(ctx)
	if err != nil {
		log.Printf("subs: prune subscribers: %v", err)
	}
	if nc > 0 || ns > 0 {
		log.Printf("subs: pruned %d changes, %d expired pending subscriptions", nc, ns)
	}
}

// Tick sends every digest that is due. A page's digest is due once its
// owner's clock has passed DigestHour today; it lists the page's Status and
// Calendar changes from DigestHour yesterday to DigestHour today, oldest
// first, under the headline now. Changes after DigestHour wait for
// tomorrow's. A subscriber is marked done for the day whether a mail went
// out or there were no changes, so a restart never sends a day twice, and
// is never mailed a change from before they confirmed. A failed send is
// not marked and is retried next tick.
func (s *Service) Tick(ctx context.Context) error {
	now := s.st.Now()
	pages, err := s.st.DigestPages(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range pages {
		if err := s.digestPage(ctx, p, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) digestPage(ctx context.Context, p store.DigestPage, now time.Time) error {
	loc, err := store.LoadTimeZone(p.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	from, at := DigestWindow(now, loc)
	due, err := s.st.DigestDue(ctx, p.UserID, at)
	if err != nil || len(due) == 0 {
		return err
	}
	changes, err := s.st.ChangesBetween(ctx, p.UserID, Keys, from, at)
	if err != nil {
		return err
	}
	var u store.User
	var doc format.Document
	if len(changes) > 0 {
		if u, err = s.st.UserByID(ctx, p.UserID); err != nil {
			// Deleted or suspended since the query; the next one leaves
			// them out.
			return nil
		}
		if doc, err = s.st.Document(ctx, p.UserID, now); err != nil {
			return nil
		}
	}
	var errs []error
	for _, sub := range due {
		// Changes from before the subscriber confirmed are not theirs.
		var theirs []store.Change
		var last int64
		for _, c := range changes {
			if c.ID > sub.LastChangeID {
				theirs = append(theirs, c)
				last = max(last, c.ID)
			}
		}
		if len(theirs) > 0 {
			if err := s.mail.Send(ctx, s.digest(u, doc, theirs, from, at, loc, sub)); err != nil {
				errs = append(errs, fmt.Errorf("mail subscriber %d: %w", sub.ID, err))
				continue
			}
		}
		if err := s.st.MarkDigested(ctx, sub.ID, at, last); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DigestWindow returns the latest digest time at or before now in loc,
// DigestHour local, and the one before it. The window is local day to
// local day, so it is 23 or 25 hours across a daylight saving change.
func DigestWindow(now time.Time, loc *time.Location) (from, at time.Time) {
	l := now.In(loc)
	y, m, d := l.Date()
	at = time.Date(y, m, d, DigestHour, 0, 0, 0, loc)
	if now.Before(at) {
		d--
		at = time.Date(y, m, d, DigestHour, 0, 0, 0, loc)
	}
	return time.Date(y, m, d-1, DigestHour, 0, 0, 0, loc), at
}

// digestTime is how times read in a digest: the window spans two days.
const digestTime = "Mon 2 Jan 15:04"

func (s *Service) digest(u store.User, doc format.Document, changes []store.Change, from, at time.Time, loc *time.Location, sub store.Subscriber) mail.Message {
	who := u.Name
	if doc.Name != "" {
		who = doc.Name + " (" + u.Name + ")"
	}
	line := format.Headline(doc)
	if line == "" {
		line = "Nothing showing right now."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s now:\n\n  %s\n\n", who, line)
	fmt.Fprintf(&b, "Changes from %s to %s (%s):\n\n", from.In(loc).Format(digestTime), at.In(loc).Format(digestTime), loc)
	for _, c := range changes {
		v := c.Value
		if v == "" {
			v = "cleared"
		}
		fmt.Fprintf(&b, "  %s  %s: %s\n", c.Set.In(loc).Format(digestTime), c.Key, v)
	}
	unsub := s.UnsubscribeURL(sub)
	fmt.Fprintf(&b, "\n%s\n\n"+
		"You get one email a day from this page, at %02d:00 its owner's time, and none on a day without changes.\n"+
		"Unsubscribe with one click: %s\n", s.pageURL(u.Name), DigestHour, unsub)
	return mail.Message{
		To:      sub.Email,
		Subject: u.Name + ": " + truncate(line, 120),
		Text:    b.String(),
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsub + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
}

// truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// normalize is the limiter key for an address. The store validates it.
func normalize(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// secret returns 32 random bytes, URL-safe base64.
func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// limiter allows at most n events per key in any window.
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, hits: map[string][]time.Time{}}
}

// allow records an event for key at now and reports whether it is within
// the limit. Refused events are not recorded.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	if len(l.hits) > 10000 {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	ts := l.hits[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.n {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}
