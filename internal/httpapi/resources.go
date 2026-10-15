package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/state"
)

// resourcePolicy returns the policy applied to uploads. It reads limits
// from environment-style overrides on the config later; for now it uses
// the recommended defaults.
func (s *Server) resourcePolicy() resources.Policy {
	return resources.DefaultPolicy()
}

// openResources opens (or reuses) the per-notebook resource store and
// journal. Both are cheap to create; the journal borrows the notebook's
// state DB for the request duration.
func (s *Server) openResources(ctx context.Context, nb *notebooks.Notebook) (
	*resources.LocalStore, *resources.Journal, func(), error,
) {
	store, err := resources.NewLocalStore(nb.Layout.Resources())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open store: %w", err)
	}
	db, _, err := state.Open(ctx, nb.Layout.StateDB())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open state: %w", err)
	}
	journal := resources.NewJournal(db.DB)
	cleanup := func() { _ = db.Close() }
	return store, journal, cleanup, nil
}

// uploadResource handles POST /n/{slug}/resources.
//
// Two request shapes are accepted:
//   - multipart/form-data with a "file" field (browser drag-and-drop).
//   - raw body with an X-Filename query parameter (scripts, curl).
//
// The flow is: policy → journal.Begin → store.Put → journal.Commit.
// Any failure aborts the journal entry and returns a structured error.
func (s *Server) uploadResource(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	policy := s.resourcePolicy()
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	var (
		src      io.Reader
		filename string
		size     int64
		mime     string
	)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// Cap the multipart read at MaxSize+1 so an oversized file is
		// rejected before buffering it all.
		maxSize := policy.MaxSize
		if maxSize <= 0 {
			maxSize = resources.DefaultMaxSize
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSize+1)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad multipart: " + err.Error()})
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
			return
		}
		defer file.Close()
		filename = header.Filename
		size = header.Size
		mime = header.Header.Get("Content-Type")
		if mime == "" || mime == "application/octet-stream" {
			mime = resources.SniffMIME(filename)
		}
		src = file
	} else {
		// Raw body upload: filename from query, size from Content-Length.
		filename = r.URL.Query().Get("filename")
		if filename == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing filename"})
			return
		}
		size = r.ContentLength
		mime = r.Header.Get("Content-Type")
		if mime == "" {
			mime = resources.SniffMIME(filename)
		}
		maxSize := policy.MaxSize
		if maxSize <= 0 {
			maxSize = resources.DefaultMaxSize
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSize+1)
		src = r.Body
	}

	// Policy check.
	sanitizedName, err := policy.Validate(filename, size, mime)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, resources.ErrTooLarge):
			status = http.StatusRequestEntityTooLarge
		case errors.Is(err, resources.ErrBadMIME), errors.Is(err, resources.ErrBadName):
			status = http.StatusUnsupportedMediaType
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	// Journal begin.
	entryID, err := journal.Begin(sanitizedName, size, mime)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "journal begin failed"})
		return
	}

	// Store the blob.
	sha, storedSize, created, err := store.Put(src)
	if err != nil {
		_ = journal.Abort(entryID)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "write:") || strings.Contains(err.Error(), "hash:") {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]string{"error": "store failed: " + err.Error()})
		return
	}

	// Verify size matches what policy allowed (guards against a lying
	// Content-Length that snuck past MaxBytesReader).
	if size > 0 && storedSize != size {
		_ = store.Delete(sha)
		_ = journal.Abort(entryID)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("size mismatch: declared %d, stored %d", size, storedSize),
		})
		return
	}

	// Journal commit.
	if err := journal.Commit(entryID, sha); err != nil {
		// The blob is stored; commit failure is a DB issue, not a user error.
		s.log.Warn("journal commit failed", "notebook", nb.Manifest.Slug, "sha", sha, "err", err)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       entryID,
		"sha":      sha,
		"filename": sanitizedName,
		"size":     storedSize,
		"mime":     mime,
		"created":  created, // false = dedup hit, already present
	})
}

// getResource serves GET /n/{slug}/resources/{sha}.
func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	rc, err := store.Get(sha)
	if err != nil {
		if errors.Is(err, resources.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	defer rc.Close()

	// Look up the recorded MIME from the journal's committed entries.
	mime := "application/octet-stream"
	if entries, err := journal.List(resources.StateCommitted, 0); err == nil {
		for _, e := range entries {
			if e.SHA == sha && e.MIME != "" {
				mime = e.MIME
				break
			}
		}
	}

	size, _ := store.Size(sha)
	w.Header().Set("Content-Type", mime)
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// listResources serves GET /n/{slug}/resources — recent uploads.
func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	_, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	stateFilter := r.URL.Query().Get("state")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := journal.List(stateFilter, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	if entries == nil {
		entries = []resources.JournalEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": entries})
}

// resourceStats serves GET /n/{slug}/resources/stats.
func (s *Server) resourceStats(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	counts, err := journal.CountByState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stats failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"storeRoot": filepath.Base(store.Root()),
		"journal":   counts,
	})
}

// deleteResource serves DELETE /n/{slug}/resources/{sha}.
func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))
	store, _, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	if !store.Has(sha) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err := store.Delete(sha); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": sha})
}
