package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"noteroot/internal/config"
)

func TestGCEndpoint_removesNothingOnFreshStore(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))

	// Upload something so there is a committed entry — GC must keep it.
	rec := postResource(t, srv, "work", "keep.txt", "text/plain", []byte("keep"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/gc", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("gc status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if _, ok := resp["journalEntriesRemoved"]; !ok {
		t.Errorf("response missing journalEntriesRemoved: %v", resp)
	}
	if _, ok := resp["blobsRemoved"]; !ok {
		t.Errorf("response missing blobsRemoved: %v", resp)
	}
	if n, _ := resp["journalEntriesRemoved"].(float64); n != 0 {
		t.Errorf("journalEntriesRemoved = %v, want 0 (committed entries kept)", n)
	}
}

func TestGCEndpoint_unknownNotebook404(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	req := httptest.NewRequest(http.MethodPost, "/n/nope/resources/gc", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestResourceStats_includesQuotaFields(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))

	rec := postResource(t, srv, "work", "data.txt", "text/plain", []byte("quota check content"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("stats = %d", rr.Code)
	}
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if _, ok := resp["usedBytes"]; !ok {
		t.Errorf("missing usedBytes: %v", resp)
	}
	if _, ok := resp["quotaBytes"]; !ok {
		t.Errorf("missing quotaBytes: %v", resp)
	}
	if used, _ := resp["usedBytes"].(float64); used <= 0 {
		t.Errorf("usedBytes = %v, want > 0 after upload", used)
	}
}

func newQuotaServer(t *testing.T, quotaBytes int64) *Server {
	t.Helper()
	cfg := config.Config{
		AuthMode:   config.AuthNone,
		QuotaBytes: quotaBytes,
		GCInterval: "999h",
	}
	return New(cfg, newTestLogger(), resourceFixture(t))
}

func TestQuota_uploadExceeds507(t *testing.T) {
	srv := newQuotaServer(t, 10) // 10-byte limit

	// 11 bytes → 507.
	rec := postResource(t, srv, "work", "big.txt", "text/plain", []byte("12345678901"))
	if rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if msg, _ := resp["error"].(string); msg == "" {
		t.Error("expected error message in body")
	}
}

func TestQuota_uploadWithinLimitSucceeds(t *testing.T) {
	srv := newQuotaServer(t, 1000)

	rec := postResource(t, srv, "work", "small.txt", "text/plain", []byte("hi"))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
}

func TestQuota_zeroMeansUnlimited(t *testing.T) {
	srv := newQuotaServer(t, 0)

	// Large-ish upload should not be blocked by quota.
	content := strings.Repeat("x", 10000)
	rec := postResource(t, srv, "work", "large.txt", "text/plain", []byte(content))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (quota=0 means unlimited)", rec.Code)
	}
}

func TestQuota_presignExceeds507(t *testing.T) {
	srv := newQuotaServer(t, 5) // 5-byte limit

	sha64 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body := `{"sha":"` + sha64 + `","size":100,"filename":"big.txt","mime":"text/plain"}`
	req := httptest.NewRequest(http.MethodPost, "/n/work/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInsufficientStorage {
		t.Errorf("presign quota status = %d, want 507; body = %s", rr.Code, rr.Body.String())
	}
}

func TestQuota_statsReportQuotaBytes(t *testing.T) {
	srv := newQuotaServer(t, 5000)

	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if q, _ := resp["quotaBytes"].(float64); q != 5000 {
		t.Errorf("quotaBytes = %v, want 5000", resp["quotaBytes"])
	}
	if _, ok := resp["reservedBytes"]; !ok {
		t.Errorf("missing reservedBytes: %v", resp)
	}
}

// Two simultaneous uploads must never both pass the same free-space
// check: the check+begin window is serialized, and an in-flight
// upload's pending journal row reserves its bytes for the next
// checker. With a 10-byte quota and 7-byte blobs, exactly one of eight
// concurrent uploads can succeed.
func TestQuota_concurrentUploadsCannotOversell(t *testing.T) {
	srv := newQuotaServer(t, 10)
	const n = 8
	codes := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct 7-byte contents: unique shas, no dedup help.
			content := []byte(fmt.Sprintf("blob-%02d", i))
			rec := postResource(t, srv, "work", fmt.Sprintf("b%d.txt", i), "text/plain", content)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	created, rejected := 0, 0
	for i, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusInsufficientStorage:
			rejected++
		default:
			t.Errorf("unexpected status %d: %s", c, bodies[i])
		}
	}
	if created != 1 {
		t.Errorf("created = %d, want exactly 1 (quota fits one 7-byte blob)", created)
	}
	if rejected != n-1 {
		t.Errorf("rejected = %d, want %d", rejected, n-1)
	}

	// The winner's bytes are committed, nothing is left reserved.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/n/work/resources/stats", nil)
	srv.http.Handler.ServeHTTP(rr, req)
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if used, _ := resp["usedBytes"].(float64); used != 7 {
		t.Errorf("usedBytes = %v, want 7", resp["usedBytes"])
	}
	if resv, _ := resp["reservedBytes"].(float64); resv != 0 {
		t.Errorf("reservedBytes = %v, want 0", resp["reservedBytes"])
	}
}
