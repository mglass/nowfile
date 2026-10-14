package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/sources"
	"github.com/mglass/nowfile/internal/store"
)

// sourcesData is the Sources section of the account page.
type sourcesData struct {
	TimeZone string
	Calendar *calendarView // nil until the user previews a calendar
	URL      string        // the URL field: the saved URL, or what was typed before a failed preview
	Fresh    bool          // the preview was fetched just now, in this response
}

type calendarView struct {
	URL       string
	Enabled   bool
	LastRun   time.Time
	LastError string
	Preview   sources.Preview
}

// loadSources fills d.Sources from the store. A URL already in d (after a
// failed preview) is kept in the field.
func (s *Server) loadSources(r *http.Request, u *store.User, d *accountData) error {
	ctx := r.Context()
	tz, err := s.st.TimeZone(ctx, u.ID)
	if err != nil {
		return err
	}
	d.Sources.TimeZone = tz
	src, err := s.st.Source(ctx, u.ID, sources.KindCalendar)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	loc, err := sources.UserLocation(ctx, s.st, u.ID)
	if err != nil {
		return err
	}
	v := sources.ViewCalendar(src, loc, s.st.Now())
	d.Sources.Calendar = &calendarView{URL: v.URL, Enabled: src.Enabled, LastRun: src.LastRun, LastError: src.LastError, Preview: v.Preview}
	if d.Sources.URL == "" {
		d.Sources.URL = v.URL
	}
	return nil
}

// sourcesPost handles the Sources section's actions. It reports false for
// an action it does not own.
func (s *Server) sourcesPost(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	ctx := r.Context()
	f := r.PostForm
	done := func(notice string) {
		s.renderAccount(w, r, u, http.StatusOK, page{Notice: notice}, accountData{})
	}
	fail := func(err error) {
		s.serverError(w, r, err)
	}

	switch f.Get("action") {
	case "calendar-preview":
		// The browser's zone rides along with the first preview, so a
		// web-only user never has to set it by hand.
		if tz := strings.TrimSpace(f.Get("tz")); tz != "" {
			if cur, err := s.st.TimeZone(ctx, u.ID); err == nil && cur == "" {
				s.st.SetTimeZone(ctx, u.ID, tz) // an unknown zone is ignored here
			}
		}
		raw := f.Get("url")
		_, _, err := sources.PreviewCalendar(ctx, s.st, s.fetch, u.ID, raw, s.st.Now())
		if err != nil {
			var ue *sources.Error
			if !errors.As(err, &ue) {
				fail(err)
				return true
			}
			d := accountData{}
			d.Sources.URL = raw
			s.renderAccount(w, r, u, http.StatusUnprocessableEntity, page{Error: ue.Msg}, d)
			return true
		}
		d := accountData{}
		d.Sources.Fresh = true
		s.renderAccount(w, r, u, http.StatusOK, page{}, d)

	case "calendar-enable":
		if err := s.st.SetSourceEnabled(ctx, u.ID, sources.KindCalendar, true); errors.Is(err, store.ErrNotFound) {
			s.renderAccount(w, r, u, http.StatusUnprocessableEntity, page{Error: "Preview a calendar before switching it on."}, accountData{})
			return true
		} else if err != nil {
			fail(err)
			return true
		}
		if err := sources.PublishNow(ctx, s.st, u.ID, s.st.Now()); err != nil {
			fail(err)
			return true
		}
		done("Calendar on. Your page shows busy time from it now.")

	case "calendar-disable", "calendar-remove":
		var err error
		if f.Get("action") == "calendar-disable" {
			err = s.st.SetSourceEnabled(ctx, u.ID, sources.KindCalendar, false)
		} else {
			err = s.st.DeleteSource(ctx, u.ID, sources.KindCalendar)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			fail(err)
			return true
		}
		if err := sources.Unpublish(ctx, s.st, u.ID); err != nil {
			fail(err)
			return true
		}
		if f.Get("action") == "calendar-disable" {
			done("Calendar off. Nothing from it shows on your page.")
		} else {
			done("Calendar removed.")
		}

	case "timezone":
		tz := strings.TrimSpace(f.Get("tz"))
		if err := s.st.SetTimeZone(ctx, u.ID, tz); errors.Is(err, store.ErrInvalidTimeZone) {
			s.renderAccount(w, r, u, http.StatusUnprocessableEntity, page{Error: "Unknown time zone. Use a name like America/New_York or Europe/London."}, accountData{})
			return true
		} else if err != nil {
			fail(err)
			return true
		}
		// The line's clock time depends on the zone.
		if err := sources.PublishNow(ctx, s.st, u.ID, s.st.Now()); err != nil {
			fail(err)
			return true
		}
		done("Time zone saved.")

	default:
		return false
	}
	return true
}
