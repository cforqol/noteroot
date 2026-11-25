package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

// twoResourceNotebooks builds a registry with two healthy notebooks,
// "alpha" and "beta", each with a trivial vault — for proving the
// global quota is a ceiling across notebooks, not a second per-notebook
// check.
func twoResourceNotebooks(t *testing.T) *notebooks.Registry {
	t.Helper()
	dataDir := t.TempDir()
	for _, slug := range []string{"alpha", "beta"} {
		dir := filepath.Join(dataDir, "notebooks", slug)
		writeTestManifest(t, dir, slug)
		writeVaultFile(t, filepath.Join(dir, notebooks.VaultDir), "a.md", "x\n")
	}
	reg, problems := notebooks.Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("notebooks.Open problems: %v", problems)
	}
	return reg
}

func newGlobalQuotaServer(t *testing.T, quotaBytes, globalQuotaBytes int64) *Server {
	t.Helper()
	cfg := config.Config{
		AuthMode:         config.AuthNone,
		QuotaBytes:       quotaBytes,
		GlobalQuotaBytes: globalQuotaBytes,
		GCInterval:       "999h",
	}
	return New(cfg, newTestLogger(), twoResourceNotebooks(t))
}

func TestGlobalQuota_zeroMeansUnlimited(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 0)
	content := strings.Repeat("x", 10000)
	rec := postResource(t, srv, "alpha", "large.txt", "text/plain", []byte(content))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (global quota disabled)", rec.Code)
	}
}

func TestGlobalQuota_capsAcrossNotebooks(t *testing.T) {
	// Per-notebook quota disabled; only the 10-byte global ceiling
	// applies, and it must be enforced even for a notebook that has
	// never uploaded anything itself.
	srv := newGlobalQuotaServer(t, 0, 10)

	rec := postResource(t, srv, "alpha", "a.txt", "text/plain", []byte("1234567")) // 7 bytes
	if rec.Code != http.StatusCreated {
		t.Fatalf("alpha upload status = %d, body = %s", rec.Code, rec.Body)
	}

	// beta's own quota is unlimited, but the combined total (7 + 7 = 14)
	// would exceed the global 10-byte ceiling.
	rec = postResource(t, srv, "beta", "b.txt", "text/plain", []byte("7654321")) // 7 bytes
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("beta upload status = %d, want 507 (global cap reached by alpha's usage): %s", rec.Code, rec.Body)
	}

	// A small enough upload that still fits under the remaining 3 bytes
	// of global budget succeeds.
	rec = postResource(t, srv, "beta", "c.txt", "text/plain", []byte("abc")) // 3 bytes
	if rec.Code != http.StatusCreated {
		t.Fatalf("beta small upload status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestGlobalQuota_onTopOfPerNotebookQuota(t *testing.T) {
	// Both ceilings active: a per-notebook quota generous enough to
	// allow the upload on its own must still be blocked by the
	// stricter global ceiling.
	srv := newGlobalQuotaServer(t, 1000, 5)

	rec := postResource(t, srv, "alpha", "big.txt", "text/plain", []byte("123456")) // 6 bytes
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507 (blocked by the global ceiling despite room in the per-notebook quota): %s", rec.Code, rec.Body)
	}
}

func TestGlobalQuota_presignExceeds507(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 5)
	sha64 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body := `{"sha":"` + sha64 + `","size":100,"filename":"big.txt","mime":"text/plain"}`
	req := httptest.NewRequest(http.MethodPost, "/n/alpha/resources/presign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInsufficientStorage {
		t.Errorf("presign global quota status = %d, want 507; body = %s", rr.Code, rr.Body.String())
	}
}

func TestGlobalQuota_statsReportGlobalFields(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 5000)

	rec := postResource(t, srv, "alpha", "a.txt", "text/plain", []byte("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/n/beta/resources/stats", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if q, _ := resp["globalQuotaBytes"].(float64); q != 5000 {
		t.Errorf("globalQuotaBytes = %v, want 5000", resp["globalQuotaBytes"])
	}
	// beta's own stats must reflect global usage contributed by alpha:
	// this is a cross-notebook figure, not beta's own usedBytes (0).
	if got, _ := resp["globalUsedBytes"].(float64); got != 5 {
		t.Errorf("globalUsedBytes (from beta's stats) = %v, want 5 (alpha's committed upload)", resp["globalUsedBytes"])
	}
	if used, _ := resp["usedBytes"].(float64); used != 0 {
		t.Errorf("beta's own usedBytes = %v, want 0", used)
	}
}

func TestGlobalQuota_statsOmitGlobalUsageWhenDisabled(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 0)
	req := httptest.NewRequest(http.MethodGet, "/n/alpha/resources/stats", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if q, _ := resp["globalQuotaBytes"].(float64); q != 0 {
		t.Errorf("globalQuotaBytes = %v, want 0", resp["globalQuotaBytes"])
	}
}

// blockStateDB makes state.Open permanently fail for one notebook, the
// same technique TestS3_corruptStateDBReturns500 uses: directories at
// both the DB path and its quarantine target, since SQLite cannot open
// a directory and the quarantine rename fails on a non-empty one.
func blockStateDB(t *testing.T, nb *notebooks.Notebook) {
	t.Helper()
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
}

// TestGlobalQuota_skipsUnreadableNotebook proves the best-effort
// posture: a notebook whose state DB cannot be opened right now is
// skipped (and logged) rather than failing the whole global quota
// check — the same degrade-availability-over-precision rule fitsQuota
// already follows for a single notebook.
func TestGlobalQuota_skipsUnreadableNotebook(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 5000)

	// Block beta's state DB before anything (including the global
	// quota check itself, which would otherwise create it on first
	// touch) opens it.
	nb, ok := srv.reg.Get("beta")
	if !ok {
		t.Fatal("notebook beta missing")
	}
	blockStateDB(t, nb)

	rec := postResource(t, srv, "alpha", "a.txt", "text/plain", []byte("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("alpha upload status = %d, body = %s", rec.Code, rec.Body)
	}

	// Global usage still reflects alpha's 5 committed bytes; beta is
	// skipped rather than turning the whole check into an error.
	used, _ := srv.globalUsedAndReserved(t.Context())
	if used != 5 {
		t.Errorf("globalUsedAndReserved used = %d, want 5 (beta skipped, not counted or erroring)", used)
	}

	// A further upload against the healthy notebook still works: the
	// unreadable sibling does not take the whole quota check down.
	rec = postResource(t, srv, "alpha", "b.txt", "text/plain", []byte("more"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status after a sibling's state DB is blocked = %d, want 201: %s", rec.Code, rec.Body)
	}
}

// Mirrors TestQuota_concurrentUploadsCannotOversell, but the race is
// across two different notebooks sharing one global budget: the
// check+reserve window must be serialized process-wide, not just
// per-notebook, or two concurrent uploads to different notebooks could
// both pass the same free-space check against the shared ceiling.
func TestGlobalQuota_concurrentUploadsAcrossNotebooksCannotOversell(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 10)
	const n = 8
	codes := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slug := "alpha"
			if i%2 == 1 {
				slug = "beta"
			}
			content := []byte(fmt.Sprintf("blob-%02d", i)) // 7 bytes, unique sha
			rec := postResource(t, srv, slug, fmt.Sprintf("b%d.txt", i), "text/plain", content)
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
		t.Errorf("created = %d, want exactly 1 (global quota fits one 7-byte blob total)", created)
	}
	if rejected != n-1 {
		t.Errorf("rejected = %d, want %d", rejected, n-1)
	}
}
