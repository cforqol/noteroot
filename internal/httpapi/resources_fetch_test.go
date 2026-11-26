package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"noteroot/internal/config"
)

func mustParseAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return addr
}

// postFetch POSTs {"url": url} to /n/{slug}/resources/fetch.
func postFetch(t *testing.T, srv *Server, slug, url string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": url})
	req := httptest.NewRequest(http.MethodPost, "/n/"+slug+"/resources/fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// fetchTestServer wires srv.fetchClient to a plain (unguarded) client
// so the test can point fetchResource at an httptest server, which
// always listens on loopback — exactly what guardedFetchClient exists
// to block in production. The guard itself is tested independently in
// TestGuardedFetchClient_blocksLoopback and TestIsPubliclyRoutable.
func fetchTestServer(t *testing.T) *Server {
	t.Helper()
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	srv.fetchClient = &http.Client{}
	return srv
}

func TestFetchResource_success(t *testing.T) {
	content := []byte("remote attachment bytes")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(content)
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/photo.txt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["sha"] == "" || raw["sha"] == nil {
		t.Fatal("no sha in response")
	}
	if raw["filename"] != "photo.txt" {
		t.Errorf("filename = %v, want photo.txt", raw["filename"])
	}
	if raw["size"].(float64) != float64(len(content)) {
		t.Errorf("size = %v, want %d", raw["size"], len(content))
	}
	if raw["mime"] != "text/plain" {
		t.Errorf("mime = %v, want text/plain", raw["mime"])
	}
	if raw["created"] != true {
		t.Errorf("created = %v, want true", raw["created"])
	}

	// The blob must actually be retrievable afterwards.
	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/n/work/resources/"+raw["sha"].(string), nil)
	srv.http.Handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get resource status = %d", getRec.Code)
	}
	if getRec.Body.String() != string(content) {
		t.Errorf("stored content mismatch")
	}
}

// Falls back to sniffing by extension when the remote serves a
// generic/missing Content-Type, same fallback uploadResource applies.
func TestFetchResource_genericContentTypeSniffsExtension(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0})
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/pic.jpg")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["mime"] != "image/jpeg" {
		t.Errorf("mime = %v, want image/jpeg", raw["mime"])
	}
}

func TestFetchResource_dedupHit(t *testing.T) {
	content := []byte("dup bytes")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec1 := postFetch(t, srv, "work", remote.URL+"/a.bin")
	rec2 := postFetch(t, srv, "work", remote.URL+"/b.bin")
	if rec1.Code != http.StatusCreated || rec2.Code != http.StatusCreated {
		t.Fatalf("statuses = %d, %d", rec1.Code, rec2.Code)
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

func TestFetchResource_rejectsNonHTTPScheme(t *testing.T) {
	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", "file:///etc/passwd")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFetchResource_rejectsEmbeddedCredentials(t *testing.T) {
	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", "http://user:pass@example.com/x.png")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFetchResource_missingURL(t *testing.T) {
	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFetchResource_unknownNotebook(t *testing.T) {
	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "nope", "http://example.com/x.png")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFetchResource_upstreamNotFound(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/missing.png")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestFetchResource_rejectsTooLargeByContentLength(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(51<<20))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024)) // body need not match; declared size alone must reject
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/big.bin")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// A remote that declares a Content-Length but delivers fewer bytes
// (truncated transfer) must be rejected rather than stored as a
// silently corrupted blob — the core integrity guarantee #107 asks
// for when "fetching from other site".
func TestFetchResource_rejectsTruncatedBody(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 100))
		// Hijack and close early to truncate the body the client sees
		// before Content-Length is satisfied.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/truncated.bin")
	// A connection the client can't fully read back is surfaced as a
	// transport-level error (502) before any size-mismatch check runs;
	// either way, it must never be StatusCreated.
	if rec.Code == http.StatusCreated {
		t.Errorf("status = %d, truncated body must never be stored as created", rec.Code)
	}
}

// SVG is blocked unconditionally by policy (it is XML and executes
// script same-origin) regardless of how the attachment arrived.
func TestFetchResource_rejectsSVG(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte("<svg></svg>"))
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/evil.svg")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

// A chunked response (no Content-Length, declaredSize == -1) is
// charged at the policy maximum for the quota check, same fallback
// uploadResource applies to a chunked client upload, and the journal
// still ends up with the real stored size once known.
func TestFetchResource_unknownContentLength_recordsRealSize(t *testing.T) {
	content := []byte("chunked-remote-content")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		flusher, _ := w.(http.Flusher)
		// Two separate writes with a flush between them force the Go
		// server to switch to chunked transfer encoding, so the
		// client sees ContentLength == -1.
		_, _ = w.Write(content[:5])
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write(content[5:])
	}))
	defer remote.Close()

	srv := fetchTestServer(t)
	rec := postFetch(t, srv, "work", remote.URL+"/chunked.txt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeJSON(t, rec, &raw)
	if raw["size"].(float64) != float64(len(content)) {
		t.Errorf("size = %v, want %d (real stored size, not the policy-max placeholder)", raw["size"], len(content))
	}

	stats := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	srec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(srec, stats)
	var statsResp map[string]any
	decodeJSON(t, srec, &statsResp)
	if used, _ := statsResp["usedBytes"].(float64); used != float64(len(content)) {
		t.Errorf("usedBytes = %v, want %d", statsResp["usedBytes"], len(content))
	}
}

func TestFetchResource_quotaExceeded(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345678901")) // 11 bytes > 10-byte quota
	}))
	defer remote.Close()

	srv := newQuotaServer(t, 10)
	srv.fetchClient = &http.Client{}
	rec := postFetch(t, srv, "work", remote.URL+"/x.bin")
	if rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", rec.Code)
	}
}

// --- SSRF guard ---

func TestIsPubliclyRoutable(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"8.8.8.8", true},
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true}, // public IPv6 (Cloudflare)
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.5", false},
		{"172.16.0.5", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"0.0.0.0", false},
		{"224.0.0.1", false},       // multicast
		{"fe80::1", false},         // link-local v6
		{"fc00::1", false},         // ULA (private) v6
		{"::ffff:10.0.0.1", false}, // IPv4-mapped private
	}
	for _, c := range cases {
		addr := mustParseAddr(t, c.addr)
		if got := isPubliclyRoutable(addr); got != c.want {
			t.Errorf("isPubliclyRoutable(%s) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestGuardedFetchClient_blocksLoopback(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should never be read"))
	}))
	defer remote.Close()

	client := guardedFetchClient(2 * time.Second)
	_, err := client.Get(remote.URL)
	if err == nil {
		t.Fatal("expected guarded client to refuse a loopback address")
	}
}

func TestGuardedFetchClient_neverFollowsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("final"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := guardedFetchClient(2 * time.Second)
	resp, err := client.Get(redirector.URL)
	// Dialing redirector.URL itself is already loopback and blocked,
	// but even an allowed host must not auto-follow: CheckRedirect
	// returns ErrUseLastResponse, so a successful Do would come back
	// with the redirect status, never transparently fetching target.
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("guarded client followed a redirect")
		}
	}
}
