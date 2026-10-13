// Package api is the write API: devices set and clear slots through it with
// a per-device bearer token. It and the web account page both end in
// store.WriteSlot and store.ClearSlot, which own every slot rule.
//
//	PUT    /api/v1/slots/{key}  {"value": "...", "until": "RFC 3339", "source": "token"}
//	DELETE /api/v1/slots/{key}
//	GET    /api/v1/me           the composed document as format.DocumentJSON
//
// Success is 204 (200 with a body for /me), a missing or bad token is 401,
// and a validation failure is 422. Every error has a JSON body
// {"error": "..."}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

// maxBody is the largest request body the API reads.
const maxBody = 4 << 10

// Register adds the API routes to mux.
func Register(mux *http.ServeMux, st *store.Store) {
	h := &handler{st: st}
	mux.HandleFunc("PUT /api/v1/slots/{key}", h.auth(h.putSlot))
	mux.HandleFunc("DELETE /api/v1/slots/{key}", h.auth(h.deleteSlot))
	mux.HandleFunc("GET /api/v1/me", h.auth(h.me))
}

type handler struct {
	st *store.Store
}

type tokenKey struct{}

// auth resolves the bearer token and passes it to next in the context.
// Unknown, revoked, and suspended-account tokens all get the same 401.
func (h *handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plain, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		plain = strings.TrimSpace(plain)
		if !ok || plain == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nowfile"`)
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		tok, err := h.st.LookupToken(r.Context(), plain)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSuspended) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nowfile", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok)))
	}
}

func token(r *http.Request) store.Token { return r.Context().Value(tokenKey{}).(store.Token) }

// slotRequest is the PUT body. Until and Source are optional.
type slotRequest struct {
	Value  string `json:"value"`
	Until  string `json:"until"`
	Source string `json:"source"`
}

func (h *handler) putSlot(w http.ResponseWriter, r *http.Request) {
	tok := token(r)
	var req slotRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "body must be a JSON object with value, and optionally until and source: "+err.Error())
		return
	}
	source := req.Source
	if source == "" {
		source = sourceFromLabel(tok.Label)
	}
	// WriteSlot owns every rule; the account page calls it too.
	_, err := h.st.WriteSlot(r.Context(), tok.UserID, store.SlotWrite{Key: r.PathValue("key"), Value: req.Value, Until: req.Until, Source: source})
	if h.failed(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteSlot(w http.ResponseWriter, r *http.Request) {
	if h.failed(w, r, h.st.ClearSlot(r.Context(), token(r).UserID, r.PathValue("key"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failed writes the response for a non-nil slot write error: 422 with the
// rule's message for a *store.ValidationError, 500 otherwise.
func (h *handler) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if ve := (*store.ValidationError)(nil); errors.As(err, &ve) {
		writeError(w, http.StatusUnprocessableEntity, ve.Message)
	} else {
		internalError(w, r, err)
	}
	return true
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	doc, err := h.st.Document(r.Context(), token(r).UserID, h.st.Now())
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSuspended) {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(format.ToJSON(doc))
}

// sourceFromLabel turns a token label such as "Mike's laptop" into a source
// token such as "mike-s-laptop": lowercase ASCII letters and digits, other
// runs replaced by one hyphen, at most store.MaxSourceBytes. A label with
// nothing usable becomes "api".
func sourceFromLabel(label string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(label) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
		} else {
			dash = true
		}
	}
	s := b.String()
	if len(s) > store.MaxSourceBytes {
		s = strings.TrimRight(s[:store.MaxSourceBytes], "-")
	}
	if s == "" {
		return "api"
	}
	return s
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("api: %s %s: %v", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
