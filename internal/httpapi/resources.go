package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// resourcePolicy returns the policy applied to uploads. It reads limits
// from environment-style overrides on the config later; for now it uses
// the recommended defaults.
func (s *Server) resourcePolicy() resources.Policy {
	return resources.DefaultPolicy()
}

// openResources opens (or reuses) the per-notebook resource store and
// journal. The store backend is selected by config: S3 when configured,
// LocalStore otherwise. The journal borrows the notebook's state DB for
// the request duration.
func (s *Server) openResources(ctx context.Context, nb *notebooks.Notebook) (
	resources.Store, *resources.Journal, func(), error,
) {
	s3Cfg := resources.S3Config{
		Endpoint:  s.cfg.S3Endpoint,
		Bucket:    s.cfg.S3Bucket,
		Region:    s.cfg.S3Region,
		AccessKey: s.cfg.S3AccessKey,
		SecretKey: s.cfg.S3SecretKey,
		PathStyle: s.cfg.S3PathStyle,
	}
	store, err := resources.NewStore(nb.Layout.Resources(), s3Cfg)
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

// quotaCutoff returns the earliest updated_at a pending journal entry
// can have and still reserve quota. It must exceed the longest
// presigned-upload window (2× the configured TTL, with a floor of
// resources.ReservationTTL) so an in-flight direct upload never loses
// its reservation to the age cutoff.
func (s *Server) quotaCutoff() time.Time {
	age := resources.ReservationTTL
	if s.cfg.S3PresignTTL != "" {
		if d, err := time.ParseDuration(s.cfg.S3PresignTTL); err == nil && d > 0 && 2*d > age {
			age = 2 * d
		}
	}
	return time.Now().Add(-age)
}

// fitsQuota reports ErrQuotaExceeded when size more bytes would pass
// the notebook quota (committed bytes + in-flight reservations). A
// storage error is logged and treated as "fits": quota is best-effort
// under DB trouble, availability wins. Callers must hold uploadMu when
// the answer feeds a reservation.
func (s *Server) fitsQuota(ctx context.Context, journal *resources.Journal, size int64) error {
	if s.cfg.QuotaBytes <= 0 {
		return nil
	}
	used, reserved, err := journal.QuotaSnapshot(ctx, s.quotaCutoff())
	if err != nil {
		s.log.Warn("quota check failed", "err", err)
		return nil
	}
	if used+reserved+size > s.cfg.QuotaBytes {
		return fmt.Errorf("%w: %d of %d bytes used", ErrQuotaExceeded, used+reserved, s.cfg.QuotaBytes)
	}
	return nil
}

// reserveQuota atomically checks the quota and journals a pending
// reservation of checkSize. The mutex makes check+Begin one
// reservation window across every upload path, so two racing requests
// can never both pass the same free-space check. Returns
// ErrQuotaExceeded when it does not fit; the entry ID is valid only on
// success.
func (s *Server) reserveQuota(ctx context.Context, journal *resources.Journal, filename, mime string, checkSize int64) (string, error) {
	if s.cfg.QuotaBytes <= 0 {
		return journal.Begin(filename, checkSize, mime)
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if err := s.fitsQuota(ctx, journal, checkSize); err != nil {
		return "", err
	}
	return journal.Begin(filename, checkSize, mime)
}

// ErrQuotaExceeded is returned by reserveQuota when the upload would
// not fit; the HTTP layer maps it to 507.
var ErrQuotaExceeded = errors.New("quota exceeded")

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
		defer func() { _ = file.Close() }()
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

	// Quota check + journal begin as one atomic reservation. An unknown
	// size (chunked upload, Content-Length -1) is charged at the policy
	// maximum so it cannot slip past; Commit records the real stored
	// size below.
	checkSize := size
	if checkSize < 0 {
		checkSize = policy.MaxSize
		if checkSize <= 0 {
			checkSize = resources.DefaultMaxSize
		}
	}
	entryID, err := s.reserveQuota(r.Context(), journal, sanitizedName, mime, checkSize)
	if err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
			return
		}
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

	// Journal commit. Record the real stored size first — the
	// reservation may have been charged at the policy maximum when the
	// declared size was unknown.
	if storedSize != checkSize {
		if err := journal.SetSize(entryID, storedSize); err != nil {
			s.log.Warn("journal size update failed", "notebook", nb.Manifest.Slug, "entry", entryID, "err", err)
		}
	}
	if err := journal.Commit(entryID, sha); err != nil {
		// The blob is stored; commit failure is a DB issue, not a user error.
		s.log.Warn("journal commit failed", "notebook", nb.Manifest.Slug, "sha", sha, "err", err)
	}

	s.emit(r, nb, "resource.upload", sanitizedName, map[string]any{"sha": sha, "size": storedSize, "created": created})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       entryID,
		"sha":      sha,
		"filename": sanitizedName,
		"size":     storedSize,
		"mime":     mime,
		"created":  created, // false = dedup hit, already present
	})
}

// resourceMaterializeLimit caps how large a blob may be to materialize
// for Range serving. Uploads are ≤50 MiB (DefaultPolicy), so normal
// traffic never falls back to whole-body streaming.
const resourceMaterializeLimit = 100 << 20

// getResource serves GET /n/{slug}/resources/{sha}.
//
// The blob is spooled to a temp file and handed to http.ServeContent so
// Range/If-Range requests get 206 responses — HTML5 media players need
// seeking. Content is immutable (content-addressed), so ETag+immutable
// caching stays correct regardless of Range handling.
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
	defer func() { _ = rc.Close() }()

	// Look up the recorded MIME from the journal's committed entries. A
	// direct lookup, not List: List is capped, and a blob outside the
	// newest rows would be served as octet-stream (+nosniff), which the
	// browser will neither draw as an image nor play as audio.
	mime := "application/octet-stream"
	if m, err := journal.MIMEBySHA(sha); err == nil && m[sha] != "" {
		mime = m[sha]
	}

	size, _ := store.Size(sha)
	w.Header().Set("Content-Type", mime)
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if mime == "image/svg+xml" {
		// Legacy blobs from before SVG uploads were rejected: never
		// render inline, or embedded script runs same-origin.
		w.Header().Set("Content-Disposition", `attachment; filename="`+sha+`.svg"`)
	}

	if size >= 0 && size <= resourceMaterializeLimit {
		if tmp, terr := os.CreateTemp("", "nr-resource-*"); terr == nil {
			defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
			if _, cerr := io.Copy(tmp, rc); cerr == nil {
				_, _ = tmp.Seek(0, io.SeekStart)
				// Handles Range → 206, If-None-Match → 304, Content-Length.
				http.ServeContent(w, r, sha, time.Time{}, tmp)
				return
			}
		}
	}
	// Fallback: oversized blob or temp-file trouble — stream whole.
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
	_, journal, cleanup, err := s.openResources(r.Context(), nb)
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
	used, reserved, err := journal.QuotaSnapshot(r.Context(), s.quotaCutoff())
	if err != nil {
		s.log.Warn("stats: quota snapshot failed", "notebook", nb.Manifest.Slug, "err", err)
	}
	backend := "local"
	if s.cfg.S3Enabled() {
		backend = "s3"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"backend":       backend,
		"journal":       counts,
		"usedBytes":     used,
		"reservedBytes": reserved,
		"quotaBytes":    s.cfg.QuotaBytes,
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
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
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
	// Soft-mark in the journal so the audit trail is preserved.
	_ = journal.MarkDeleted(sha)
	s.emit(r, nb, "resource.delete", sha, nil)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": sha})
}

// presignUpload serves POST /n/{slug}/resources/presign.
//
// Body: {"sha":"<sha256>","size":123,"filename":"x.png","mime":"image/png"}
// Returns a presigned S3 PUT URL so the client can upload directly to
// object storage, bypassing the app server. When object storage is not
// configured, returns {"direct":true} so the client falls back to the
// normal server-side upload endpoint.
//
// Flow:
//  1. Client computes sha256 of the file.
//  2. Client POSTs here with {sha, size, filename, mime}.
//  3. Server validates policy + dedup check.
//  4. If blob exists → {"exists":true,"sha":...} (client skips upload).
//  5. If S3 configured → {"uploadURL":...,"sha":...,"entryID":...}.
//  6. If no S3 → {"direct":true} (client uses POST /resources instead).
//  7. After direct upload, client POSTs /resources/confirm {entryID, sha}.
func (s *Server) presignUpload(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	var req struct {
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		Filename string `json:"filename"`
		MIME     string `json:"mime"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	if req.SHA == "" || req.Filename == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sha and filename required"})
		return
	}
	// The sha becomes the object key in presigned URLs — reject anything
	// that is not a content hash before it can influence a key.
	if !resources.ValidSHA(req.SHA) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sha: must be 64 lowercase hex chars"})
		return
	}

	policy := s.resourcePolicy()
	_, err := policy.Validate(req.Filename, req.Size, req.MIME)
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

	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	// Early, read-only quota check: an upload that cannot fit is refused
	// on every backend, before dedup. No journal row yet.
	checkSize := req.Size
	if checkSize < 0 {
		checkSize = policy.MaxSize
		if checkSize <= 0 {
			checkSize = resources.DefaultMaxSize
		}
	}
	if err := s.fitsQuota(r.Context(), journal, checkSize); err != nil {
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
		return
	}

	// Dedup: blob already present → nothing new to store, so no
	// reservation and no journal row.
	if store.Has(req.SHA) {
		writeJSON(w, http.StatusOK, map[string]any{"exists": true, "sha": req.SHA})
		return
	}

	// Non-S3 store: client must use the server-side upload endpoint,
	// which does its own quota reservation.
	s3Store, isS3 := store.(*resources.S3Store)
	if !isS3 {
		writeJSON(w, http.StatusOK, map[string]any{"direct": true})
		return
	}

	// A new blob headed for S3: re-check and journal as one atomic
	// reservation (the early check above is advisory; concurrent
	// requests are serialized here). The pending row holds the quota
	// until confirm or until it ages out of the reservation window.
	entryID, err := s.reserveQuota(r.Context(), journal, req.Filename, req.MIME, checkSize)
	if err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "journal begin failed"})
		return
	}

	ttl := 15 * time.Minute
	if s.cfg.S3PresignTTL != "" {
		if d, err := time.ParseDuration(s.cfg.S3PresignTTL); err == nil && d > 0 {
			ttl = d
		}
	}

	uploadURL, err := s3Store.Presign(http.MethodPut, req.SHA, ttl)
	if err != nil {
		_ = journal.Abort(entryID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "presign failed: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"direct":    false,
		"uploadURL": uploadURL,
		"sha":       req.SHA,
		"entryID":   entryID,
		"expiresIn": int(ttl.Seconds()),
	})
}

// confirmUpload serves POST /n/{slug}/resources/confirm.
//
// Body: {"entryID":"...","sha":"..."} — called by the client after it
// finishes a direct-to-S3 upload. The server verifies the blob exists
// and commits the journal entry.
func (s *Server) confirmUpload(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		EntryID string `json:"entryID"`
		SHA     string `json:"sha"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.EntryID == "" || req.SHA == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "entryID and sha required"})
		return
	}
	if !resources.ValidSHA(req.SHA) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sha: must be 64 lowercase hex chars"})
		return
	}

	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	if !store.Has(req.SHA) {
		_ = journal.Abort(req.EntryID)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "blob not found in store"})
		return
	}
	if err := journal.Commit(req.EntryID, req.SHA); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.emit(r, nb, "resource.upload", "", map[string]any{"sha": req.SHA})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sha": req.SHA})
}

// presignRead serves GET /n/{slug}/resources/{sha}/presign-read — returns
// a presigned GET URL for direct browser download from object storage.
// Falls back to {"direct":true} for local stores.
func (s *Server) presignRead(w http.ResponseWriter, r *http.Request) {
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

	s3Store, isS3 := store.(*resources.S3Store)
	if !isS3 {
		writeJSON(w, http.StatusOK, map[string]any{"direct": true, "url": "/n/" + nb.Manifest.Slug + "/resources/" + sha})
		return
	}

	ttl := 15 * time.Minute
	if s.cfg.S3PresignTTL != "" {
		if d, err := time.ParseDuration(s.cfg.S3PresignTTL); err == nil && d > 0 {
			ttl = d
		}
	}
	readURL, err := s3Store.Presign(http.MethodGet, sha, ttl)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "presign failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"direct":    false,
		"url":       readURL,
		"expiresIn": int(ttl.Seconds()),
	})
}

// listResourceRefs serves GET /n/{slug}/resources/{sha}/refs.
//
// Scans all markdown files in the notebook vault for any occurrence of
// the given SHA and returns their vault-relative paths. The scan is
// bounded by a 3-second context timeout and capped at 2000 files to
// avoid blocking under very large vaults. Refs are lazily loaded by the
// Attachments settings page.
func (s *Server) listResourceRefs(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))
	if !resources.ValidSHA(sha) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sha"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	v := vault.Open(nb.Layout.Vault())
	needle := []byte(sha)
	var refs []string
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
		if rerr != nil {
			return nil
		}
		if bytes.Contains(data, needle) {
			rel, rerr := filepath.Rel(v.Root(), path)
			if rerr == nil {
				refs = append(refs, filepath.ToSlash(rel))
			}
		}
		return nil
	})

	if refs == nil {
		refs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"refs": refs})
}
