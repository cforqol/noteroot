package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestTrash(t *testing.T, v *Vault) *Trash {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trash")
	return OpenTrash(v, dir)
}

func TestTrash_putAndList(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/b.md", "hello")
	v := Open(root)
	tr := openTestTrash(t, v)
	entries, err := tr.Put("a/b.md", WriteOpts{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	e := entries[0]
	if e.OrigPath != "a/b.md" || e.Actor != "tester" || e.Sha == "" || e.Size != 5 {
		t.Errorf("entry = %+v", e)
	}
	if _, err := v.Read("a/b.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("original still readable: %v", err)
	}
	listed, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != e.Name {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestTrash_putBaseMismatch(t *testing.T) {
	root := t.TempDir()
	write(t, root, "n.md", "v1")
	v := Open(root)
	if _, err := openTestTrash(t, v).Put("n.md", WriteOpts{BaseSha: "deadbeef"}); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got, _ := v.Read("n.md"); string(got) != "v1" {
		t.Errorf("file moved despite conflict: %q", got)
	}
}

func TestTrash_putMissing(t *testing.T) {
	v := Open(t.TempDir())
	if _, err := openTestTrash(t, v).Put("nope.md", WriteOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTrash_putDirPrefix(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/a.md", "a")
	write(t, root, "d/sub/b.md", "b")
	write(t, root, "keep.md", "k")
	v := Open(root)
	entries, err := openTestTrash(t, v).Put("d", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	for _, rel := range []string{"d/a.md", "d/sub/b.md"} {
		if _, err := v.Read(rel); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s still readable", rel)
		}
	}
	if got, _ := v.Read("keep.md"); string(got) != "k" {
		t.Errorf("sibling touched: %q", got)
	}
}

func TestTrash_restore(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "data")
	v := Open(root)
	tr := openTestTrash(t, v)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	path, err := tr.Restore(entries[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if path != "a.md" {
		t.Errorf("path = %q, want a.md", path)
	}
	if got, _ := v.Read("a.md"); string(got) != "data" {
		t.Errorf("restored = %q", got)
	}
	if listed, _ := tr.List(); len(listed) != 0 {
		t.Errorf("listed = %+v, want empty after restore", listed)
	}
}

func TestTrash_restoreMissing(t *testing.T) {
	tr := openTestTrash(t, Open(t.TempDir()))
	if _, err := tr.Restore("nosuchfile.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTrash_restoreOccupied(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "v1")
	v := Open(root)
	tr := openTestTrash(t, v)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Write("a.md", []byte("v2"), WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Restore(entries[0].Name); !errors.Is(err, ErrOccupied) {
		t.Errorf("err = %v, want ErrOccupied", err)
	}
}

func TestTrash_purge(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "a")
	write(t, root, "b.md", "b")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Put("b.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	n, err := tr.Purge(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged = %d, want 2", n)
	}
	if listed, _ := tr.List(); len(listed) != 0 {
		t.Errorf("listed = %+v, want empty", listed)
	}
}

func TestTrash_purgeKeepsFresh(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "a")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	n, err := tr.Purge(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("purged = %d, want 0", n)
	}
	if listed, _ := tr.List(); len(listed) != 1 {
		t.Errorf("listed = %+v, want the fresh entry kept", listed)
	}
}

func TestTrash_listNewestFirst(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "a")
	write(t, root, "b.md", "b")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Put("b.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	listed, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || !strings.HasSuffix(listed[0].OrigPath, "b.md") {
		t.Fatalf("listed = %+v, want b.md first", listed)
	}
}

func TestTrash_putInvalidPath(t *testing.T) {
	tr := openTestTrash(t, Open(t.TempDir()))
	for _, rel := range []string{"", "../x.md", ".git/config"} {
		if _, err := tr.Put(rel, WriteOpts{}); err == nil {
			t.Errorf("Put(%q): expected error", rel)
		}
	}
}

func TestTrash_putMissingRoot(t *testing.T) {
	tr := openTestTrash(t, Open(filepath.Join(t.TempDir(), "gone")))
	if _, err := tr.Put("a.md", WriteOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTrash_putBlockedTrashDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.WriteFile(dir, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrash(Open(root), dir).Put("a.md", WriteOpts{}); err == nil {
		t.Error("blocked trash dir: expected error")
	}
	if got, _ := Open(root).Read("a.md"); string(got) != "x" {
		t.Errorf("original disturbed: %q", got)
	}
}

func TestTrash_putUnreadableTrashDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	v := Open(root)
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := OpenTrash(v, dir).Put("a.md", WriteOpts{}); err == nil {
		t.Error("unwritable trash dir: expected error")
	}
	if got, _ := v.Read("a.md"); string(got) != "x" {
		t.Errorf("original moved despite failure: %q", got)
	}
}

func TestTrash_restoreBadName(t *testing.T) {
	tr := openTestTrash(t, Open(t.TempDir()))
	for _, name := range []string{"", "a/b.md", ".."} {
		if _, err := tr.Restore(name); err == nil {
			t.Errorf("Restore(%q): expected error", name)
		}
	}
}

func TestTrash_restoreCorruptSidecar(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	v := Open(root)
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(v, dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entries[0].Name+".json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	listed, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].OrigPath != "" {
		t.Fatalf("listed = %+v, want ghost entry with empty origin", listed)
	}
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("restore without origin: expected error")
	}
}

func TestTrash_restoreHostileOrigin(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	v := Open(root)
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(v, dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSidecar(t, dir, entries[0].Name, TrashEntry{Name: entries[0].Name, OrigPath: "../evil.md"})
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("hostile origin: expected error")
	}
	if _, err := os.Stat(filepath.Join(root, "evil.md")); !os.IsNotExist(err) {
		t.Error("escape: file written outside vault")
	}
}

func TestTrash_restoreMissingRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	v := Open(root)
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(v, dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Restore(entries[0].Name); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTrash_putDirSkipsUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "d/ok.md", "x")
	write(t, root, "d/locked/inner.md", "x")
	locked := filepath.Join(root, "d", "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	entries, err := openTestTrash(t, Open(root)).Put("d", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want only ok.md", entries)
	}
}

func rewriteSidecar(t *testing.T, dir, name string, e TrashEntry) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrash_putRootLoop(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "looproot")
	if err := os.Symlink(root, root); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	tr := openTestTrash(t, Open(root))
	if _, err := tr.Put("a.md", WriteOpts{}); err == nil {
		t.Fatal("root loop: expected error")
	}
}

func TestTrash_putMidPathLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	tr := openTestTrash(t, Open(root))
	if _, err := tr.Put("loop/a.md", WriteOpts{}); err == nil {
		t.Fatal("mid-path loop: expected error")
	}
}

func TestTrash_putEmptyDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openTestTrash(t, Open(root)).Put("empty", WriteOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTrash_putDirSkipsIgnoredAndDangling(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/ok.md", "x")
	write(t, root, "d/junk.swp", "x")
	if err := os.Symlink("nowhere", filepath.Join(root, "d", "dangling.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	entries, err := openTestTrash(t, Open(root)).Put("d", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].OrigPath != "d/ok.md" {
		t.Fatalf("entries = %+v, want only d/ok.md", entries)
	}
}

func TestTrash_putCollisionRenames(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name == "a.md" {
		t.Fatalf("entries = %+v, want renamed collision", entries)
	}
}

func TestTrash_putCollisionWithDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "d/a.md", "a")
	write(t, root, "d/b.md", "b")
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory squatting the target name is just a collision: the entry
	// is renamed, nothing is lost.
	if err := os.Mkdir(filepath.Join(dir, "d_b.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("d", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["d_a.md"] || !names["d_b.md-1"] {
		t.Errorf("names = %v, want d_a.md and d_b.md-1", names)
	}
}

func TestTrash_executeOnlyTrashDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := OpenTrash(Open(root), dir).Put("a.md", WriteOpts{}); err == nil {
		t.Error("write-only-less trash dir: expected rename error")
	}
}

func TestTrash_sidecarBlockedRollback(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	v := Open(root)
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a.md.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrash(v, dir).Put("a.md", WriteOpts{}); err == nil {
		t.Fatal("blocked sidecar: expected error")
	}
	if got, _ := v.Read("a.md"); string(got) != "x" {
		t.Errorf("rollback failed, original = %q", got)
	}
}

func TestTrash_listOrphanFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphan.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := OpenTrash(Open(t.TempDir()), dir)
	listed, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].OrigPath != "" {
		t.Fatalf("listed = %+v, want synthesized entry without origin", listed)
	}
}

func TestTrash_listUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	outer := t.TempDir()
	dir := filepath.Join(outer, "trash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outer, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outer, 0o755) })
	tr := OpenTrash(Open(t.TempDir()), dir)
	if _, err := tr.List(); err == nil {
		t.Error("unreadable trash dir: expected error")
	}
	if _, err := tr.Purge(time.Now()); err == nil {
		t.Error("unreadable trash dir: purge expected error")
	}
}

func TestTrash_restoreUnreadableSidecar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, entries[0].Name+".json")
	if err := os.Chmod(sidecar, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sidecar, 0o644) })
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("unreadable sidecar: expected error")
	}
}

func TestTrash_restoreUnreadableVault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("unreadable vault: expected error")
	}
}

func TestTrash_restoreLockedSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSidecar(t, dir, entries[0].Name, TrashEntry{Name: entries[0].Name, OrigPath: "locked/a.md"})
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("unwritable target dir: expected error")
	}
}

func TestTrash_restoreEvalsRootError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	outer := t.TempDir()
	root := filepath.Join(outer, "vault")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outer, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outer, 0o755) })
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("unevaluable root: expected error")
	}
}

func TestTrash_purgeReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	n, err := tr.Purge(time.Now().Add(time.Hour))
	if err == nil {
		t.Error("read-only trash dir: expected joined error")
	}
	if n != 0 {
		t.Errorf("purged = %d, want 0", n)
	}
}

func TestTrash_restoreReadOnlySubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	ro := filepath.Join(root, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSidecar(t, dir, entries[0].Name, TrashEntry{Name: entries[0].Name, OrigPath: "ro/a.md"})
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("unwritable target dir: expected rename error")
	}
}

func TestTrash_missingDir(t *testing.T) {
	tr := OpenTrash(Open(t.TempDir()), filepath.Join(t.TempDir(), "nope"))
	listed, err := tr.List()
	if err != nil || len(listed) != 0 {
		t.Errorf("List = %+v, %v; want empty, nil", listed, err)
	}
	if n, err := tr.Purge(time.Now()); err != nil || n != 0 {
		t.Errorf("Purge = %d, %v; want 0, nil", n, err)
	}
}

func TestTrash_restoreUncreatableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	ro := filepath.Join(root, "ro")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "trash")
	tr := OpenTrash(Open(root), dir)
	entries, err := tr.Put("a.md", WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSidecar(t, dir, entries[0].Name, TrashEntry{Name: entries[0].Name, OrigPath: "ro/sub/a.md"})
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if _, err := tr.Restore(entries[0].Name); err == nil {
		t.Error("uncreatable parent: expected error")
	}
}

func TestTrash_emptyRemovesEverything(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "one")
	write(t, root, "b/c.md", "two")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Put("b/c.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}

	n, err := tr.Empty()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Empty() removed %d, want 2", n)
	}
	after, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("trash still holds %+v", after)
	}
	// Sidecars go with the notes, not just the notes themselves.
	left, _ := os.ReadDir(tr.dir)
	if len(left) != 0 {
		t.Errorf("trash dir still holds %d files", len(left))
	}

	// Emptying an already-empty trash is a no-op, not an error.
	if n, err := tr.Empty(); err != nil || n != 0 {
		t.Errorf("second Empty() = %d, %v", n, err)
	}
}

func TestTrash_deleteOneEntry(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "one")
	write(t, root, "b.md", "two")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Put("b.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}

	if err := tr.Delete("a.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rest, err := tr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Name != "b.md" {
		t.Fatalf("remaining = %+v, want just b.md", rest)
	}
	if _, err := os.Stat(filepath.Join(tr.dir, "a.md.json")); !os.IsNotExist(err) {
		t.Error("sidecar outlived its note")
	}
	if err := tr.Delete("a.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}

func TestTrash_deleteRejectsNamesOutsideTheTrash(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "one")
	v := Open(root)
	tr := openTestTrash(t, v)
	if _, err := tr.Put("a.md", WriteOpts{}); err != nil {
		t.Fatal(err)
	}
	// Anything that is not a plain *.md name in this directory is refused
	// before it can reach the filesystem.
	for _, bad := range []string{"", "../a.md", "sub/a.md", "a.md.json", "a.txt", "."} {
		if err := tr.Delete(bad); err == nil {
			t.Errorf("Delete(%q) was accepted", bad)
		}
	}
	if rest, _ := tr.List(); len(rest) != 1 {
		t.Errorf("trash was disturbed: %+v", rest)
	}
}

func TestTrash_read(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "---\ntitle: A\n---\nhello")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	// sidecar from Put
	e, src, err := tr.Read("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "a.md" || e.Actor != "tester" {
		t.Errorf("entry = %+v", e)
	}
	if string(src) != "---\ntitle: A\n---\nhello" {
		t.Errorf("body = %q", string(src))
	}
	// now delete the sidecar so entry() falls back to the file's mod time
	_ = os.Remove(filepath.Join(tr.dir, "a.md.json"))
	e2, _, err := tr.Read("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if e2.Name != "a.md" || e2.Size != 22 {
		t.Errorf("fallback entry = %+v, want Size=22, got %d", e2, e2.Size)
	}
}

func TestTrash_restoreUnder(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "sub")
	_ = os.MkdirAll(nested, 0o755)
	write(t, root, "a.md", "---\ntitle: A\n---\nhello from a")
	write(t, nested, "b.md", "---\ntitle: B\n---\nhello from b")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	_, _ = tr.Put("sub/b.md", WriteOpts{Actor: "tester"})
	// now permanently delete from sub/folder, simulating that they are trashed there
	restored, skipped, err := tr.RestoreUnder("sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0] != "sub/b.md" {
		t.Errorf("restored = %v, want [sub/b.md]", restored)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want empty", skipped)
	}
}

func TestTrash_restoreUnder_root(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "---\ntitle: A\n---\nhello")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	restored, _, err := tr.RestoreUnder("")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0] != "a.md" {
		t.Errorf("restored = %v, want [a.md]", restored)
	}
}

func TestTrash_deleteUnder(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "sub")
	_ = os.MkdirAll(nested, 0o755)
	write(t, root, "a.md", "---\ntitle: A\n---\nhello")
	write(t, nested, "b.md", "---\ntitle: B\n---\nhello from b")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	_, _ = tr.Put("sub/b.md", WriteOpts{Actor: "tester"})
	// purge the sub folder
	n, err := tr.DeleteUnder("sub")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
	_, err = tr.DeleteUnder("sub")
	if err != nil {
		t.Errorf("second delete err = %v, want nil", err)
	}
	// a.md is still there
	list, _ := tr.List()
	if len(list) != 1 || list[0].Name != "a.md" {
		t.Errorf("list = %+v, want [a.md]", list)
	}
}

func TestTrash_deleteUnder_root(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "---\ntitle: A\n---\nhello")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	n, err := tr.DeleteUnder("")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
	list, _ := tr.List()
	if len(list) != 0 {
		t.Errorf("list = %+v, want [], got %+v", list, list)
	}
}

// Verify that RestoreUnder + DeleteUnder correctly reject folder names
// that escape the trash directory.
func TestTrash_restoreUnderRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "sub")
	_ = os.MkdirAll(nested, 0o755)
	write(t, root, "a.md", "---\ntitle: A\n---\nhello")
	v := Open(root)
	tr := openTestTrash(t, v)
	_, _ = tr.Put("a.md", WriteOpts{Actor: "tester"})
	// Passing ".." or "../" should be rejected early
	if _, _, err := tr.RestoreUnder(".."); err == nil {
		t.Error("RestoreUnder(..) should be rejected")
	}
	if _, err := tr.DeleteUnder(".."); err == nil {
		t.Error("DeleteUnder(..) should be rejected")
	}
	if _, _, err := tr.RestoreUnder("../a"); err == nil {
		t.Error("RestoreUnder(../a) should be rejected")
	}
}
