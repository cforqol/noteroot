package httpapi

// resources_fetch.go backs the "fetch a referenced attachment" step of
// plain-markdown note import (#107): a note imported from outside
// noteroot can reference an attachment by an external URL instead of
// bundling the blob the way a .tar.gz export does. There is no
// companion file to upload in that case, so the browser asks the
// server to fetch the URL itself and store the result exactly like a
// direct upload — same journal/quota/policy pipeline, same response
// shape.
//
// This is the first outbound request this server makes whose target
// comes from note content rather than an admin-configured value
// (Karakeep/Slink/S3/webhooks all dial addresses an operator typed
// into config). guardedFetchClient is the SSRF defense that makes
// that safe: see its doc comment.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"noteroot/internal/resources"
)

const (
	maxFetchRequestBody = 4 << 10 // 4 KiB — just a URL
	fetchTimeout        = 20 * time.Second
)

type fetchResourceRequest struct {
	URL string `json:"url"`
}

// fetchResource handles POST /n/{slug}/resources/fetch.
//
// Request body:  {"url": "https://example.com/image.png"}
// Response body: same shape as POST /n/{slug}/resources (sha, filename,
// size, mime, created).
func (s *Server) fetchResource(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxFetchRequestBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read error"})
		return
	}
	var req fetchResourceRequest
	if err := json.Unmarshal(body, &req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	target, err := parseFetchURL(req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	policy := s.resourcePolicy()
	maxSize := policy.MaxSize
	if maxSize <= 0 {
		maxSize = resources.DefaultMaxSize
	}

	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	client := s.fetchClient
	if client == nil {
		client = guardedFetchClient(fetchTimeout)
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "request build failed"})
		return
	}
	upstream.Header.Set("User-Agent", "noteroot-import/1.0")
	resp, err := client.Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "fetch failed: " + err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("remote returned %d", resp.StatusCode)})
		return
	}
	if resp.ContentLength > maxSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "attachment too large"})
		return
	}

	filename := filenameFromURL(target)
	mimeType := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = resources.SniffMIME(filename)
	}

	// An unknown remote size (no Content-Length) is charged at the
	// policy maximum for the quota reservation, same fallback
	// uploadResource uses for a chunked client upload.
	declaredSize := resp.ContentLength
	checkSize := declaredSize
	if checkSize < 0 {
		checkSize = maxSize
	}

	sanitizedName, err := policy.Validate(filename, checkSize, mimeType)
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

	entryID, err := s.reserveQuota(r.Context(), journal, sanitizedName, mimeType, checkSize)
	if err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "journal begin failed"})
		return
	}

	sha, storedSize, created, err := store.Put(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		_ = journal.Abort(entryID)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "store failed: " + err.Error()})
		return
	}
	if storedSize > maxSize {
		if created {
			_ = store.Delete(sha)
		}
		_ = journal.Abort(entryID)
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "attachment too large"})
		return
	}
	// A short read (connection dropped mid-transfer, or a remote that
	// lied about Content-Length) must never store a silently
	// truncated file — "ensure the file is not corrupted" (#107) is
	// exactly what this check is for; the dedicated /verify endpoint
	// covers corruption discovered later, after the blob is already
	// on disk.
	if declaredSize > 0 && storedSize != declaredSize {
		if created {
			_ = store.Delete(sha)
		}
		_ = journal.Abort(entryID)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("incomplete download: declared %d, got %d bytes", declaredSize, storedSize),
		})
		return
	}

	if storedSize != checkSize {
		if err := journal.SetSize(entryID, storedSize); err != nil {
			s.log.Warn("fetch resource: journal size update failed", "notebook", nb.Manifest.Slug, "entry", entryID, "err", err)
		}
	}
	if err := journal.Commit(entryID, sha); err != nil {
		s.log.Warn("fetch resource: journal commit failed", "notebook", nb.Manifest.Slug, "sha", sha, "err", err)
	}

	s.emit(r, nb, "resource.upload", sanitizedName, map[string]any{
		"sha": sha, "size": storedSize, "created": created, "source": "url",
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"sha":      sha,
		"filename": sanitizedName,
		"size":     storedSize,
		"mime":     mimeType,
		"created":  created,
	})
}

// parseFetchURL rejects anything that is not a plain http(s) URL with a
// host: no embedded credentials (a classic SSRF/credential-leak vector
// via "http://user:pass@host/"), no other scheme (file://, gopher://,
// etc. have no business being "an attachment URL").
func parseFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("url must be http(s)")
	}
	if u.Host == "" || u.User != nil {
		return nil, errors.New("invalid url")
	}
	u.Fragment = ""
	return u, nil
}

// filenameFromURL takes the last path segment as the attachment's
// filename, falling back to a generic name for a bare "/" or "" path
// (e.g. "https://example.com/" with no file component at all).
func filenameFromURL(u *url.URL) string {
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return "attachment"
	}
	if name, err := url.QueryUnescape(base); err == nil && name != "" {
		base = name
	}
	return base
}
