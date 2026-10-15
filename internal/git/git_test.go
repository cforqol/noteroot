package git

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary missing:", err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@local")
	run("config", "user.name", "test")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitCount(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "0"
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestFlush_commitsPending(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.md", "hi\n")
	f := New(dir, testLogger())
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s", "-1"); got != "noteroot: alice write a.md" {
		t.Errorf("message = %q", got)
	}
	if status := gitOut(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("dirty tree after flush:\n%s", status)
	}
	_ = f.Close()
}

func TestFlush_messageCoalesces(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.md", "a\n")
	writeFile(t, dir, "b.md", "b\n")
	writeFile(t, dir, "c.md", "c\n")
	f := New(dir, testLogger())
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	f.MarkDirty(Op{Action: "write", Path: "b.md", Actor: "alice"})
	f.MarkDirty(Op{Action: "delete", Path: "c.md", Actor: ""})
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s", "-1"); got != "noteroot: alice write a.md [+2 more]" {
		t.Errorf("message = %q", got)
	}
	if n := commitCount(t, dir); n != "1" {
		t.Errorf("commits = %s, want 1", n)
	}
	_ = f.Close()
}

func TestFlush_emptyIsNoop(t *testing.T) {
	dir := initRepo(t)
	f := New(dir, testLogger())
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := commitCount(t, dir); n != "0" {
		t.Errorf("commits = %s, want 0 (no empty commits)", n)
	}
	// Marks with no net change must not commit either.
	writeFile(t, dir, "a.md", "hi\n")
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	if err := os.Remove(filepath.Join(dir, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := commitCount(t, dir); n != "0" {
		t.Errorf("commits = %s, want 0 (net-zero change)", n)
	}
	_ = f.Close()
}

func TestFlush_noRepo(t *testing.T) {
	dir := t.TempDir()
	f := New(dir, testLogger())
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	if err := f.Flush(); err == nil {
		t.Error("non-repo dir: expected error")
	}
	_ = f.Close()
}

func TestFlush_autoAfterWindow(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.md", "hi\n")
	f := New(dir, testLogger())
	f.window = 50 * time.Millisecond
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := commitCount(t, dir); n == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("auto-flush did not commit within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = f.Close()
}

func TestClose_flushesPending(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.md", "hi\n")
	f := New(dir, testLogger())
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := commitCount(t, dir); n != "1" {
		t.Errorf("commits = %s, want 1", n)
	}
}

func TestFlush_errorRetainsPending(t *testing.T) {
	dir := t.TempDir() // not a repo yet: first Flush must fail
	writeFile(t, dir, "a.md", "hi\n")
	f := New(dir, testLogger())
	f.MarkDirty(Op{Action: "write", Path: "a.md", Actor: "alice"})
	f.window = 50 * time.Millisecond // restore re-arms with this window
	if err := f.Flush(); err == nil {
		t.Fatal("non-repo dir: expected error")
	}
	run := exec.Command("git", "-C", dir, "init", "-q")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// The failed flush must not have discarded the pending op: the
	// re-armed timer retries it (doc: "retried on the next window").
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := commitCount(t, dir); n == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retried flush did not commit within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := gitOut(t, dir, "log", "--format=%s", "-1"); got != "noteroot: alice write a.md" {
		t.Errorf("message = %q", got)
	}
	_ = f.Close()
}
