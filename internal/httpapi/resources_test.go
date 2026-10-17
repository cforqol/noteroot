package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// A chunked upload (Content-Length unknown, -1) used to journal -1
// bytes permanently under-counting — and even subtracting — the quota
// total. The journal must record the real stored size once known.
func TestUpload_unknownSize_recordsRealStoredBytes(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := []byte("chunked-blob-content") // 19 bytes
	target := "/n/work/resources?filename=chunked.txt"
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(content))
	req.ContentLength = -1 // simulate chunked transfer encoding
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	stats := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	srec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(srec, stats)
	var resp map[string]any
	decodeJSON(t, srec, &resp)
	if used, _ := resp["usedBytes"].(float64); used != float64(len(content)) {
		t.Errorf("usedBytes = %v, want %d (real stored size)", resp["usedBytes"], len(content))
	}
}

// Unknown-size uploads are conservatively charged the policy maximum
// at the quota check: a chunked transfer cannot be allowed to probe
// free space byte-by-byte.
func TestUpload_unknownSize_chargedPolicyMax(t *testing.T) {
	srv := newQuotaServer(t, 1000) // << 50 MiB policy max
	target := "/n/work/resources?filename=chunked.txt"
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte("small")))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507 (unknown size charged at policy max)", rec.Code)
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

// --- Transform endpoints (stage 6) ---

func TestThumb_imageThumbnail(t *testing.T) {
	hasFFmpegForTest(t)
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))

	// Upload a real image via ffmpeg-generated PNG.
	img := generateTestPNG(t, 640, 480)
	up := postResource(t, srv, "work", "photo.png", "image/png", img)
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/thumb?w=320&h=240", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	body := rec.Body.Bytes()
	if len(body) < 3 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Errorf("not a JPEG response")
	}
}

func TestThumb_cacheHit(t *testing.T) {
	hasFFmpegForTest(t)
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	img := generateTestPNG(t, 320, 240)
	up := postResource(t, srv, "work", "p.png", "image/png", img)
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	url := "/n/work/resources/" + sha + "/thumb?w=100&h=100"
	// First request: MISS
	req1 := httptest.NewRequest(http.MethodGet, url, nil)
	rec1 := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec1, req1)
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Errorf("first X-Cache = %q, want MISS", rec1.Header().Get("X-Cache"))
	}
	// Second request: HIT
	req2 := httptest.NewRequest(http.MethodGet, url, nil)
	rec2 := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Errorf("second X-Cache = %q, want HIT", rec2.Header().Get("X-Cache"))
	}
	// Same body both times.
	if !bytes.Equal(rec1.Body.Bytes(), rec2.Body.Bytes()) {
		t.Error("cached body differs from fresh body")
	}
}

func TestThumb_videoPoster(t *testing.T) {
	hasFFmpegForTest(t)
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))

	// Generate a tiny test video.
	vid := generateTestVideo(t)
	up := postResource(t, srv, "work", "clip.mp4", "video/mp4", vid)
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/poster", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.Bytes()
	if len(body) < 3 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Error("poster is not JPEG")
	}
}

func TestThumb_notFound(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	fake := strings.Repeat("a", 64)
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+fake+"/thumb", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestThumb_unsupportedMIME(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	up := postResource(t, srv, "work", "doc.pdf", "application/pdf", []byte("%PDF-1.4"))
	var raw map[string]any
	decodeJSON(t, up, &raw)
	sha := raw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/thumb", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

// --- Helpers for transform tests ---

func hasFFmpegForTest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
}

func generateTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "test.png")
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i",
		fmt.Sprintf("color=c=red:s=%dx%d", w, h),
		"-frames:v", "1", out,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, output)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func generateTestVideo(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "test.mp4")
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=160x120:rate=10",
		"-pix_fmt", "yuv420p", out,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, output)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- Presigned endpoints (stage 7) ---

func TestPresignUpload_directWhenNoS3(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	body := `{"sha":"` + strings.Repeat("a", 64) + `","size":100,"filename":"x.png","mime":"image/png"}`
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["direct"] != true {
		t.Errorf("direct = %v, want true (no S3 configured)", raw["direct"])
	}
}

func TestPresignUpload_existsWhenBlobPresent(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	// Upload a blob first.
	up := postResource(t, srv, "work", "x.txt", "text/plain", []byte("content"))
	var upRaw map[string]any
	decodeJSON(t, up, &upRaw)
	sha := upRaw["sha"].(string)

	body := fmt.Sprintf(`{"sha":%q,"size":7,"filename":"x.txt","mime":"text/plain"}`, sha)
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["exists"] != true {
		t.Errorf("exists = %v, want true", raw["exists"])
	}
	if raw["sha"] != sha {
		t.Errorf("sha = %v, want %q", raw["sha"], sha)
	}
}

func TestPresignUpload_rejectsBadMIME(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	body := `{"sha":"` + strings.Repeat("b", 64) + `","size":100,"filename":"evil.exe","mime":"application/x-msdownload"}`
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestPresignUpload_missingFields(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestConfirmUpload_blobMissing(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	body := `{"entryID":"nonexistent","sha":"` + strings.Repeat("c", 64) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestConfirmUpload_success(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	// Upload blob, then begin a journal entry via presign (direct mode
	// won't create one, so create one manually through the upload flow).
	up := postResource(t, srv, "work", "y.txt", "text/plain", []byte("hello"))
	var upRaw map[string]any
	decodeJSON(t, up, &upRaw)
	sha := upRaw["sha"].(string)
	entryID := upRaw["id"].(string)

	body := fmt.Sprintf(`{"entryID":%q,"sha":%q}`, entryID, sha)
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	// Already committed by upload — confirm returns conflict (bad state).
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already committed)", rec.Code)
	}
}

func TestPresignRead_directWhenNoS3(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	up := postResource(t, srv, "work", "z.txt", "text/plain", []byte("read"))
	var upRaw map[string]any
	decodeJSON(t, up, &upRaw)
	sha := upRaw["sha"].(string)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/presign-read", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["direct"] != true {
		t.Errorf("direct = %v, want true", raw["direct"])
	}
	if raw["url"] != "/n/work/resources/"+sha {
		t.Errorf("url = %v", raw["url"])
	}
}

func TestPresignRead_notFound(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	fake := strings.Repeat("d", 64)
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+fake+"/presign-read", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Presign short-circuits (non-S3 "direct" and dedup "exists") store
// nothing, so they must not leave journal rows behind — the journal
// would otherwise fill with aborted entries on every upload.
func TestPresign_shortCircuitsLeaveNoJournalRows(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	journalLen := func() int {
		t.Helper()
		rec := get(t, srv, "/n/work/resources", nil)
		var body struct {
			Resources []map[string]any `json:"resources"`
		}
		decodeJSON(t, rec, &body)
		return len(body.Resources)
	}
	presign := func(sha string) map[string]any {
		t.Helper()
		b := `{"sha":"` + sha + `","size":5,"filename":"p.txt","mime":"text/plain"}`
		req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("presign = %d, %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		decodeJSON(t, rec, &out)
		return out
	}

	// Unknown blob on a local store → direct upload, no row.
	unknown := strings.Repeat("ab", 32)
	if out := presign(unknown); out["direct"] != true {
		t.Fatalf("presign = %v, want direct", out)
	}
	if n := journalLen(); n != 0 {
		t.Fatalf("journal rows after direct presign = %d, want 0", n)
	}

	// Upload for real (one committed row), then presign the same blob.
	rec := postResource(t, srv, "work", "p.txt", "text/plain", []byte("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d, %s", rec.Code, rec.Body.String())
	}
	var up struct {
		SHA string `json:"sha"`
	}
	decodeJSON(t, rec, &up)
	if out := presign(up.SHA); out["exists"] != true {
		t.Fatalf("presign of stored blob = %v, want exists", out)
	}
	if n := journalLen(); n != 1 {
		t.Fatalf("journal rows after dedup presign = %d, want 1 (the upload)", n)
	}
}
