package httpapi

// proxy_integrations.go wires noteroot to four optional external services:
//
//   - GET  /api/v1/integrations/status         — reports which integrations are live
//   - GET  /api/v1/integrations/s3/test        — verifies S3 bucket connectivity
//   - POST /api/v1/integrations/karakeep/archive — archives a URL via Karakeep
//   - POST /api/v1/integrations/slink/upload    — uploads a file to Slink
//
// All are behind the same auth middleware as the rest of /api/v1, but the
// external credentials never leave the server: the client sends only the raw
// URL or binary payload and receives a hosted URL back.
//
// Karakeep (https://karakeep.app, formerly Hoarder) REST API:
//   POST <base>/api/v1/bookmarks  {"url": "..."}
//   Authorization: Bearer <key>
//   → {"id":"…", …}  bookmark accessible at <base>/bookmarks/<id>
//
// Slink generic upload API (self-hosted file-host):
//   POST <base>/upload  multipart "file" field
//   Authorization: Bearer <key>
//   → {"url":"https://…"}

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"noteroot/internal/resources"
)

const (
	maxArchiveBody = 4 << 10  // 4 KiB — just a URL
	maxSlinkBody   = 50 << 20 // 50 MiB — same as resource upload limit
	proxyTimeout   = 30 * time.Second
)

var proxyClient = &http.Client{Timeout: proxyTimeout}

// integrationStatus is the response shape for GET /api/v1/integrations/status.
type integrationStatus struct {
	Karakeep bool `json:"karakeep"`
	Slink    bool `json:"slink"`
	S3       bool `json:"s3"`
}

// integrationStatus returns which external integrations are configured.
// The client uses this to decide whether to enable paste-archiving, Slink
// upload, and S3 object storage.
func (s *Server) integrationStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, integrationStatus{
		Karakeep: s.cfg.KarakeepURL != "" && s.cfg.KarakeepKey != "",
		Slink:    s.cfg.SlinkURL != "" && s.cfg.SlinkKey != "",
		S3:       s.cfg.S3Enabled(),
	})
}

// testS3 handles GET /api/v1/integrations/s3/test — verifies that the
// configured S3 bucket is reachable. Returns {"ok":true} on success or
// {"ok":false,"error":"…"} when S3 is not configured or unreachable.
func (s *Server) testS3(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.S3Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "S3 not configured"})
		return
	}
	store, err := resources.NewS3Store(resources.S3Config{
		Endpoint:  s.cfg.S3Endpoint,
		Bucket:    s.cfg.S3Bucket,
		Region:    s.cfg.S3Region,
		AccessKey: s.cfg.S3AccessKey,
		SecretKey: s.cfg.S3SecretKey,
		PathStyle: s.cfg.S3PathStyle,
		SSE:       s.cfg.S3SSE,
		SSEKeyID:  s.cfg.S3SSEKeyID,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// archiveWithKarakeep proxies a bookmark creation request to Karakeep.
//
// Request body:  {"url": "https://example.com"}
// Response body: {"bookmarkUrl": "https://karakeep.example.com/bookmarks/abc123"}
func (s *Server) archiveWithKarakeep(w http.ResponseWriter, r *http.Request) {
	if s.cfg.KarakeepURL == "" || s.cfg.KarakeepKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Karakeep not configured",
		})
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxArchiveBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read error"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url must be http(s)"})
		return
	}

	base := strings.TrimRight(s.cfg.KarakeepURL, "/")
	apiURL := base + "/api/v1/bookmarks"

	payload, _ := json.Marshal(map[string]string{"url": req.URL})
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy request failed"})
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Authorization", "Bearer "+s.cfg.KarakeepKey)

	resp, err := proxyClient.Do(upstream)
	if err != nil {
		s.log.Warn("karakeep proxy: upstream error", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "karakeep unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		s.log.Warn("karakeep proxy: upstream status", "status", resp.StatusCode)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("karakeep returned %d", resp.StatusCode),
		})
		return
	}

	// Extract bookmark ID from the response and build the web UI URL.
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil || result.ID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "unexpected karakeep response"})
		return
	}

	bookmarkURL := base + "/bookmarks/" + result.ID
	writeJSON(w, http.StatusOK, map[string]string{"bookmarkUrl": bookmarkURL})
}

// uploadToSlink proxies a file upload to the configured Slink instance.
//
// Request:  multipart/form-data with a "file" field.
// Response: {"url": "https://slink.example.com/files/abc123/image.png"}
func (s *Server) uploadToSlink(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SlinkURL == "" || s.cfg.SlinkKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Slink not configured",
		})
		return
	}

	if err := r.ParseMultipartForm(maxSlinkBody); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "multipart parse failed"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field required"})
		return
	}
	defer func() { _ = file.Close() }()

	// Re-encode as multipart for the upstream request.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", hdr.Filename)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "multipart build failed"})
		return
	}
	if _, err := io.Copy(part, file); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	_ = mw.Close()

	base := strings.TrimRight(s.cfg.SlinkURL, "/")
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/upload", &buf)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy request failed"})
		return
	}
	upstream.Header.Set("Content-Type", mw.FormDataContentType())
	upstream.Header.Set("Authorization", "Bearer "+s.cfg.SlinkKey)

	resp, err := proxyClient.Do(upstream)
	if err != nil {
		s.log.Warn("slink proxy: upstream error", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "slink unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		s.log.Warn("slink proxy: upstream status", "status", resp.StatusCode)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("slink returned %d", resp.StatusCode),
		})
		return
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil || result.URL == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "unexpected slink response"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"url": result.URL})
}
