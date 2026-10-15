package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"net/http"
	"time"

	"noteroot/internal/frontmatter"
	"noteroot/internal/markdown"
	"noteroot/internal/vault"
)

func (s *Server) vaultFor(slug string) (*vault.Vault, bool) {
	nb, ok := s.reg.Get(slug)
	if !ok {
		return nil, false
	}
	return vault.Open(nb.Layout.Vault()), true
}

// entityHeaders exposes the file identity editors base their writes on:
// ETag is the sha256 of the exact bytes served.
func entityHeaders(w http.ResponseWriter, src []byte, modTime time.Time) {
	w.Header().Set("ETag", `"`+etagOf(src)+`"`)
	if !modTime.IsZero() {
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
	}
}

func etagOf(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// renderNote is a seam so tests can force a render failure.
var renderNote = func(src []byte) ([]byte, error) {
	return markdown.New().Render(src)
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	v, ok := s.vaultFor(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	t, err := v.Tree()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "walk failed"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) {
	v, ok := s.vaultFor(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")

	src, err := v.Read(rel)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	_, modTime, _ := v.Stat(rel)
	entityHeaders(w, src, modTime)

	doc := frontmatter.Parse(src)
	resp := map[string]any{
		"path":  rel,
		"id":    doc.ID,
		"title": doc.Title,
		"tags":  frontmatter.Strings(doc.Meta, "tags"),
		"sha":   etagOf(src),
	}
	if doc.Err != nil {
		resp["frontmatterError"] = doc.Err.Error()
	}

	// A render failure must never prevent the note from opening.
	if out, rerr := renderNote(doc.Body); rerr == nil {
		resp["html"] = string(out)
	} else {
		resp["html"] = "<pre>" + html.EscapeString(string(doc.Body)) + "</pre>"
		resp["renderError"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	v, ok := s.vaultFor(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	src, err := v.Read(rel)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	_, modTime, _ := v.Stat(rel)
	entityHeaders(w, src, modTime)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "private")
	_, _ = w.Write(src)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotFile):
		return http.StatusNotFound
	case errors.Is(err, vault.ErrEscapes):
		return http.StatusForbidden
	case errors.Is(err, vault.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}
