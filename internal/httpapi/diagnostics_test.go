package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func postDiagnostics(t *testing.T, srv *Server, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/n/"+slug+"/diagnostics", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func readZipFile(t *testing.T, body []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			return data
		}
	}
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	t.Fatalf("zip missing %q; entries: %v", name, names)
	return nil
}

func TestDiagnostics_unknownNotebook404(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	rec := postDiagnostics(t, srv, "nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDiagnostics_returnsValidZipWithExpectedEntries(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))

	upload := postResource(t, srv, "work", "a.txt", "text/plain", []byte("hello"))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", upload.Code)
	}

	rec := postDiagnostics(t, srv, "work")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "work-diagnostics.zip") {
		t.Errorf("Content-Disposition = %q, want work-diagnostics.zip", cd)
	}

	body := rec.Body.Bytes()
	diagJSON := readZipFile(t, body, "diagnostics.json")
	var report diagnosticsReport
	if err := json.Unmarshal(diagJSON, &report); err != nil {
		t.Fatalf("decode diagnostics.json: %v", err)
	}
	if report.Notebook.Slug != "work" {
		t.Errorf("Notebook.Slug = %q, want work", report.Notebook.Slug)
	}
	if report.Version == "" {
		t.Error("Version empty")
	}
	if report.Goroutines <= 0 {
		t.Error("Goroutines <= 0")
	}
	if report.Notebook.UsedBytes != 5 {
		t.Errorf("UsedBytes = %d, want 5", report.Notebook.UsedBytes)
	}
	if report.Notebook.VaultRowCount < 0 {
		t.Errorf("VaultRowCount = %d, want >= 0", report.Notebook.VaultRowCount)
	}
	if report.Notebook.Breaker.Open {
		t.Error("Breaker.Open = true for a healthy notebook")
	}
	foundStateDB := false
	for _, db := range report.Notebook.Databases {
		if db.Name == "state.db" {
			foundStateDB = true
			if !db.Exists || db.Bytes <= 0 {
				t.Errorf("state.db stat = %+v, want exists with size > 0", db)
			}
		}
	}
	if !foundStateDB {
		t.Errorf("Databases missing state.db: %+v", report.Notebook.Databases)
	}
	if report.GlobalQuota != nil {
		t.Errorf("GlobalQuota = %+v, want nil (disabled)", report.GlobalQuota)
	}

	goroutines := readZipFile(t, body, "goroutines.txt")
	if !bytes.Contains(goroutines, []byte("goroutine")) {
		t.Errorf("goroutines.txt doesn't look like a goroutine dump: %s", goroutines[:min(200, len(goroutines))])
	}

	// recent-errors.log must exist as an entry even with nothing
	// captured yet (empty file is fine, missing entry is not).
	readZipFile(t, body, "recent-errors.log")
}

func TestDiagnostics_capturesRecentWarnLogs(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	srv.log.Warn("synthetic diagnostics warning", "marker", "xyz123")

	rec := postDiagnostics(t, srv, "work")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	logData := readZipFile(t, rec.Body.Bytes(), "recent-errors.log")
	if !bytes.Contains(logData, []byte("synthetic diagnostics warning")) {
		t.Errorf("recent-errors.log missing the captured warning: %s", logData)
	}
}

func TestDiagnostics_includesGlobalQuotaWhenEnabled(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 5000)
	upload := postResource(t, srv, "alpha", "a.txt", "text/plain", []byte("hello"))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", upload.Code)
	}

	rec := postDiagnostics(t, srv, "alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var report diagnosticsReport
	if err := json.Unmarshal(readZipFile(t, rec.Body.Bytes(), "diagnostics.json"), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.GlobalQuota == nil {
		t.Fatal("GlobalQuota = nil, want populated when enabled")
	}
	if report.GlobalQuota.QuotaBytes != 5000 {
		t.Errorf("GlobalQuota.QuotaBytes = %d, want 5000", report.GlobalQuota.QuotaBytes)
	}
	if report.GlobalQuota.UsedBytes != 5 {
		t.Errorf("GlobalQuota.UsedBytes = %d, want 5", report.GlobalQuota.UsedBytes)
	}
}

// TestDiagnostics_degradesGracefullyWhenStateDBUnreadable proves the
// bundle still comes back (with best-effort defaults instead of real
// row counts/usage) when the notebook's own state DB can't be opened
// — exactly the situation this endpoint exists to help diagnose, so
// it must not itself fail closed.
func TestDiagnostics_degradesGracefullyWhenStateDBUnreadable(t *testing.T) {
	reg := resourceFixture(t)
	nb, ok := reg.Get("work")
	if !ok {
		t.Fatal("notebook work missing")
	}
	blockStateDB(t, nb)

	srv := New(config.Config{
		DataDir: t.TempDir(), ListenAddr: ":0", LogLevel: "info", AuthMode: config.AuthNone,
	}, newTestLogger(), reg)

	rec := postDiagnostics(t, srv, "work")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with an unreadable state DB: %s", rec.Code, rec.Body)
	}
	var report diagnosticsReport
	if err := json.Unmarshal(readZipFile(t, rec.Body.Bytes(), "diagnostics.json"), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Notebook.Slug != "work" {
		t.Errorf("Notebook.Slug = %q, want work", report.Notebook.Slug)
	}
	if report.Notebook.UsedBytes != 0 || report.Notebook.VaultRowCount != 0 {
		t.Errorf("degraded report should report zero usage/row-count, got %+v", report.Notebook)
	}
}

// TestAdminHealth_degradesGracefullyWhenStateDBUnreadable is the same
// best-effort check for the health page: one notebook's unreadable
// state DB must not take the whole page down.
func TestAdminHealth_degradesGracefullyWhenStateDBUnreadable(t *testing.T) {
	reg := twoNotebookRegistry(t)
	nb, ok := reg.Get("work")
	if !ok {
		t.Fatal("notebook work missing")
	}
	blockStateDB(t, nb)

	srv := New(config.Config{
		DataDir: t.TempDir(), ListenAddr: ":0", LogLevel: "info", AuthMode: config.AuthNone,
	}, newTestLogger(), reg)

	rec := getAdminHealth(t, srv, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with one notebook's state DB unreadable", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"work", "bad"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing notebook %q: %s", want, body)
		}
	}
}

// TestDiagnostics_breakerGuarded proves the diagnostics endpoint
// participates in the per-notebook circuit breaker (#44): once a
// notebook's breaker is open, diagnostics fails fast with 503 too,
// rather than always succeeding regardless of the notebook's state.
func TestDiagnostics_breakerGuarded(t *testing.T) {
	reg := twoNotebookRegistry(t)
	cfg := config.Config{
		DataDir:          t.TempDir(),
		ListenAddr:       ":0",
		LogLevel:         "info",
		AuthMode:         config.AuthNone,
		BreakerThreshold: 1,
		BreakerCooldown:  "1h",
	}
	srv := New(cfg, newTestLogger(), reg)

	if rec := get(t, srv, "/n/bad/search?q=x", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("priming failure status = %d, want 500", rec.Code)
	}

	rec := postDiagnostics(t, srv, "bad")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("diagnostics status for a tripped notebook = %d, want 503: %s", rec.Code, rec.Body)
	}

	// The healthy sibling is unaffected.
	rec = postDiagnostics(t, srv, "work")
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics status for the healthy sibling = %d, want 200: %s", rec.Code, rec.Body)
	}
}

