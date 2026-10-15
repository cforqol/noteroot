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

	if !store.Has(sha) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// Determine source kind from journal MIME (fall back to extension).
	mime := ""
	if entries, err := journal.List(resources.StateCommitted, 0); err == nil {
		for _, e := range entries {
			if e.SHA == sha {
				mime = e.MIME
				break
			}
		}
	}

	tr := resources.NewFFmpegTransformer()
	if !tr.Available() {
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

	kind := resources.DetectKind(mime)
	cacheDir := transformCacheDir(nb)

	var (
		out   []byte
		hit   bool
		cache string
	)

	switch kind {
	case "image":
		cache = transformCacheKey(sha, "thumb", strconv.Itoa(maxW), strconv.Itoa(maxH))
		out, hit, err = cachedTransform(r.Context(), cacheDir, cache, func() ([]byte, error) {
			b, _, _, e := tr.ResizeImage(r.Context(), srcPath, maxW, maxH)
			return b, e
		})
	case "video":
		cache = transformCacheKey(sha, "poster")
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transform failed: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+cache+`"`)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// posterResource serves GET /n/{slug}/resources/{sha}/poster — video
// poster frame only (alias for thumb with video kind).
func (s *Server) posterResource(w http.ResponseWriter, r *http.Request) {
	s.thumbResource(w, r)
}

// materializeSource copies a blob to a temp file so ffmpeg can seek it.
// Returns the path and a cleanup func. Source size is capped at 100 MiB.
func materializeSource(store *resources.LocalStore, sha string) (string, func(), error) {
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
