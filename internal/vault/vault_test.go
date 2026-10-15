package vault

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/b.md", "hello")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := Open(root)

	got, err := v.Read("a/b.md")
	if err != nil || string(got) != "hello" {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if _, err := v.Read("missing.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
	if _, err := v.Read("dir"); !errors.Is(err, ErrNotFile) {
		t.Errorf("dir: err = %v, want ErrNotFile", err)
	}
	if _, err := v.Read("../x.md"); err == nil {
		t.Error("traversal: expected error")
	}
	if _, err := v.Read(""); err == nil {
		t.Error("empty: expected error")
	}
}

func TestRead_tooLarge(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "big.md")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := Open(root).Read("big.md"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestRead_symlinkEscape(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret.md", "secret")
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	v := Open(root)
	for _, rel := range []string{"link.md", "dirlink/secret.md"} {
		if _, err := v.Read(rel); !errors.Is(err, ErrEscapes) {
			t.Errorf("Read(%q) err = %v, want ErrEscapes", rel, err)
		}
	}
}

func TestRead_symlinkInsideVaultAllowed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real.md", "ok")
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if got, err := Open(root).Read("alias.md"); err != nil || string(got) != "ok" {
		t.Errorf("Read = %q, %v", got, err)
	}
}

func TestTree(t *testing.T) {
	root := t.TempDir()
	write(t, root, "b.md", "x")
	write(t, root, "A.md", "x")
	write(t, root, "zdir/n.md", "x")
	write(t, root, "adir/m.md", "x")
	write(t, root, "skip.txt", "x")
	write(t, root, ".git/config", "x")
	write(t, root, ".hidden.md", "x")
	write(t, root, "tmp.md.swp", "x")

	tree, err := Open(root).Tree()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range tree.Children {
		names = append(names, c.Name)
	}
	want := []string{"adir", "zdir", "A.md", "b.md"} // dirs first, case-insensitive
	if len(names) != len(want) {
		t.Fatalf("children = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("children = %v, want %v", names, want)
		}
	}
	if got := tree.Children[0].Children[0].Path; got != "adir/m.md" {
		t.Errorf("nested path = %q, want adir/m.md", got)
	}
}

func TestTree_emptyVault(t *testing.T) {
	tree, err := Open(t.TempDir()).Tree()
	if err != nil || len(tree.Children) != 0 {
		t.Errorf("Tree = %+v, %v", tree, err)
	}
}

func TestRoot(t *testing.T) {
	root := t.TempDir()
	if got := Open(root).Root(); got != root {
		t.Errorf("Root = %q, want %q", got, root)
	}
}

func TestRead_missingVaultRoot(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "gone")).Read("a.md")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRead_pathThroughFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	if _, err := Open(root).Read("a.md/sub.md"); err == nil {
		t.Fatal("Read through a regular file: expected error")
	}
}

func TestRead_symlinkLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop.md", filepath.Join(root, "loop.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Read("loop.md"); err == nil {
		t.Fatal("symlink loop: expected error")
	}
}

func TestRead_rootIsSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "looproot")
	if err := os.Symlink(root, root); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Read("a.md"); err == nil {
		t.Fatal("root symlink loop: expected error")
	}
}

func TestTree_missingRoot(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "gone")).Tree()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTree_unreadableSubdirSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "ok.md", "x")
	write(t, root, "locked/inner.md", "x")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	tree, err := Open(root).Tree()
	if err != nil {
		t.Fatalf("Tree = %v", err)
	}
	var names []string
	for _, c := range tree.Children {
		names = append(names, c.Name)
	}
	// locked is listed but its unreadable contents are skipped; walk continues.
	want := []string{"locked", "ok.md"}
	if len(names) != len(want) {
		t.Fatalf("children = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("children = %v, want %v", names, want)
		}
	}
}

func TestTree_unreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "a.md", "x")
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if _, err := Open(root).Tree(); err == nil {
		t.Fatal("unreadable root: expected error, not a silent empty tree")
	}
}

func TestStat(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/b.md", "hello")
	v := Open(root)
	size, mt, err := v.Stat("a/b.md")
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if mt.IsZero() {
		t.Error("modtime is zero")
	}
	if _, _, err := v.Stat("missing.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, _, err := v.Stat("../x.md"); err == nil {
		t.Error("traversal: expected error")
	}
}

func TestWrite_roundtrip(t *testing.T) {
	root := t.TempDir()
	v := Open(root)
	res, err := v.Write("a/b.md", []byte("hello"), WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Size != 5 || res.Sha == "" || res.ModTime.IsZero() {
		t.Errorf("res = %+v, want Created with sha/size/mtime", res)
	}
	got, err := v.Read("a/b.md")
	if err != nil || string(got) != "hello" {
		t.Fatalf("Read = %q, %v", got, err)
	}
	res2, err := v.Write("a/b.md", []byte("bye"), WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Created {
		t.Error("overwrite: Created must be false")
	}
	if res2.Sha == res.Sha {
		t.Error("sha must change with content")
	}
}

func TestWrite_baseSha(t *testing.T) {
	root := t.TempDir()
	v := Open(root)
	res, err := v.Write("n.md", []byte("v1"), WriteOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Write("n.md", []byte("v2"), WriteOpts{BaseSha: res.Sha}); err != nil {
		t.Errorf("matching base: %v", err)
	}
	if _, err := v.Write("n.md", []byte("v3"), WriteOpts{BaseSha: "deadbeef"}); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got, _ := v.Read("n.md"); string(got) != "v2" {
		t.Errorf("loser write landed: %q", got)
	}
	if _, err := v.Write("new.md", []byte("x"), WriteOpts{BaseSha: "deadbeef"}); !errors.Is(err, ErrConflict) {
		t.Errorf("base on missing file err = %v, want ErrConflict", err)
	}
}

func TestWrite_rejects(t *testing.T) {
	v := Open(t.TempDir())
	if _, err := v.Write("../x.md", []byte("x"), WriteOpts{}); err == nil {
		t.Error("traversal: expected error")
	}
	if _, err := v.Write("", []byte("x"), WriteOpts{}); err == nil {
		t.Error("empty: expected error")
	}
	if _, err := v.Write("a/b/c/d/e/f.md", []byte("x"), WriteOpts{}); err == nil {
		t.Error("too deep: expected error")
	}
	big := make([]byte, MaxReadBytes+1)
	if _, err := v.Write("big.md", big, WriteOpts{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestWrite_depthBoundary(t *testing.T) {
	v := Open(t.TempDir())
	if _, err := v.Write("a/b/c/d/e.md", []byte("x"), WriteOpts{}); err != nil {
		t.Errorf("5 levels: %v", err)
	}
}

func TestWrite_symlinkEscape(t *testing.T) {
	outside := t.TempDir()
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Write("dirlink/evil.md", []byte("x"), WriteOpts{}); !errors.Is(err, ErrEscapes) {
		t.Errorf("err = %v, want ErrEscapes", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("outside vault was touched: %v", entries)
	}
}

func TestWrite_missingRoot(t *testing.T) {
	v := Open(filepath.Join(t.TempDir(), "gone"))
	if _, err := v.Write("a.md", []byte("x"), WriteOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestWrite_concurrentSamePath(t *testing.T) {
	v := Open(t.TempDir())
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = v.Write("race.md", []byte{byte('a' + i)}, WriteOpts{})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	}
	got, err := v.Read("race.md")
	if err != nil || len(got) != 1 {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestWrite_rootSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "looproot")
	if err := os.Symlink(root, root); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Write("a.md", []byte("x"), WriteOpts{}); err == nil {
		t.Fatal("root symlink loop: expected error")
	}
}

func TestWrite_danglingSymlinkParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("nowhere", filepath.Join(root, "dangling")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Write("dangling/a.md", []byte("x"), WriteOpts{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestWrite_fileAsParent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "x")
	if _, err := Open(root).Write("a.md/sub.md", []byte("y"), WriteOpts{}); err == nil {
		t.Fatal("file as parent: expected error")
	}
}

func TestWrite_unwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "locked/x.md", "x")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := Open(root).Write("locked/y.md", []byte("y"), WriteOpts{}); err == nil {
		t.Fatal("unwritable parent: expected error")
	}
}

func TestWrite_midPathSymlinkLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Open(root).Write("loop/a.md", []byte("x"), WriteOpts{}); err == nil {
		t.Fatal("symlink loop in parent: expected error")
	}
}

func TestWrite_deepUnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	write(t, root, "locked/x.md", "x")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := Open(root).Write("locked/sub/y.md", []byte("y"), WriteOpts{}); err == nil {
		t.Fatal("unreadable ancestor: expected error")
	}
}

func TestWrite_rejectsGit(t *testing.T) {
	v := Open(t.TempDir())
	if _, err := v.Write(".git/config", []byte("x"), WriteOpts{}); err == nil {
		t.Fatal(".git write: expected error")
	}
}
