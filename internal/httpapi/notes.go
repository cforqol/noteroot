package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"net/http"
	"net/url"
	"time"

	"noteroot/internal/frontmatter"
	"noteroot/internal/index"
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

// renderNote is a seam so tests can force a render failure. resolve is
// nil when no link data is available; wikilinks then render as visible
// dead spans, never as literal [[brackets]] and never silently dropped.
var renderNote = func(src []byte, resolve func(string) (string, bool)) ([]byte, error) {
	return markdown.New().RenderWithResolver(src, resolve)
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
	slug := r.PathValue("slug")
	v, ok := s.vaultFor(slug)
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

	// Refresh the index (mtime-cheap, same as search) and read the link
	// rows behind the panels. The renderer resolves wikilinks against
	// those same rows, so the HTML and the outgoing panel can never
	// disagree (design §7). Index trouble degrades to no panels and
	// dead-span wikilinks — it never blocks opening the note.
	var resolve func(string) (string, bool)
	if nb, ok := s.reg.Get(slug); ok {
		idx, _, oerr := index.Open(r.Context(), nb.Layout.IndexDB(), nb.Layout.StateDB())
		if oerr != nil {
			s.log.Warn("index open failed", "notebook", slug, "err", oerr)
		} else {
			if _, uerr := idx.Update(r.Context(), v); uerr != nil {
				s.log.Warn("index update failed", "notebook", slug, "err", uerr)
			}
			if back, berr := idx.Backlinks(r.Context(), rel); berr == nil {
				resp["backlinks"] = back
			}
			if out, oerr := idx.Outgoing(r.Context(), rel); oerr == nil {
				resp["outgoing"] = out
				resolve = outgoingResolver(slug, out)
			}
			_ = idx.Close()
		}
	}

	// A render failure must never prevent the note from opening.
	if out, rerr := renderNote(doc.Body, resolve); rerr == nil {
		resp["html"] = string(out)
	} else {
		resp["html"] = "<pre>" + html.EscapeString(string(doc.Body)) + "</pre>"
		resp["renderError"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// outgoingResolver maps wikilink targets to note hrefs using the same
// links rows that back the outgoing panel. Unresolved targets report
// ok=false so the renderer marks them dead instead of guessing.
func outgoingResolver(slug string, links []index.Link) func(string) (string, bool) {
	byTarget := make(map[string]string, len(links))
	for _, l := range links {
		if l.Path != "" {
			byTarget[l.Target] = l.Path
		}
	}
	return func(target string) (string, bool) {
		p, ok := byTarget[target]
		if !ok {
			return "", false
		}
		return "/n/" + slug + "/note?path=" + url.QueryEscape(p), true
	}
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
