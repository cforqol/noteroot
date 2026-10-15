package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

// post uploads a raw body to the resources endpoint.
func postResource(t *testing.T, srv *Server, slug, filename, mime string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	target := "/n/" + slug + "/resources?filename=" + filename
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// postMultipart uploads via multipart/form-data.
func postMultipart(t *testing.T, srv *Server, slug, fieldName, filename string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/n/"+slug+"/resources", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
}

// resourceFixture builds a registry with one notebook "work".
func resourceFixture(t *testing.T) *notebooks.Registry {
	t.Helper()
	return notesFixture(t, map[string]string{"a.md": "x"})
}

func TestUpload_rawBody(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := []byte("hello blob")
	rec := postResource(t, srv, "work", "hello.txt", "text/plain", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string `json:"id"`
		SHA     string `json:"sha"`
		created bool   `json:"created"`
		Size    int64  `json:"size"`
		MIME    string `json:"mime"`
	}
	// Use map for created field (lowercase in JSON).
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	sha, _ := raw["sha"].(string)
	if sha == "" {
		t.Fatal("no sha in response")
	}
	if raw["size"].(float64) != float64(len(content)) {
		t.Errorf("size = %v, want %d", raw["size"], len(content))
	}
	if raw["mime"] != "text/plain" {
		t.Errorf("mime = %v", raw["mime"])
	}
	_ = resp
}

func TestUpload_multipart(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := []byte("multipart blob")
	rec := postMultipart(t, srv, "work", "file", "photo.jpg", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["filename"] != "photo.jpg" {
		t.Errorf("filename = %v", raw["filename"])
	}
}

func TestUpload_missingFilename(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources", bytes.NewReader([]byte("x")))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestUpload_rejectsBadMIME(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	rec := postResource(t, srv, "work", "evil.exe", "application/x-msdownload", []byte("MZ"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestUpload_rejectsTooLarge(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	// 51 MiB > DefaultMaxSize (50 MiB)
	big := make([]byte, 51<<20)
	rec := postResource(t, srv, "work", "big.bin", "application/octet-stream", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestUpload_dedupHit(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := []byte("same content")
	rec1 := postResource(t, srv, "work", "a.txt", "text/plain", content)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first upload: %d", rec1.Code)
	}
	rec2 := postResource(t, srv, "work", "b.txt", "text/plain", content)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second upload: %d", rec2.Code)
	}
	var raw1, raw2 map[string]any
	decodeJSON(t, rec1, &raw1)
	decodeJSON(t, rec2, &raw2)
	if raw1["sha"] != raw2["sha"] {
		t.Errorf("sha mismatch on dedup: %v vs %v", raw1["sha"], raw2["sha"])
	}
	if raw2["created"] != false {
		t.Errorf("created = %v, want false on dedup", raw2["created"])
	}
}

func TestUpload_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	rec := postResource(t, srv, "nope", "a.txt", "text/plain", []byte("x"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGetResource_servesBlob(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := []byte("serve me")
	up := postResource(t, srv, "work", "s.txt", "text/plain", content)
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("ETag") != `"`+sha+`"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	got, _ := io.ReadAll(rec.Body)
	if !bytes.Equal(got, content) {
		t.Errorf("body = %q, want %q", got, content)
	}
}

func TestGetResource_setsContentTypeFromJournal(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	up := postResource(t, srv, "work", "p.png", "image/png", []byte("\x89PNG"))
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

func TestGetResource_notFound(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	fake := strings.Repeat("a", 64)
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+fake, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListResources(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	postResource(t, srv, "work", "a.txt", "text/plain", []byte("a"))
	postResource(t, srv, "work", "b.txt", "text/plain", []byte("b"))

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Resources []map[string]any `json:"resources"`
	}
	decodeJSON(t, rec, &body)
	if len(body.Resources) != 2 {
		t.Errorf("got %d resources, want 2", len(body.Resources))
	}
}

func TestListResources_empty(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	var body struct {
		Resources []map[string]any `json:"resources"`
	}
	decodeJSON(t, rec, &body)
	if body.Resources == nil {
		t.Error("resources = nil, want empty slice")
	}
}

func TestResourceStats(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	postResource(t, srv, "work", "a.txt", "text/plain", []byte("a"))

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Journal map[string]int `json:"journal"`
	}
	decodeJSON(t, rec, &body)
	if body.Journal["committed"] != 1 {
		t.Errorf("committed = %d, want 1", body.Journal["committed"])
	}
}

func TestDeleteResource(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	up := postResource(t, srv, "work", "a.txt", "text/plain", []byte("a"))
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/n/work/resources/"+sha, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// Should be gone now.
	getReq := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	getRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", getRec.Code)
	}
}

func TestDeleteResource_notFound(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	fake := strings.Repeat("b", 64)
	req := httptest.NewRequest(http.MethodDelete, "/n/work/resources/"+fake, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUploadCSRF_rejectedWithoutOrigin(t *testing.T) {
	// With AuthForward + PublicOrigin set, mutating requests need a
	// matching Origin. Uploads go through mutating(), so CSRF applies.
	srv := newTestServer(t, config.AuthForward, resourceFixture(t))
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources?filename=a.txt", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Auth-User", "alice")
	// No Origin header → bare API client, passes CSRF content-type but
	// AuthForward requires X-Auth-User which we set. Actually bare clients
	// (no Origin) are allowed by design. So this should succeed.
	// To test CSRF rejection, send a cross-origin Origin header instead.
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
