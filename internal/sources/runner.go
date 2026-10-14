package sources

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

// Tick is how often the runner wakes. Each tick republishes every enabled
// calendar from its cached spans, so a meeting shows within a minute of its
// start; feeds themselves are fetched only every FetchEvery.
const Tick = time.Minute

// parallel bounds concurrent fetches.
const parallel = 4

// Runner keeps every enabled calendar's slot current. It is safe to run one
// per database.
type Runner struct {
	st    *store.Store
	fetch *Fetcher
	// Now is the clock; it defaults to the store's.
	Now func() time.Time
	// Logf logs; it defaults to log.Printf.
	Logf func(format string, args ...any)
}

// NewRunner returns a runner over st that fetches with f.
func NewRunner(st *store.Store, f *Fetcher) *Runner {
	return &Runner{st: st, fetch: f, Now: st.Now, Logf: log.Printf}
}

// Run runs a pass now and then every Tick until ctx is cancelled, and
// returns nil then. With no enabled sources a pass is one query.
func (r *Runner) Run(ctx context.Context) error {
	t := time.NewTicker(Tick)
	defer t.Stop()
	for {
		r.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// RunOnce makes one pass over every enabled calendar.
func (r *Runner) RunOnce(ctx context.Context) {
	srcs, err := r.st.EnabledSources(ctx, KindCalendar)
	if err != nil {
		if ctx.Err() == nil {
			r.Logf("sources: list: %v", err)
		}
		return
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, src := range srcs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			r.runCalendar(ctx, src)
		}()
	}
	wg.Wait()
}

// runCalendar fetches src if it is due, records the outcome, and publishes
// the line. A failed fetch leaves the slot alone: the last line stays until
// its own until passes.
func (r *Runner) runCalendar(ctx context.Context, src store.Source) {
	var cfg CalendarConfig
	var state CalendarState
	if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil || cfg.URL == "" {
		r.Logf("sources: calendar %d: bad config", src.ID)
		return
	}
	json.Unmarshal([]byte(src.State), &state)
	name := host(cfg.URL)
	loc, err := UserLocation(ctx, r.st, src.UserID)
	if err != nil {
		r.Logf("sources: calendar %d: time zone: %v", src.ID, err)
		return
	}
	now := r.Now()

	var lastRun time.Time
	lastError := src.LastError
	if now.Sub(state.Fetched) >= FetchEvery {
		next, warns, err := r.fetch.Refresh(ctx, cfg.URL, state, loc, now)
		state, lastRun = next, now
		if err != nil {
			r.Logf("sources: calendar %d (%s): fetch failed (%d in a row): %s", src.ID, name, state.Failures, Message(err))
			if state.Failures >= FailuresBeforeError {
				lastError = Message(err)
			}
		} else {
			lastError = ""
			if len(warns) > 0 {
				r.Logf("sources: calendar %d (%s): skipped %d events: %v", src.ID, name, len(warns), warns)
			}
		}
	}
	stj, _ := json.Marshal(state)
	// The record applies only while the source is still enabled with this
	// URL, so a run that raced the user's edit publishes nothing.
	if err := r.st.RecordSourceRun(ctx, src, lastRun, lastError, string(stj)); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			r.Logf("sources: calendar %d: record: %v", src.ID, err)
		}
		return
	}
	if state.Failures > 0 {
		return
	}
	if err := publish(ctx, r.st, src.UserID, state, loc, now); err != nil {
		r.Logf("sources: calendar %d: publish: %v", src.ID, err)
	}
}

// publish writes or clears the Calendar slot to match state at now. It
// writes only when the line or its until changed, so a steady meeting is one
// write and one change in the history, not one a minute. It clears only a
// slot the calendar wrote, never one another writer set.
func publish(ctx context.Context, st *store.Store, userID int64, state CalendarState, loc *time.Location, now time.Time) error {
	slots, err := st.Slots(ctx, userID)
	if err != nil {
		return err
	}
	var cur *format.Slot
	for i := range slots {
		if slots[i].Key == "Calendar" {
			cur = &slots[i]
		}
	}
	line, until, ok := Line(state.Spans, loc, now)
	until = until.UTC().Truncate(time.Second)
	if !ok || !until.After(now) {
		if cur != nil && cur.Source == SlotSource {
			return st.ClearSlot(ctx, userID, "Calendar")
		}
		return nil
	}
	if cur != nil && cur.Source == SlotSource && cur.Value == line && cur.Until.Equal(until) {
		return nil
	}
	_, err = st.WriteSlot(ctx, userID, store.SlotWrite{Key: "Calendar", Value: line, Until: until.Format(time.RFC3339), Source: SlotSource})
	return err
}

// PublishNow publishes the account's calendar from its last fetched spans
// if it is enabled and healthy, without fetching. The account page calls it
// on Enable so the line appears at once rather than on the next tick.
func PublishNow(ctx context.Context, st *store.Store, userID int64, now time.Time) error {
	src, err := st.Source(ctx, userID, KindCalendar)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || !src.Enabled {
		return err
	}
	var state CalendarState
	json.Unmarshal([]byte(src.State), &state)
	if state.Failures > 0 {
		return nil
	}
	loc, err := UserLocation(ctx, st, userID)
	if err != nil {
		return err
	}
	return publish(ctx, st, userID, state, loc, now)
}

// Unpublish clears the Calendar slot if the calendar wrote it. The account
// page calls it on Disable and Remove.
func Unpublish(ctx context.Context, st *store.Store, userID int64) error {
	slots, err := st.Slots(ctx, userID)
	if err != nil {
		return err
	}
	for _, sl := range slots {
		if sl.Key == "Calendar" && sl.Source == SlotSource {
			return st.ClearSlot(ctx, userID, "Calendar")
		}
	}
	return nil
}

// host is the part of a feed URL that is safe to log.
func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	return u.Hostname()
}
