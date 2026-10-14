// Package web serves nowfile over HTTP: the landing page, each user's page
// as HTML, text, and JSON, sign-up and magic-link sign-in, and the account
// page. Every public rendering comes from one store.DocumentByName call, and
// account-page slot writes go through store.WriteSlot, as the write API does.
// The write API (internal/api) is mounted on the same mux.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mglass/nowfile/internal/api"
	"github.com/mglass/nowfile/internal/config"
	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/sources"
	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/subs"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the HTTP site. It implements http.Handler.
type Server struct {
	cfg    config.Config
	st     *store.Store
	mail   mail.Sender
	subs   *subs.Service
	secure bool // set the Secure attribute on cookies
	pages  map[string]*template.Template
	mux    *http.ServeMux
	fetch  *sources.Fetcher // calendar previews; refuses non-public addresses
}

// Serve runs the site on cfg.HTTPAddr until ctx is cancelled, then shuts
// down gracefully. Sign-in mail goes through Postmark when
// cfg.PostmarkToken is set and to the log otherwise.
func Serve(ctx context.Context, cfg config.Config, st *store.Store) error {
	s, err := New(cfg, st, mail.New(cfg.PostmarkToken, cfg.MailFrom))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("web: listening on %s", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// New builds the site. Cookies are marked Secure unless cfg.BaseURL is
// plain http, so a dev server on http://localhost works in every browser.
func New(cfg config.Config, st *store.Store, m mail.Sender) (*Server, error) {
	s := &Server{
		cfg:    cfg,
		st:     st,
		mail:   m,
		subs:   subs.New(st, m, cfg.BaseURL),
		fetch:  sources.NewFetcher(false),
		secure: !strings.HasPrefix(cfg.BaseURL, "http://"),
		pages:  map[string]*template.Template{},
		mux:    http.NewServeMux(),
	}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "templates/")
		if base == "layout.html" {
			continue
		}
		t, err := template.New(base).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", name)
		if err != nil {
			return nil, err
		}
		s.pages[strings.TrimSuffix(base, ".html")] = t
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	s.mux.HandleFunc("GET /{$}", s.landing)
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /signup", s.signupForm)
	s.mux.HandleFunc("POST /signup", s.post(s.signup))
	s.mux.HandleFunc("GET /login", s.loginForm)
	s.mux.HandleFunc("POST /login", s.post(s.login))
	s.mux.HandleFunc("GET /login/{token}", s.consumeLink)
	s.mux.HandleFunc("POST /logout", s.post(s.logout))
	s.mux.HandleFunc("GET /account", s.account)
	s.mux.HandleFunc("POST /account", s.post(s.accountPost))
	s.mux.HandleFunc("GET /directory", s.directory)
	// /{name}, /{name}.txt, /{name}.json, and /{name}.rss share one pattern
	// because a wildcard must be a whole path segment.
	s.mux.HandleFunc("GET /{name}", s.public)
	// Subscription routes take no CSRF token; see subsForm.
	s.mux.HandleFunc("POST /subscribe/{name}", s.subscribe)
	s.mux.HandleFunc("GET /subscribe/confirm/{token}", s.confirmPage)
	s.mux.HandleFunc("POST /subscribe/confirm/{token}", s.confirm)
	s.mux.HandleFunc("GET /unsubscribe/{token}", s.unsubscribePage)
	s.mux.HandleFunc("POST /unsubscribe/{token}", s.unsubscribe)
	api.Register(s.mux, st)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: http:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	s.mux.ServeHTTP(w, r)
}

var funcs = template.FuncMap{
	"rfc3339": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	},
}

// page is the data every template gets.
type page struct {
	Title  string
	User   *store.User
	CSRF   string
	Error  string
	Notice string
	Data   any
}

func (s *Server) render(w http.ResponseWriter, status int, name string, p page) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Printf("web: no template %q", name)
		return
	}
	var b strings.Builder
	if err := t.ExecuteTemplate(&b, "layout", p); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Printf("web: render %s: %v", name, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(b.String()))
}

// message renders a page that is only a heading and a paragraph.
func (s *Server) message(w http.ResponseWriter, r *http.Request, status int, title, text string) {
	u, _ := s.currentUser(r)
	p := page{Title: title, User: u, Notice: text}
	if u != nil {
		p.CSRF = s.csrfToken(w, r)
	}
	s.render(w, status, "message", p)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("web: %s %s: %v", r.Method, r.URL.Path, err)
	s.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server hit an error. Try again in a minute.")
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	u, _ := s.currentUser(r)
	p := page{Title: "nowfile", User: u}
	if u != nil {
		p.CSRF = s.csrfToken(w, r)
	}
	s.render(w, http.StatusOK, "landing", p)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

// fingerHost is the host part of BaseURL, for "finger name@host" hints.
func (s *Server) fingerHost() string {
	u, err := url.Parse(s.cfg.BaseURL)
	if err != nil || u.Hostname() == "" {
		return "localhost"
	}
	return u.Hostname()
}
