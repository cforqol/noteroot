package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"

	"github.com/BurntSushi/toml"
)

func writeTestManifest(t *testing.T, dir string, slug string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, notebooks.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := notebooks.Manifest{
		ID:            "id-" + slug,
		Slug:          slug,
		Name:          slug,
		LayoutVersion: notebooks.LayoutVersion,
		Created:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := toml.NewEncoder(f).Encode(m); err != nil {
		t.Fatal(err)
	}
}

func openTestRegistry(t *testing.T, slugs ...string) *notebooks.Registry {
	t.Helper()
	dataDir := t.TempDir()
	for _, slug := range slugs {
		dir := filepath.Join(dataDir, "notebooks", slug)
		writeTestManifest(t, dir, slug)
	}
	reg, problems := notebooks.Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("notebooks.Open problems: %v", problems)
	}
	return reg
}

// --- /healthz ---

func TestHealth_returnsOK(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHealth_unauthenticated_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, nil)
	rec := httptest.NewRecorder()
	// No X-Auth-User header — health must still pass
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 (must be unauthenticated)", rec.Code)
	}
}

// --- /readyz ---

func TestReady_returnsOK(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReady_unauthenticated_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("readyz status = %d, want 200 (must be unauthenticated)", rec.Code)
	}
}

// --- /api/v1/notebooks ---

func TestListNotebooks_empty(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notebooks", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	nbs, ok := body["notebooks"]
	if !ok {
		t.Fatal("response missing 'notebooks' key")
	}
	list, ok := nbs.([]any)
	if !ok || len(list) != 0 {
		t.Errorf("notebooks = %v, want empty array", nbs)
	}
}

func TestListNotebooks_withNotebook(t *testing.T) {
	reg := openTestRegistry(t, "work", "personal")
	srv := newTestServer(t, config.AuthNone, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notebooks", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list := body["notebooks"].([]any)
	if len(list) != 2 {
		t.Errorf("notebooks count = %d, want 2", len(list))
	}
}

func TestListNotebooks_requiresAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, nil)
	rec := httptest.NewRecorder()
	// No X-Auth-User header
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notebooks", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestListNotebooks_allowedWithAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notebooks", nil)
	req.Header.Set("X-Auth-User", "alice")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// --- /n/{slug}/ ---

func TestNotebookRoot_notFound(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/missing/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestNotebookRoot_found(t *testing.T) {
	reg := openTestRegistry(t, "work")
	srv := newTestServer(t, config.AuthNone, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["slug"] != "work" {
		t.Errorf("slug = %q, want work", body["slug"])
	}
}

func TestNotebookRoot_requiresAuth_in_forward_mode(t *testing.T) {
	reg := openTestRegistry(t, "work")
	srv := newTestServer(t, config.AuthForward, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// --- content-type ---

func TestWriteJSON_contentType(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}
