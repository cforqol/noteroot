package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
)

// transformCacheDir returns the per-notebook directory for cached
// transforms. It is created on demand.
func transformCacheDir(nb *notebooks.Notebook) string {
	return filepath.Join(nb.Layout.Cache(), "transforms")
}

// transformCacheKey builds a deterministic cache filename for a source
// blob and transform parameters. The key includes everything that
// affects the output so parameter changes never serve stale bytes.
func transformCacheKey(sha, kind string, params ...string) string {
	parts := append([]string{kind}, params...)
	return sha + "_" + strings.Join(parts, "_") + ".jpg"
}

// cachedTransform returns the cached JPEG if present, or runs `gen` and
// stores the result. It is safe against concurrent callers: the write
// uses temp+rename so readers never see a partial file.
func cachedTransform(ctx context.Context, dir, key string, gen func() ([]byte, error)) ([]byte, bool, error) {
	path := filepath.Join(dir, key)
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return b, true, nil
	}
	out, err := gen()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Cache write failure is non-fatal; return the bytes anyway.
		return out, false, nil
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return out, false, nil
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return out, false, nil
	}
	_ = tmp.Close()
	_ = os.Rename(tmpName, path)
	return out, false, nil
}

// thumbResource serves GET /n/{slug}/resources/{sha}/thumb?w=&h=&kind=
//
// Produces a JPEG thumbnail of an image blob, or a poster frame for a
// video blob. Output is cached on disk keyed by (sha, w, h, kind).
func (s *Server) thumbResource(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))

	maxW, _ := strconv.Atoi(r.URL.Query().Get("w"))
	maxH, _ := strconv.Atoi(r.URL.Query().Get("h"))
	if maxW <= 0 {
		maxW = 400
	}
	if maxH <= 0 {
		maxH = 400
	}
	// Cap to the transformer's maximum.
	if maxW > 4096 {
		maxW = 4096
	}
	if maxH > 4096 {
		maxH = 4096
	}

	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	// Determine source kind from the journal MIME. A direct lookup, not
	// List: List is capped, so older blobs would read as "other" and 415.
	mime := ""
	if m, err := journal.MIMEBySHA(sha); err == nil {
		mime = m[sha]
	}
	kind := resources.DetectKind(mime)

	cacheDir := transformCacheDir(nb)
	cache := transformCacheKey(sha, "thumb", strconv.Itoa(maxW), strconv.Itoa(maxH))
	if kind == "video" {
		cache = transformCacheKey(sha, "poster")
	}

	// A cached transform is served before the source blob is consulted:
	// the pixels on disk stay valid even when the blob itself is gone
	// (reclaimed by an older sweep, or a vault restored without its
	// store), and a picture must keep rendering in read mode.
	if b, err := os.ReadFile(filepath.Join(cacheDir, cache)); err == nil && len(b) > 0 {
		writeTransformed(w, b, cache, true)
		return
	}

	if !store.Has(sha) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// A thumbnail is only an optimisation for images: without a working
	// transformer (ffmpeg missing, as in the stock container image) or
	// when it fails, serve the original so the picture still shows.
	serveOriginal := func() { s.getResource(w, r) }

	tr := resources.NewFFmpegTransformer()
	if !tr.Available() {
		if kind == "image" {
			serveOriginal()
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transformer unavailable"})
		return
	}

	// Resolve source to a temp file (store.Get gives a reader; ffmpeg needs a path).
	srcPath, srcCleanup, err := materializeSource(store, sha)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "source unavailable"})
		return
	}
	defer srcCleanup()

	var (
		out []byte
		hit bool
	)

	switch kind {
	case "image":
		out, hit, err = cachedTransform(r.Context(), cacheDir, cache, func() ([]byte, error) {
			b, _, _, e := tr.ResizeImage(r.Context(), srcPath, maxW, maxH)
			return b, e
		})
	case "video":
		out, hit, err = cachedTransform(r.Context(), cacheDir, cache, func() ([]byte, error) {
			return tr.VideoPoster(r.Context(), srcPath, 0)
		})
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"error": fmt.Sprintf("no transform for mime %q", mime),
		})
		return
	}

	if err != nil {
		if kind == "image" {
			s.log.Warn("thumbnail failed; serving original", "sha", sha, "err", err)
			serveOriginal()
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transform failed: " + err.Error()})
		return
	}

	writeTransformed(w, out, cache, hit)
}

// writeTransformed writes a cached-or-fresh JPEG transform.
func writeTransformed(w http.ResponseWriter, body []byte, cache string, hit bool) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+cache+`"`)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// posterResource serves GET /n/{slug}/resources/{sha}/poster — video
// poster frame only (alias for thumb with video kind).
func (s *Server) posterResource(w http.ResponseWriter, r *http.Request) {
	s.thumbResource(w, r)
}

// materializeSource copies a blob to a temp file so ffmpeg can seek it.
// Returns the path and a cleanup func. Source size is capped at 100 MiB.
func materializeSource(store resources.Store, sha string) (string, func(), error) {
	rc, err := store.Get(sha)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp("", "nr-src-*")
	if err != nil {
		return "", nil, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	const maxSrc = 100 << 20
	n, err := io.Copy(tmp, io.LimitReader(rc, maxSrc))
	_ = tmp.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if n >= maxSrc {
		cleanup()
		return "", nil, errors.New("source too large")
	}
	return tmpName, cleanup, nil
}
