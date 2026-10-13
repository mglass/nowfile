package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/username"
)

const (
	sessionCookie = "nf_session"
	// csrfCookie holds the CSRF token for signed-out forms (double submit).
	// Signed-in forms derive the token from the session instead.
	csrfCookie = "nf_csrf"
	csrfField  = "csrf"

	sessionTTL = 30 * 24 * time.Hour
	linkTTL    = 15 * time.Minute
	maxForm    = 64 << 10
)

// currentUser returns the signed-in account, or nil.
func (s *Server) currentUser(r *http.Request) (*store.User, string) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	u, err := s.st.SessionUser(r.Context(), c.Value)
	if err != nil {
		return nil, ""
	}
	return &u, c.Value
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// sessionCSRF derives a session's CSRF token from its secret. Knowing the
// token does not reveal the session.
func sessionCSRF(session string) string {
	return store.HashToken("csrf:" + session)
}

// csrfToken returns the token to put in a form's hidden field, setting the
// signed-out CSRF cookie if the request has neither a session nor one.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if _, sess := s.currentUser(r); sess != "" {
		return sessionCSRF(sess)
	}
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 24)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.setCookie(w, csrfCookie, tok, 0)
	return tok
}

// post wraps a POST handler: it caps the body, parses the form, and rejects
// the request with 403 unless the csrf field matches the session's token
// (signed in) or the CSRF cookie (signed out).
func (s *Server) post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxForm)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		var want string
		if _, sess := s.currentUser(r); sess != "" {
			want = sessionCSRF(sess)
		} else if c, err := r.Cookie(csrfCookie); err == nil {
			want = c.Value
		}
		got := r.PostForm.Get(csrfField)
		if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			s.message(w, r, http.StatusForbidden, "Form expired", "That form was stale or did not come from this site. Go back, reload, and try again.")
			return
		}
		h(w, r)
	}
}

type signupData struct {
	Name, Email string
}

func (s *Server) signupForm(w http.ResponseWriter, r *http.Request) {
	if u, _ := s.currentUser(r); u != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "signup", page{Title: "Sign up", CSRF: s.csrfToken(w, r), Data: signupData{}})
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	d := signupData{Name: strings.TrimSpace(r.PostForm.Get("name")), Email: strings.TrimSpace(r.PostForm.Get("email"))}
	fail := func(msg string) {
		s.render(w, http.StatusUnprocessableEntity, "signup", page{Title: "Sign up", CSRF: s.csrfToken(w, r), Error: msg, Data: d})
	}
	u, err := s.st.CreateUser(r.Context(), d.Name, d.Email)
	switch {
	case errors.Is(err, username.ErrReserved):
		fail("That name is reserved. Pick another.")
		return
	case errors.Is(err, username.ErrInvalid):
		fail(usernameHelp(err))
		return
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrNameHeld):
		fail("That name is taken. Pick another.")
		return
	case errors.Is(err, store.ErrInvalidEmail):
		fail("That email address does not look right.")
		return
	case errors.Is(err, store.ErrEmailTaken):
		// One account per email. Send that account a sign-in link and show
		// the same page as a fresh sign-up, so the form does not reveal
		// which addresses have accounts.
		existing, err := s.st.UserByEmail(r.Context(), d.Email)
		if err == nil && !existing.Suspended {
			if err := s.sendLink(r, existing); err != nil {
				log.Printf("web: signup sign-in link for existing account: %v", err)
			}
		}
		s.checkEmail(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if err := s.sendLink(r, u); err != nil {
		s.serverError(w, r, fmt.Errorf("signup mail: %w", err))
		return
	}
	s.checkEmail(w, r)
}

// usernameHelp turns a username.Validate error into a sentence.
func usernameHelp(err error) string {
	const rule = "Use 2 to 32 lowercase letters, digits, and hyphens, starting with a letter."
	_, detail, _ := strings.Cut(err.Error(), ": ")
	if detail == "" {
		return "That name does not work. " + rule
	}
	return "That name does not work (" + detail + "). " + rule
}

func (s *Server) checkEmail(w http.ResponseWriter, r *http.Request) {
	s.message(w, r, http.StatusOK, "Check your email", "If that address has an account, a sign-in link is on its way. It works once and expires in 15 minutes.")
}

func (s *Server) sendLink(r *http.Request, u store.User) error {
	tok, err := s.st.CreateMagicLink(r.Context(), u.ID, s.st.Now().Add(linkTTL))
	if err != nil {
		return err
	}
	link := s.cfg.BaseURL + "/login/" + tok
	return s.mail.Send(r.Context(), mail.Message{
		To:      u.Email,
		Subject: "Sign in to nowfile",
		Text: fmt.Sprintf("Sign in to nowfile as %s:\n\n%s\n\nThe link works once and expires in 15 minutes. If you did not ask for it, ignore this email.\n",
			u.Name, link),
	})
}

type loginData struct {
	Email string
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if u, _ := s.currentUser(r); u != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "login", page{Title: "Sign in", CSRF: s.csrfToken(w, r), Data: loginData{}})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostForm.Get("email"))
	if email == "" {
		s.render(w, http.StatusUnprocessableEntity, "login", page{Title: "Sign in", CSRF: s.csrfToken(w, r), Error: "Enter your email address.", Data: loginData{}})
		return
	}
	u, err := s.st.UserByEmail(r.Context(), email)
	switch {
	case err == nil && !u.Suspended:
		if err := s.sendLink(r, u); err != nil {
			log.Printf("web: login mail: %v", err)
		}
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.serverError(w, r, err)
		return
	}
	s.checkEmail(w, r)
}

// consumeLink signs in with a magic link and starts a session.
func (s *Server) consumeLink(w http.ResponseWriter, r *http.Request) {
	// The token is in this URL; keep it out of Referer headers.
	w.Header().Set("Referrer-Policy", "no-referrer")
	userID, err := s.st.ConsumeMagicLink(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrLinkInvalid) || errors.Is(err, store.ErrSuspended) {
		s.message(w, r, http.StatusBadRequest, "Link expired", "That sign-in link is invalid, used, or expired. Ask for a new one from the sign-in page.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if _, old := s.currentUser(r); old != "" {
		s.st.DeleteSession(r.Context(), old)
	}
	sess, err := s.st.CreateSession(r.Context(), userID, s.st.Now().Add(sessionTTL))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.st.TouchLastSeen(r.Context(), userID); err != nil {
		log.Printf("web: touch last seen: %v", err)
	}
	s.setCookie(w, sessionCookie, sess, int(sessionTTL/time.Second))
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, sess := s.currentUser(r); sess != "" {
		if err := s.st.DeleteSession(r.Context(), sess); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
