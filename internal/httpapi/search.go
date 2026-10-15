package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"noteroot/internal/index"
	"noteroot/internal/vault"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	v := vault.Open(nb.Layout.Vault())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing query"})
		return
	}

	ctx := r.Context()
	idx, _, err := index.Open(ctx, nb.Layout.IndexDB())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "index unavailable"})
		return
	}
	defer idx.Close()
	if _, err := idx.Update(ctx, v); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "index update failed"})
		return
	}
	hits, err := idx.Search(ctx, q, searchLimit(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad query"})
		return
	}

	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"path":    h.Path,
			"title":   h.Title,
			"snippet": safeSnippet(h.Snippet),
			"rank":    h.Rank,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "hits": out})
}

// safeSnippet escapes raw note text, then converts the \x01..\x02 match
// markers into highlight tags. Escaping first keeps note HTML inert.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func safeSnippet(raw string) string {
	s := htmlEscaper.Replace(raw)
	s = strings.ReplaceAll(s, "\x01", "<b>")
	s = strings.ReplaceAll(s, "\x02", "</b>")
	return s
}

func searchLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return defaultSearchLimit
	}
	if n > maxSearchLimit {
		return maxSearchLimit
	}
	return n
}
