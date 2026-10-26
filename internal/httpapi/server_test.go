package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
	"noteroot/internal/vault"
	"noteroot/internal/version"

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
	defer func() { _ = f.Close() }()
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

// The frontend reads the build version from /healthz once at boot.
func TestHealthAndReady_reportVersion(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var resp map[string]any
		decodeJSON(t, rec, &resp)
		v, _ := resp["version"].(string)
		if v != version.V {
			t.Errorf("%s version = %q, want %q", path, v, version.V)
		}
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

// --- lifecycle ---

func TestListenAndServe_afterShutdown(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("ListenAndServe = %v, want ErrServerClosed", err)
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

func TestNew_purgesOldTrash(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "content\n"})
	nb, _ := reg.Get("work")
	v := vault.Open(nb.Layout.Vault())
	entries, err := vault.OpenTrash(v, nb.Layout.Trash()).Put("a.md", vault.WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ageSidecar(t, nb.Layout.Trash(), entries[0].Name, -31*24)
	newTestServer(t, config.AuthNone, reg)
	rest, err := vault.OpenTrash(v, nb.Layout.Trash()).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("entries = %+v, want purged", rest)
	}
	if _, err := v.Read("a.md"); err == nil {
		t.Error("purged note must not be restored")
	}
}

// ageSidecar backdates a trash sidecar by hours. White-box on the sidecar
// format, which the vault package owns and versions with it.
func ageSidecar(t *testing.T, trashDir, name string, hours int) {
	t.Helper()
	p := filepath.Join(trashDir, name+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["deletedAt"] = time.Now().Add(time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitAutocommit_endToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary missing:", err)
	}
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", nb.Layout.Vault()}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := put(t, srv, putNotePath("gone.md"), []byte("bye\n"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("put status = %d", rec.Code)
	}
	etag := get(t, srv, notePath("gone.md"), nil).Header().Get("ETag")
	if rec := deleteNote(t, srv, putNotePath("gone.md"), map[string]string{"If-Match": etag}); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", nb.Layout.Vault(), "log", "--format=%s")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "noteroot: unknown write gone.md") {
		t.Fatalf("log = %q, want one coalesced commit", out)
	}
}

func TestShutdown_gitFailureDoesNotFail(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary missing:", err)
	}
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	cmd := exec.Command("git", "-C", nb.Layout.Vault(), "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := put(t, srv, putNotePath("n.md"), []byte("x\n"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("put status = %d", rec.Code)
	}
	if err := os.RemoveAll(filepath.Join(nb.Layout.Vault(), ".git")); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown = %v, want nil despite git failure", err)
	}
}
