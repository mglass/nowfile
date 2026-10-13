package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

// userPage is the data for the public HTML page.
type userPage struct {
	Username   string
	URL        string
	FingerHost string
	Doc        format.Document
	Title      string // display name, or the username
	Current    string // format.Headline
}

// public serves GET /{name}, /{name}.txt, and /{name}.json. All three come
// from one store.DocumentByName call, which composes at the store's Now, so
// they cannot disagree and never carry an expired slot. Unknown, suspended,
// and deleted users get the same 404.
func (s *Server) public(w http.ResponseWriter, r *http.Request) {
	name, kind := r.PathValue("name"), "html"
	if n, ok := strings.CutSuffix(name, ".txt"); ok {
		name, kind = n, "txt"
	} else if n, ok := strings.CutSuffix(name, ".json"); ok {
		name, kind = n, "json"
	}
	name = strings.ToLower(name)

	doc, err := s.st.DocumentByName(r.Context(), name, s.st.Now())
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSuspended) {
		s.notFound(w, r, kind)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	switch kind {
	case "txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(format.Render(doc, true)))
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(format.ToJSON(doc))
	default:
		title := doc.Name
		if title == "" {
			title = name
		}
		u, _ := s.currentUser(r)
		p := page{Title: title, User: u, Data: userPage{
			Username:   name,
			URL:        s.cfg.BaseURL + "/" + name,
			FingerHost: s.fingerHost(),
			Doc:        doc,
			Title:      title,
			Current:    format.Headline(doc),
		}}
		if u != nil {
			p.CSRF = s.csrfToken(w, r)
		}
		s.render(w, http.StatusOK, "user", p)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, kind string) {
	switch kind {
	case "txt":
		http.Error(w, "not found", http.StatusNotFound)
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}` + "\n"))
	default:
		s.message(w, r, http.StatusNotFound, "Not found", "There is no nowfile here.")
	}
}
