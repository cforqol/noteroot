package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"noteroot/internal/config"
)

// initGitVault creates a git repo with one commit in the given dir.
func initGitVault(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
}

func TestCreateBackup_success(t *testing.T) {
	reg := notesFixture(t, map[string]string{"index.md": "# Test"})
	srv := newTestServer(t, config.AuthNone, reg)

	// Seed a git vault in the notebook so git bundle works.
	nbs := reg.List()
	if len(nbs) == 0 {
		t.Fatal("no notebooks")
	}
	initGitVault(t, nbs[0].Layout.Vault())

	req := httptest.NewRequest(http.MethodPost, "/n/work/backup", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if resp["bundlePath"] == "" || resp["bundlePath"] == nil {
		t.Errorf("missing bundlePath in response: %v", resp)
	}
	if resp["statePath"] == "" || resp["statePath"] == nil {
		t.Errorf("missing statePath in response: %v", resp)
	}
	if resp["bundleSHA"] == "" || resp["bundleSHA"] == nil {
		t.Errorf("missing bundleSHA: %v", resp)
	}
}

func TestCreateBackup_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	req := httptest.NewRequest(http.MethodPost, "/n/nope/backup", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCreateBackup_noGitVault_returnsErrors(t *testing.T) {
	// Vault is NOT a git repo — bundle will fail but backup still returns 200
	// with errors listed in the manifest.
	reg := notesFixture(t, map[string]string{"a.md": "x"})
	srv := newTestServer(t, config.AuthNone, reg)

	req := httptest.NewRequest(http.MethodPost, "/n/work/backup", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (partial backup)", rr.Code)
	}
	var resp map[string]any
	decodeJSON(t, rr, &resp)
	// Errors field should be non-empty (bundle failed).
	errs, _ := resp["errors"].([]any)
	if len(errs) == 0 {
		t.Errorf("expected errors in manifest for non-git vault: %v", resp)
	}
	// State snapshot should still succeed.
	if resp["statePath"] == "" || resp["statePath"] == nil {
		t.Errorf("state snapshot should succeed even when bundle fails: %v", resp)
	}
}
