package backup

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a minimal git repo with one commit so git bundle has
// something to export.
func initGitRepo(t *testing.T, dir string) {
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

// openSQLite creates a minimal SQLite database so VACUUM INTO has a source.
func openSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv (k TEXT, v TEXT)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kv VALUES ('hello', 'world')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
}

func TestCreate_bundle(t *testing.T) {
	vaultDir := t.TempDir()
	initGitRepo(t, vaultDir)
	stateDB := filepath.Join(t.TempDir(), "state.db")
	openSQLite(t, stateDB)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, stateDB, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.BundlePath == "" {
		t.Error("BundlePath is empty")
	}
	if m.BundleSHA == "" {
		t.Error("BundleSHA is empty")
	}
	// Bundle file must exist.
	if _, err := os.Stat(filepath.Join(outDir, m.BundlePath)); err != nil {
		t.Errorf("bundle file missing: %v", err)
	}
	if len(m.Errors) > 0 {
		t.Errorf("unexpected errors: %v", m.Errors)
	}
}

func TestCreate_stateSnapshot(t *testing.T) {
	vaultDir := t.TempDir()
	initGitRepo(t, vaultDir)
	stateDB := filepath.Join(t.TempDir(), "state.db")
	openSQLite(t, stateDB)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, stateDB, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.StatePath == "" {
		t.Error("StatePath is empty")
	}
	if m.StateSHA == "" {
		t.Error("StateSHA is empty")
	}
	// Snapshot must be readable as SQLite.
	snap := filepath.Join(outDir, m.StatePath)
	db, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k = 'hello'`).Scan(&v); err != nil || v != "world" {
		t.Errorf("snapshot kv = %q, %v; want world", v, err)
	}
}

func TestCreate_bundleSHAVerifiable(t *testing.T) {
	vaultDir := t.TempDir()
	initGitRepo(t, vaultDir)
	stateDB := filepath.Join(t.TempDir(), "state.db")
	openSQLite(t, stateDB)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, stateDB, outDir)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute SHA and compare.
	got, err := sha256File(filepath.Join(outDir, m.BundlePath))
	if err != nil || got != m.BundleSHA {
		t.Errorf("sha mismatch: %q != %q (err=%v)", got, m.BundleSHA, err)
	}
}

func TestCreate_missingStateDB_recordsError(t *testing.T) {
	vaultDir := t.TempDir()
	initGitRepo(t, vaultDir)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, "/nonexistent/state.db", outDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.StatePath != "" {
		t.Error("StatePath should be empty on failure")
	}
	found := false
	for _, e := range m.Errors {
		if strings.Contains(e, "vacuum into") || strings.Contains(e, "open src") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected vacuum error in Errors: %v", m.Errors)
	}
}

func TestCreate_nonGitVault_recordsError(t *testing.T) {
	vaultDir := t.TempDir() // not a git repo
	stateDB := filepath.Join(t.TempDir(), "state.db")
	openSQLite(t, stateDB)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, stateDB, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.BundlePath != "" {
		t.Error("BundlePath should be empty on failure")
	}
	found := false
	for _, e := range m.Errors {
		if strings.Contains(e, "git bundle") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected git bundle error in Errors: %v", m.Errors)
	}
}

func TestCreate_timestampPrefix(t *testing.T) {
	vaultDir := t.TempDir()
	initGitRepo(t, vaultDir)
	stateDB := filepath.Join(t.TempDir(), "state.db")
	openSQLite(t, stateDB)
	outDir := t.TempDir()

	m, err := Create(context.Background(), vaultDir, stateDB, outDir)
	if err != nil || m.BundlePath == "" {
		t.Fatalf("Create: %v, bundle=%q", err, m.BundlePath)
	}
	if !strings.HasSuffix(m.BundlePath, "-vault.bundle") {
		t.Errorf("unexpected bundle name %q", m.BundlePath)
	}
	if !strings.HasSuffix(m.StatePath, "-state.db") {
		t.Errorf("unexpected state name %q", m.StatePath)
	}
}
