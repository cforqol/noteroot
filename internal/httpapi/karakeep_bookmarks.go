package httpapi

// karakeep_bookmarks.go lists every URL this notebook has archived with
// Karakeep, and which notes mention it. There is no separate bookmarks
// database to keep in sync with edits — the paste handler's own output
// (the pasted URL immediately followed by " ([archived](<archive
// URL>))") is the single source of truth, so a note that later drops
// the marker simply drops out of this list, same as deleting a resource
// link makes it disappear from the attachment manager's references.

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"noteroot/internal/vault"
)

// karakeepArchivedRe matches the exact text the paste handler inserts:
// a bare URL, a space, then the archived-link marker. Capture groups
// are the original URL and the Karakeep-hosted archive URL.
var karakeepArchivedRe = regexp.MustCompile(`(https?://[^\s()]+)\s\(\[archived\]\((https?://[^\s()]+)\)\)`)

type karakeepBookmark struct {
	URL        string   `json:"url"`
	ArchiveURL string   `json:"archiveUrl"`
	Paths      []string `json:"paths"`
}

// listKarakeepBookmarks serves GET /n/{slug}/integrations/karakeep/bookmarks.
//
// Scans every markdown file in the vault for the archived-link marker,
// grouping occurrences by (url, archiveUrl) pair so a URL pasted into
// several notes lists all of them together. Bounded like
// listResourceRefs: a 3-second timeout and a 2000-file cap, since this
// is a full vault scan with no index behind it.
func (s *Server) listKarakeepBookmarks(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	v := vault.Open(nb.Layout.Vault())
	byKey := make(map[string]*karakeepBookmark)
	order := make([]string, 0)
	scanned := 0
	const maxFiles = 2000

	_ = filepath.WalkDir(v.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		scanned++
		data, rerr := os.ReadFile(path)
		if rerr != nil || !bytes.Contains(data, []byte("([archived](")) {
			return nil
		}
		rel, rerr := filepath.Rel(v.Root(), path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, m := range karakeepArchivedRe.FindAllStringSubmatch(string(data), -1) {
			url, archiveURL := m[1], m[2]
			key := url + "\x00" + archiveURL
			b, ok := byKey[key]
			if !ok {
				b = &karakeepBookmark{URL: url, ArchiveURL: archiveURL}
				byKey[key] = b
				order = append(order, key)
			}
			if !slices.Contains(b.Paths, rel) {
				b.Paths = append(b.Paths, rel)
			}
		}
		return nil
	})

	out := make([]*karakeepBookmark, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmarks": out})
}
