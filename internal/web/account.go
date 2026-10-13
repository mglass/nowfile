package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

// webSource is the slot source recorded for writes from the account page.
const webSource = "web"

// accountData is the data for the account page.
type accountData struct {
	URL      string
	Static   store.Static
	Links    []string // Static.Links padded to format.MaxLinks inputs
	Status   *format.Slot
	Document string // the long form, exactly as /{name}.txt serves it
	Tokens   []store.Token
	NewToken string // plaintext of a just-created token, shown once
	Form     statusForm
}

// statusForm keeps the status inputs across a failed submit.
type statusForm struct {
	Value, Expiry, Custom string
}

// requireUser returns the signed-in account or redirects to /login.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) *store.User {
	u, _ := s.currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
	return u
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	u := s.requireUser(w, r)
	if u == nil {
		return
	}
	var notice string
	if r.URL.Query().Get("saved") != "" {
		notice = "Saved."
	}
	s.renderAccount(w, r, u, http.StatusOK, page{Notice: notice}, accountData{})
}

// renderAccount fills in the account page from the store. Fields already set
// in d (NewToken, Form, and Static after a failed save) are kept.
func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, u *store.User, status int, p page, d accountData) {
	ctx := r.Context()
	doc, err := s.st.Document(ctx, u.ID, s.st.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Static.Links == nil {
		if d.Static, err = s.st.GetStatic(ctx, u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if d.Tokens, err = s.st.Tokens(ctx, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	for i := range doc.Slots {
		if doc.Slots[i].Key == "Status" {
			d.Status = &doc.Slots[i]
		}
	}
	d.URL = s.cfg.BaseURL + "/" + u.Name
	d.Document = format.Render(doc, true)
	d.Links = make([]string, format.MaxLinks)
	copy(d.Links, d.Static.Links)
	if d.Form.Expiry == "" {
		d.Form.Expiry = "none"
	}
	p.Title = "Account"
	p.User = u
	p.CSRF = s.csrfToken(w, r)
	p.Data = d
	s.render(w, status, "account", p)
}

func (s *Server) accountPost(w http.ResponseWriter, r *http.Request) {
	u := s.requireUser(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	f := r.PostForm
	saved := func() { http.Redirect(w, r, "/account?saved=1", http.StatusSeeOther) }
	invalid := func(err error, d accountData) {
		msg := err.Error()
		if ve := (*store.ValidationError)(nil); errors.As(err, &ve) {
			msg = ve.Message
		} else if errors.Is(err, format.ErrInvalid) {
			msg = strings.TrimPrefix(msg, format.ErrInvalid.Error()+": ")
		}
		s.renderAccount(w, r, u, http.StatusUnprocessableEntity, page{Error: msg}, d)
	}

	switch f.Get("action") {
	case "profile":
		st := store.Static{
			Name:     strings.TrimSpace(f.Get("name")),
			Headline: strings.TrimSpace(f.Get("headline")),
			Photo:    strings.TrimSpace(f.Get("photo")),
			Links:    []string{},
			Plan:     f.Get("plan"),
		}
		for _, l := range f["link"] {
			if l = strings.TrimSpace(l); l != "" {
				st.Links = append(st.Links, l)
			}
		}
		for _, v := range append([]string{st.Photo}, st.Links...) {
			if v != "" && !webURL(v) {
				invalid(errors.New("Photo and links must be full http:// or https:// URLs."), accountData{Static: st})
				return
			}
		}
		if err := s.st.PutStatic(ctx, u.ID, st); err != nil {
			if errors.Is(err, format.ErrInvalid) {
				invalid(err, accountData{Static: st})
				return
			}
			s.serverError(w, r, err)
			return
		}
		saved()

	case "status":
		form := statusForm{Value: f.Get("value"), Expiry: f.Get("expiry"), Custom: f.Get("custom")}
		until, err := expiry(s.st.Now(), form.Expiry, form.Custom, f.Get("custom_utc"), f.Get("tz"))
		if err != nil {
			invalid(err, accountData{Form: form})
			return
		}
		// WriteSlot owns every rule; the write API calls it too.
		if _, err := s.st.WriteSlot(ctx, u.ID, store.SlotWrite{Key: "Status", Value: form.Value, Until: until, Source: webSource}); err != nil {
			if errors.Is(err, format.ErrInvalid) {
				invalid(err, accountData{Form: form})
				return
			}
			s.serverError(w, r, err)
			return
		}
		saved()

	case "clear-status":
		if err := s.st.ClearSlot(ctx, u.ID, "Status"); err != nil {
			s.serverError(w, r, err)
			return
		}
		saved()

	case "directory":
		if err := s.st.SetDirectory(ctx, u.ID, f.Get("directory") == "1"); err != nil {
			s.serverError(w, r, err)
			return
		}
		saved()

	case "token-create":
		plain, _, err := s.st.CreateToken(ctx, u.ID, f.Get("label"))
		if errors.Is(err, format.ErrInvalid) {
			invalid(err, accountData{})
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderAccount(w, r, u, http.StatusOK, page{Notice: "Token created. Copy it now; it is not shown again."}, accountData{NewToken: plain})

	case "token-revoke":
		id, err := strconv.ParseInt(f.Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad token id", http.StatusBadRequest)
			return
		}
		if err := s.st.RevokeToken(ctx, u.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
		saved()

	case "delete":
		s.render(w, http.StatusOK, "delete", page{Title: "Delete account", User: u, CSRF: s.csrfToken(w, r)})

	case "delete-confirm":
		if strings.ToLower(strings.TrimSpace(f.Get("confirm"))) != u.Name {
			s.render(w, http.StatusUnprocessableEntity, "delete", page{Title: "Delete account", User: u, CSRF: s.csrfToken(w, r),
				Error: "Type your username exactly to confirm."})
			return
		}
		if err := s.st.DeleteUser(ctx, u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		if _, sess := s.currentUser(r); sess != "" {
			s.st.DeleteSession(ctx, sess)
		}
		s.setCookie(w, sessionCookie, "", -1)
		s.render(w, http.StatusOK, "message", page{Title: "Account deleted",
			Notice: "Your account and page are gone. The name " + u.Name + " stays held for 90 days before anyone else can take it."})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// webURL reports whether v is an absolute http or https URL with a host.
func webURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// expiry turns the status form's expiry choice into the RFC 3339 until
// string WriteSlot takes; empty means no expiry. tz is the browser's offset
// from UTC in minutes (east positive), set by account.js; without it the
// server uses UTC. customUTC is the custom time already converted to
// RFC 3339 by account.js, which gets DST right; it is passed through as is
// so WriteSlot alone judges it. custom is the raw datetime-local value used
// when script did not run. The only errors are form errors: no custom time,
// or an unknown choice.
func expiry(now time.Time, preset, custom, customUTC, tz string) (string, error) {
	off, _ := strconv.Atoi(tz)
	if off < -14*60 || off > 14*60 {
		off = 0
	}
	loc := time.FixedZone("", off*60)
	rfc := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	switch preset {
	case "", "none":
		return "", nil
	case "30m":
		return rfc(now.Add(30 * time.Minute)), nil
	case "1h":
		return rfc(now.Add(time.Hour)), nil
	case "eod":
		t := now.In(loc)
		return rfc(time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)), nil
	case "custom":
		if customUTC != "" {
			return customUTC, nil
		}
		for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
			if t, err := time.ParseInLocation(layout, custom, loc); err == nil {
				return rfc(t), nil
			}
		}
		return "", errors.New("Pick a date and time for the custom expiry.")
	}
	return "", errors.New("Unknown expiry choice.")
}
