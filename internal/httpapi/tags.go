package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"noteroot/internal/frontmatter"
)

// listTags serves GET /n/{slug}/tags: every frontmatter tag in the
// notebook with the notes carrying it, as {"tags": {"tag": ["a.md"]}}.
// The walk is bounded (file count and time) like the resource-refs scan,
// so a huge vault degrades to a partial list instead of a slow request.
func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	v, ok := s.vaultFor(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	const maxFiles = 5000
	tags := map[string][]string{}
	scanned := 0
	_ = filepath.WalkDir(v.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != v.Root() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		scanned++
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(v.Root(), path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		seen := map[string]bool{}
		for _, t := range frontmatter.Strings(frontmatter.Parse(data).Meta, "tags") {
			t = strings.TrimPrefix(strings.TrimSpace(t), "#")
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			tags[t] = append(tags[t], rel)
		}
		return nil
	})
	for _, paths := range tags {
		sort.Strings(paths)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}
