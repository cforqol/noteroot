package httpapi

// Integration tests for stage 7: the HTTP API wired to an S3-compatible
// backend (in-process fake S3). Covers the full presigned upload flow
// (presign → direct PUT → confirm → serve), server-side uploads landing
// in remote storage, presigned reads, policy enforcement, journal state
// transitions, thumbs, and deletion.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/state"
)

// --- fake S3 (httpapi flavour) ----------------------------------------

type s3Fake struct {
	mu         sync.Mutex
	objects    map[string][]byte
	puts       int
	failDelete bool
	srv        *httptest.Server
}

func newS3Fake(t *testing.T) *s3Fake {
	t.Helper()
	f := &s3Fake{objects: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		bucket, key, ok := strings.Cut(p, "/")
		if !ok || bucket != "testbucket" || key == "" {
			http.Error(w, "no key", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			f.objects[key] = body
			f.puts++
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			obj, found := f.objects[key]
			if !found {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			_, _ = w.Write(obj)
		case http.MethodHead:
			obj, found := f.objects[key]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj)))
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if f.failDelete {
				http.Error(w, "injected delete failure", http.StatusInternalServerError)
				return
			}
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *s3Fake) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *s3Fake) get(key string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key]
}

// --- server construction ----------------------------------------------

// newS3Server builds a test server whose resources store is the given
// fake S3. Extra mutations (e.g. PublicOrigin for CSRF tests) go through
// the returned config pointer before New is called — hence the opts func.
func newS3Server(t *testing.T, reg *notebooks.Registry, f *s3Fake, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Config{
		DataDir:    t.TempDir(),
		ListenAddr: ":0",
		LogLevel:   "info",
		AuthMode:   config.AuthNone,

		S3Endpoint:  f.srv.URL,
		S3Bucket:    "testbucket",
		S3Region:    "us-east-1",
		S3AccessKey: "AKIDEXAMPLE",
		S3SecretKey: "SECRETKEY",
		S3PathStyle: true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg, newTestLogger(), reg)
}

// s3Presign asks the server for a direct-upload URL.
func s3Presign(t *testing.T, srv *Server, sha string, size int64, filename, mime string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"sha":%q,"size":%d,"filename":%q,"mime":%q}`, sha, size, filename, mime)
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// putRaw performs the direct-to-S3 upload the client would do.
func putRaw(t *testing.T, uploadURL string, body []byte, mime string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("direct PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("direct PUT status %d: %s", resp.StatusCode, b)
	}
}

// s3Confirm posts the journal confirm after a direct upload.
func s3Confirm(t *testing.T, srv *Server, entryID, sha string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"entryID":%q,"sha":%q}`, entryID, sha)
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// journalCounts opens the notebook's journal and returns counts by state.
func journalCounts(t *testing.T, reg *notebooks.Registry) map[string]int {
	t.Helper()
	nb, ok := reg.Get("work")
	if !ok {
		t.Fatal("no notebook work")
	}
	db, _, err := state.Open(context.Background(), nb.Layout.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	j := resources.NewJournal(db.DB)
	counts, err := j.CountByState()
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// --- tests -------------------------------------------------------------

// TestS3_fullPresignFlow exercises the stage-7 happy path end to end:
// presign → browser-style direct PUT → confirm → serve, asserting the
// bytes only ever touched the remote store (fake S3 has the object and
// the server streams it back from there).
func TestS3_fullPresignFlow(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("direct upload payload")
	sha := sha256HexOf(content)

	rec := s3Presign(t, srv, sha, int64(len(content)), "note.txt", "text/plain")
	if rec.Code != http.StatusOK {
		t.Fatalf("presign status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var presign map[string]any
	decodeJSON(t, rec, &presign)
	if presign["direct"] != false {
		t.Fatalf("direct = %v, want false", presign["direct"])
	}
	uploadURL, _ := presign["uploadURL"].(string)
	entryID, _ := presign["entryID"].(string)
	if uploadURL == "" || entryID == "" {
		t.Fatalf("missing uploadURL/entryID: %v", presign)
	}
	if got, _ := presign["expiresIn"].(float64); got != 900 {
		t.Errorf("expiresIn = %v, want 900", presign["expiresIn"])
	}

	putRaw(t, uploadURL, content, "text/plain")
	if !f.has(sha) {
		t.Fatal("object missing from remote store after direct PUT")
	}

	conf := s3Confirm(t, srv, entryID, sha)
	if conf.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", conf.Code, conf.Body.String())
	}

	// Serve streams from the remote store.
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	got := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("serve status = %d", got.Code)
	}
	if got.Body.String() != string(content) {
		t.Errorf("served %q, want %q", got.Body.String(), content)
	}
	if ct := got.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	// Journal committed.
	counts := journalCounts(t, reg)
	if counts[resources.StateCommitted] != 1 {
		t.Errorf("journal committed = %d, want 1 (%v)", counts[resources.StateCommitted], counts)
	}
}

// TestS3_confirmWithoutUploadAborts verifies the crash-recovery path:
// presign opens a journal entry, but if the direct PUT never happens
// the confirm must fail and the entry must end up aborted, not pending.
func TestS3_confirmWithoutUploadAborts(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	sha := sha256HexOf([]byte("never uploaded"))
	rec := s3Presign(t, srv, sha, 5, "x.txt", "text/plain")
	var presign map[string]any
	decodeJSON(t, rec, &presign)
	entryID, _ := presign["entryID"].(string)

	conf := s3Confirm(t, srv, entryID, sha)
	if conf.Code != http.StatusConflict {
		t.Fatalf("confirm status = %d, want 409", conf.Code)
	}
	if f.has(sha) {
		t.Error("object unexpectedly present")
	}
	counts := journalCounts(t, reg)
	if counts[resources.StateAborted] != 1 {
		t.Errorf("journal aborted = %d, want 1 (%v)", counts[resources.StateAborted], counts)
	}
}

// TestS3_presignDedupAfterUpload: once the blob exists remotely, a second
// presign request short-circuits with exists=true (no uploadURL issued).
func TestS3_presignDedupAfterUpload(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("dedup me")
	sha := sha256HexOf(content)

	rec := s3Presign(t, srv, sha, int64(len(content)), "d.txt", "text/plain")
	var presign map[string]any
	decodeJSON(t, rec, &presign)
	putRaw(t, presign["uploadURL"].(string), content, "text/plain")
	if c := s3Confirm(t, srv, presign["entryID"].(string), sha); c.Code != http.StatusOK {
		t.Fatalf("confirm = %d", c.Code)
	}

	putsAfterFirst := f.puts
	rec2 := s3Presign(t, srv, sha, int64(len(content)), "d.txt", "text/plain")
	var again map[string]any
	decodeJSON(t, rec2, &again)
	if again["exists"] != true {
		t.Errorf("exists = %v, want true", again["exists"])
	}
	if _, hasURL := again["uploadURL"]; hasURL {
		t.Error("uploadURL must not be issued for an existing blob")
	}
	if f.puts != putsAfterFirst {
		t.Errorf("extra PUTs = %d, want 0", f.puts-putsAfterFirst)
	}
}

// TestS3_presignRead_returnsFetchableURL: presign-read hands the client a
// signed GET URL that works directly against the object store.
func TestS3_presignRead_returnsFetchableURL(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("readable remotely")
	sha := sha256HexOf(content)
	rec := s3Presign(t, srv, sha, int64(len(content)), "r.txt", "text/plain")
	var presign map[string]any
	decodeJSON(t, rec, &presign)
	putRaw(t, presign["uploadURL"].(string), content, "text/plain")
	if c := s3Confirm(t, srv, presign["entryID"].(string), sha); c.Code != http.StatusOK {
		t.Fatalf("confirm = %d", c.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/presign-read", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("presign-read status = %d", rr.Code)
	}
	var read map[string]any
	decodeJSON(t, rr, &read)
	if read["direct"] != false {
		t.Fatalf("direct = %v, want false", read["direct"])
	}
	readURL, _ := read["url"].(string)
	if !strings.Contains(readURL, "X-Amz-Signature=") {
		t.Errorf("url missing signature: %s", readURL)
	}
	if got, _ := read["expiresIn"].(float64); got != 900 {
		t.Errorf("expiresIn = %v, want 900", read["expiresIn"])
	}

	// Fetch the signed URL (fake S3 is the host).
	resp, err := http.Get(readURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != string(content) {
		t.Errorf("signed GET status=%d body=%q", resp.StatusCode, body)
	}
}

// TestS3_serverSideUploadLandsInRemoteStore: the classic POST /resources
// endpoint must write through the S3 store when object storage is on.
func TestS3_serverSideUploadLandsInRemoteStore(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("server-side via S3 backend")
	rec := postResource(t, srv, "work", "ss.txt", "text/plain", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	sha, _ := raw["sha"].(string)

	if !f.has(sha) {
		t.Fatal("object not written to remote store")
	}
	if string(f.get(sha)) != string(content) {
		t.Error("remote body mismatch")
	}

	// Serve round-trips through the remote store.
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	got := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(got, req)
	if got.Code != http.StatusOK || got.Body.String() != string(content) {
		t.Errorf("serve status=%d body=%q", got.Code, got.Body.String())
	}
}

// TestS3_statsReportsBackend confirms the stats endpoint distinguishes
// the configured backend.
func TestS3_statsReportsBackend(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["backend"] != "s3" {
		t.Errorf("backend = %v, want s3", raw["backend"])
	}

	// Local-mode server reports local (covered precisely here for the
	// pairing with the s3 case).
	local := newTestServer(t, config.AuthNone, resourceFixture(t))
	req = httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	rec = httptest.NewRecorder()
	local.http.Handler.ServeHTTP(rec, req)
	decodeJSON(t, rec, &raw)
	if raw["backend"] != "local" {
		t.Errorf("backend = %v, want local", raw["backend"])
	}
}

// TestS3_policyEnforcedOnPresign: size and MIME policy apply to the
// presign endpoint even though the bytes never pass through the server.
func TestS3_policyEnforcedOnPresign(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	// Oversized: 51 MiB > DefaultMaxSize.
	rec := s3Presign(t, srv, sha256HexOf([]byte("x")), 51<<20, "big.bin", "application/octet-stream")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize status = %d, want 413", rec.Code)
	}

	// Bad MIME.
	rec = s3Presign(t, srv, sha256HexOf([]byte("y")), 10, "run.exe", "application/x-msdownload")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("bad-mime status = %d, want 415", rec.Code)
	}

	// Nothing was written to the remote store for rejected requests.
	if f.puts != 0 {
		t.Errorf("puts = %d, want 0", f.puts)
	}
}

// TestS3_deleteRemovesFromRemoteStore: DELETE must purge the object from
// both the store and subsequent serves.
func TestS3_deleteRemovesFromRemoteStore(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("delete remote")
	rec := postResource(t, srv, "work", "del.txt", "text/plain", content)
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	sha, _ := raw["sha"].(string)
	if !f.has(sha) {
		t.Fatal("precondition: object not in remote store")
	}

	req := httptest.NewRequest(http.MethodDelete, "/n/work/resources/"+sha, nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if f.has(sha) {
		t.Error("object still in remote store after delete")
	}

	req = httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha, nil)
	rr = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("serve after delete = %d, want 404", rr.Code)
	}
}

// TestS3_thumbServedFromRemote: transform endpoints must materialize the
// source from the object store (ffmpeg path), proving materializeSource
// works against Store implementations other than LocalStore.
func TestS3_thumbServedFromRemote(t *testing.T) {
	hasFFmpegForTest(t)
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	png := generateTestPNG(t, 64, 48)
	rec := postResource(t, srv, "work", "pic.png", "image/png", png)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d", rec.Code)
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	sha, _ := raw["sha"].(string)
	if !f.has(sha) {
		t.Fatal("PNG not in remote store")
	}

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/thumb?w=32&h=32", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("thumb status = %d body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Errorf("thumb Content-Type = %q", ct)
	}
	if rr.Body.Len() == 0 {
		t.Error("empty thumb body")
	}
}

// TestS3_listIncludesRemoteUploads: the list endpoint reflects entries
// regardless of which path (presign or server-side) created them.
func TestS3_listIncludesPresignedUploads(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("listed remotely")
	sha := sha256HexOf(content)
	rec := s3Presign(t, srv, sha, int64(len(content)), "l.txt", "text/plain")
	var presign map[string]any
	decodeJSON(t, rec, &presign)
	putRaw(t, presign["uploadURL"].(string), content, "text/plain")
	if c := s3Confirm(t, srv, presign["entryID"].(string), sha); c.Code != http.StatusOK {
		t.Fatalf("confirm = %d", c.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), sha) {
		t.Errorf("list body missing sha: %s", rr.Body.String())
	}
}

// TestS3_csrfBlocksPresignCrossOrigin: the presign endpoint is a
// mutation and must reject a mismatching Origin (bare API clients
// without Origin are allowed by design — see CSRF).
func TestS3_csrfBlocksPresignCrossOrigin(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, func(c *config.Config) {
		c.PublicOrigin = "https://notes.example.com"
	})

	sha := sha256HexOf([]byte("csrf"))
	body := fmt.Sprintf(`{"sha":%q,"size":1,"filename":"a.txt","mime":"text/plain"}`, sha)

	// Cross-origin → rejected.
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", rec.Code)
	}

	// Matching Origin passes.
	req = httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://notes.example.com")
	rec = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("with Origin status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// Bare client (no Origin) allowed by design.
	req = httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("bare client status = %d, want 200", rec.Code)
	}
}

// TestS3_csrfBlocksConfirmCrossOrigin applies the same check to confirm.
func TestS3_csrfBlocksConfirmCrossOrigin(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, func(c *config.Config) {
		c.PublicOrigin = "https://notes.example.com"
	})

	body := `{"entryID":"e1","sha":"` + strings.Repeat("a", 64) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", rec.Code)
	}
}

// TestS3_presignUnknownNotebook: 404s consistently with other endpoints.
func TestS3_presignUnknownNotebook(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	body := `{"sha":"` + strings.Repeat("b", 64) + `","size":1,"filename":"a.txt","mime":"text/plain"}`
	req := httptest.NewRequest(http.MethodPost, "/n/nosuch/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- local helper -------------------------------------------------------

func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- gap coverage: input validation, TTL config, failure paths ----------

// TestS3_presignRejectsMalformedJSON covers the decode-error path (400)
// distinct from missing-fields.
func TestS3_presignRejectsMalformedJSON(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestS3_presignRejectsInvalidSHA: the sha becomes the presigned object
// key, so non-content-hash values (path traversal, uppercase, short) must
// be rejected before any journal entry or URL is minted.
func TestS3_presignRejectsInvalidSHA(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	cases := []struct {
		name string
		sha  string
	}{
		{"path traversal", "../../bucket-secrets/evil"},
		{"too short", "abc123"},
		{"uppercase", strings.Repeat("A", 64)},
		{"non hex", strings.Repeat("z", 64)},
		{"embedded slash", strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"sha":%q,"size":1,"filename":"a.txt","mime":"text/plain"}`, tc.sha)
			req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
	// No journal entries were created for rejected shas.
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	var list map[string]any
	decodeJSON(t, rec, &list)
	if res, _ := list["resources"].([]any); len(res) != 0 {
		t.Errorf("journal has %d entries, want 0", len(res))
	}
	if f.puts != 0 {
		t.Errorf("puts = %d, want 0", f.puts)
	}
}

// TestS3_confirmRejectsMalformedAndInvalidSHA covers confirm's 400 paths.
func TestS3_confirmRejectsMalformedAndInvalidSHA(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)

	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed status = %d, want 400", rec.Code)
	}

	body := `{"entryID":"e1","sha":"not-a-sha"}`
	req = httptest.NewRequest(http.MethodPost, "/n/work/resources/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid sha status = %d, want 400", rec.Code)
	}
}

// TestS3_presignUnreachableStoreAbortsEntry: when the object store
// endpoint is broken, presign must fail with 500 AND abort the journal
// entry it opened (no orphaned pending entries).
func TestS3_presignUnreachableStoreAbortsEntry(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, func(c *config.Config) {
		c.S3Endpoint = "[" // unparseable URL
	})

	sha := sha256HexOf([]byte("broken endpoint"))
	rec := s3Presign(t, srv, sha, 5, "b.txt", "text/plain")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	counts := journalCounts(t, reg)
	if counts[resources.StateAborted] != 1 {
		t.Errorf("journal aborted = %d, want 1 (%v)", counts[resources.StateAborted], counts)
	}
	if counts[resources.StatePending] != 0 {
		t.Errorf("journal pending = %d, want 0", counts[resources.StatePending])
	}
}

// TestS3_presignReadBrokenStore404 documents current behaviour: Has()
// cannot distinguish "missing" from "store unreachable", so presign-read
// returns 404 (never a signed URL for a blob it cannot confirm).
func TestS3_presignReadBrokenStore404(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, func(c *config.Config) {
		c.S3Endpoint = "http://127.0.0.1:1" // unroutable
	})
	sha := sha256HexOf([]byte("x"))
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/presign-read", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestS3_presignTTLConfigurable: NOTEROOT_S3_PRESIGN_TTL drives expiresIn.
func TestS3_presignTTLConfigurable(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, func(c *config.Config) {
		c.S3PresignTTL = "30m"
	})
	sha := sha256HexOf([]byte("ttl"))
	rec := s3Presign(t, srv, sha, 1, "t.txt", "text/plain")
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if got, _ := raw["expiresIn"].(float64); got != 1800 {
		t.Errorf("expiresIn = %v, want 1800", raw["expiresIn"])
	}
	// The signed X-Amz-Expires must match too.
	uploadURL, _ := raw["uploadURL"].(string)
	if !strings.Contains(uploadURL, "X-Amz-Expires=1800") {
		t.Errorf("URL missing X-Amz-Expires=1800: %s", uploadURL)
	}
}

// TestS3_presignTTLInvalidFallsBack: a malformed TTL falls back to the
// 15-minute default rather than failing requests.
func TestS3_presignTTLInvalidFallsBack(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, func(c *config.Config) {
		c.S3PresignTTL = "bogus"
	})
	sha := sha256HexOf([]byte("ttl2"))
	rec := s3Presign(t, srv, sha, 1, "t.txt", "text/plain")
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if got, _ := raw["expiresIn"].(float64); got != 900 {
		t.Errorf("expiresIn = %v, want 900 (default)", raw["expiresIn"])
	}
}

// --- gap coverage: unknown notebook, delete failure, read TTL -----------

// TestS3_unknownNotebook_allStage7Endpoints: every stage-7 route returns
// 404 consistently when the notebook slug does not exist.
func TestS3_unknownNotebook_allStage7Endpoints(t *testing.T) {
	f := newS3Fake(t)
	srv := newS3Server(t, resourceFixture(t), f, nil)
	sha := sha256HexOf([]byte("x"))
	validSHA64 := sha

	cases := []struct {
		method, target, body, ctype string
	}{
		{"GET", "/n/nope/resources", "", ""},
		{"GET", "/n/nope/resources/stats", "", ""},
		{"GET", "/n/nope/resources/" + validSHA64, "", ""},
		{"GET", "/n/nope/resources/" + validSHA64 + "/presign-read", "", ""},
		{"DELETE", "/n/nope/resources/" + validSHA64, "", ""},
		{"POST", "/n/nope/resources/confirm", `{"entryID":"e","sha":"` + validSHA64 + `"}`, "application/json"},
	}
	for _, tc := range cases {
		var body *strings.Reader
		if tc.body == "" {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.target, body)
		if tc.ctype != "" {
			req.Header.Set("Content-Type", tc.ctype)
		}
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.target, rec.Code)
		}
	}
}

// TestS3_deleteStoreFailure500: a remote delete failure surfaces as 500
// (not a silent success) while the object remains present.
func TestS3_deleteStoreFailure500(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("keep me")
	rec := postResource(t, srv, "work", "k.txt", "text/plain", content)
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	sha := raw["sha"].(string)

	f.failDelete = true // remote DELETE now returns 500
	req := httptest.NewRequest(http.MethodDelete, "/n/work/resources/"+sha, nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if !f.has(sha) {
		t.Error("object should still exist after failed delete")
	}
}

// TestS3_presignReadHonorsTTLConfig: presign-read parses
// NOTEROOT_S3_PRESIGN_TTL independently of presign-upload.
func TestS3_presignReadHonorsTTLConfig(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, func(c *config.Config) {
		c.S3PresignTTL = "45m"
	})

	content := []byte("ttl read")
	sha := sha256HexOf(content)
	pre := s3Presign(t, srv, sha, int64(len(content)), "tr.txt", "text/plain")
	var p map[string]any
	decodeJSON(t, pre, &p)
	putRaw(t, p["uploadURL"].(string), content, "text/plain")
	if c := s3Confirm(t, srv, p["entryID"].(string), sha); c.Code != http.StatusOK {
		t.Fatalf("confirm = %d", c.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+sha+"/presign-read", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	var read map[string]any
	decodeJSON(t, rr, &read)
	if got, _ := read["expiresIn"].(float64); got != 2700 {
		t.Errorf("expiresIn = %v, want 2700", read["expiresIn"])
	}
	if url, _ := read["url"].(string); !strings.Contains(url, "X-Amz-Expires=2700") {
		t.Errorf("URL missing X-Amz-Expires=2700: %s", url)
	}
}

// TestS3_corruptStateDBReturns500: when the notebook's state DB cannot
// be opened, every stage-7 endpoint fails closed with 500 instead of
// proceeding with a half-initialized journal.
func TestS3_corruptStateDBReturns500(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	nb, ok := reg.Get("work")
	if !ok {
		t.Fatal("no notebook")
	}
	// state.Open quarantines a bad DB by renaming it aside, then rebuilds
	// (state is rebuildable by design). Force an open failure by blocking
	// BOTH the DB path and the quarantine target with directories — SQLite
	// cannot open a directory, and the rename fails on a non-empty dir.
	dbPath := nb.Layout.StateDB()
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dbPath+".corrupt", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbPath+".corrupt", "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha := sha256HexOf([]byte("x"))
	cases := []struct {
		method, target, body, ctype string
	}{
		{"GET", "/n/work/resources", "", ""},
		{"GET", "/n/work/resources/stats", "", ""},
		{"GET", "/n/work/resources/" + sha, "", ""},
		{"GET", "/n/work/resources/" + sha + "/presign-read", "", ""},
		{"DELETE", "/n/work/resources/" + sha, "", ""},
		{"POST", "/n/work/resources/presign",
			`{"sha":"` + sha + `","size":1,"filename":"a.txt","mime":"text/plain"}`, "application/json"},
		{"POST", "/n/work/resources/confirm",
			`{"entryID":"e1","sha":"` + sha + `"}`, "application/json"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if tc.ctype != "" {
			req.Header.Set("Content-Type", tc.ctype)
		}
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s = %d, want 500", tc.method, tc.target, rec.Code)
		}
	}
}
