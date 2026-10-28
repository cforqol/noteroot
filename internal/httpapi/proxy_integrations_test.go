package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

func newServerWithInteg(t *testing.T, karakeepSrv, slinkSrv *httptest.Server) *Server {
	t.Helper()
	reg, problems := notebooks.Open(t.TempDir())
	if len(problems) != 0 {
		t.Fatalf("notebooks.Open: %v", problems)
	}
	cfg := config.Config{
		DataDir:    filepath.Join(t.TempDir(), "data"),
		ListenAddr: ":0",
		AuthMode:   config.AuthNone,
	}
	if karakeepSrv != nil {
		cfg.KarakeepURL = karakeepSrv.URL
		cfg.KarakeepKey = "kk-test-key"
	}
	if slinkSrv != nil {
		cfg.SlinkURL = slinkSrv.URL
		cfg.SlinkKey = "sl-test-key"
	}
	return New(cfg, newTestLogger(), reg)
}

func integGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return get(t, s, path, nil)
}

func integPostJSON(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	return w
}

func integPostMultipart(t *testing.T, s *Server, path, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", filename)
	_, _ = io.WriteString(part, content)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	return w
}

// --- integration status ---

func TestIntegrationStatus_nothingConfigured(t *testing.T) {
	s := newServerWithInteg(t, nil, nil)
	w := integGet(t, s, "/api/v1/integrations/status")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var st integrationStatus
	decodeJSON(t, w, &st)
	if st.Karakeep || st.Slink || st.S3 {
		t.Errorf("expected all false, got karakeep=%v slink=%v s3=%v", st.Karakeep, st.Slink, st.S3)
	}
}

func TestIntegrationStatus_bothConfigured(t *testing.T) {
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer kk.Close()
	sl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer sl.Close()

	s := newServerWithInteg(t, kk, sl)
	w := integGet(t, s, "/api/v1/integrations/status")
	var st integrationStatus
	decodeJSON(t, w, &st)
	if !st.Karakeep || !st.Slink {
		t.Errorf("expected both true, got karakeep=%v slink=%v", st.Karakeep, st.Slink)
	}
	if st.S3 {
		t.Errorf("S3 should be false when not configured")
	}
}

// --- karakeep proxy ---

func TestKarakeepArchive_success(t *testing.T) {
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kk-test-key" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["url"] == "" {
			http.Error(w, "no url", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"bm123","url":%q}`, body["url"])
	}))
	defer kk.Close()

	s := newServerWithInteg(t, kk, nil)
	w := integPostJSON(t, s, "/api/v1/integrations/karakeep/archive",
		map[string]string{"url": "https://example.com/page"})

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]string
	decodeJSON(t, w, &resp)
	want := kk.URL + "/bookmarks/bm123"
	if resp["bookmarkUrl"] != want {
		t.Errorf("bookmarkUrl: want %q, got %q", want, resp["bookmarkUrl"])
	}
}

func TestKarakeepArchive_notConfigured(t *testing.T) {
	s := newServerWithInteg(t, nil, nil)
	w := integPostJSON(t, s, "/api/v1/integrations/karakeep/archive",
		map[string]string{"url": "https://example.com"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}

func TestKarakeepArchive_invalidURL(t *testing.T) {
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer kk.Close()
	s := newServerWithInteg(t, kk, nil)

	w := integPostJSON(t, s, "/api/v1/integrations/karakeep/archive",
		map[string]string{"url": "ftp://bad"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ftp: want 400, got %d: %s", w.Code, w.Body)
	}

	w = integPostJSON(t, s, "/api/v1/integrations/karakeep/archive", map[string]string{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty: want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestKarakeepArchive_upstreamError(t *testing.T) {
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer kk.Close()
	s := newServerWithInteg(t, kk, nil)
	w := integPostJSON(t, s, "/api/v1/integrations/karakeep/archive",
		map[string]string{"url": "https://example.com"})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
}

// --- slink proxy ---

func TestSlinkUpload_success(t *testing.T) {
	sl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sl-test-key" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil || hdr.Filename == "" {
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		_ = f.Close()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"url":"https://slink.example.com/f/%s"}`, hdr.Filename)
	}))
	defer sl.Close()

	s := newServerWithInteg(t, nil, sl)
	w := integPostMultipart(t, s, "/api/v1/integrations/slink/upload", "image.png", "PNGDATA")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]string
	decodeJSON(t, w, &resp)
	if !strings.HasPrefix(resp["url"], "https://slink.example.com/") {
		t.Errorf("unexpected url: %q", resp["url"])
	}
}

func TestSlinkUpload_notConfigured(t *testing.T) {
	s := newServerWithInteg(t, nil, nil)
	w := integPostMultipart(t, s, "/api/v1/integrations/slink/upload", "img.png", "data")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}

func TestSlinkUpload_upstreamError(t *testing.T) {
	sl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusInternalServerError)
	}))
	defer sl.Close()
	s := newServerWithInteg(t, nil, sl)
	w := integPostMultipart(t, s, "/api/v1/integrations/slink/upload", "img.png", "data")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
}

// newServerWithS3 builds a server pointing at a fake S3 endpoint.
func newServerWithS3(t *testing.T, s3Srv *httptest.Server) *Server {
	t.Helper()
	reg, problems := notebooks.Open(t.TempDir())
	if len(problems) != 0 {
		t.Fatalf("notebooks.Open: %v", problems)
	}
	cfg := config.Config{
		DataDir:    filepath.Join(t.TempDir(), "data"),
		ListenAddr: ":0",
		AuthMode:   config.AuthNone,
	}
	if s3Srv != nil {
		cfg.S3Endpoint = s3Srv.URL
		cfg.S3Bucket = "test-bucket"
		cfg.S3Region = "us-east-1"
		cfg.S3AccessKey = "access"
		cfg.S3SecretKey = "secret"
		cfg.S3PathStyle = true
	}
	return New(cfg, newTestLogger(), reg)
}

// --- S3 test endpoint ---

func TestS3Test_notConfigured(t *testing.T) {
	s := newServerWithInteg(t, nil, nil)
	w := integGet(t, s, "/api/v1/integrations/s3/test")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	decodeJSON(t, w, &resp)
	if ok, _ := resp["ok"].(bool); ok {
		t.Error("ok should be false when S3 not configured")
	}
}

func TestS3Test_reachable(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 200 for HEAD /test-bucket/
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	s := newServerWithS3(t, fake)
	w := integGet(t, s, "/api/v1/integrations/s3/test")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	decodeJSON(t, w, &resp)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("ok should be true, got %v (error: %v)", resp["ok"], resp["error"])
	}
}

func TestS3Test_serverError(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer fake.Close()

	s := newServerWithS3(t, fake)
	w := integGet(t, s, "/api/v1/integrations/s3/test")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	decodeJSON(t, w, &resp)
	if ok, _ := resp["ok"].(bool); ok {
		t.Error("ok should be false when S3 returns 5xx")
	}
	if resp["error"] == "" {
		t.Error("error message should be present")
	}
}

func TestIntegrationStatus_S3Configured(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer fake.Close()

	s := newServerWithS3(t, fake)
	w := integGet(t, s, "/api/v1/integrations/status")
	var st integrationStatus
	decodeJSON(t, w, &st)
	if !st.S3 {
		t.Error("S3 should be true when configured")
	}
}
