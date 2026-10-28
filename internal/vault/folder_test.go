package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMkdir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "file.md", "x")
	v := Open(root)

	if err := v.Mkdir("a/b/c"); err != nil {
		t.Fatalf("nested mkdir: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("a/b/c not created: %v", err)
	}
	if err := v.Mkdir("a/b/c"); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate: err = %v, want ErrConflict", err)
	}
	for _, bad := range []string{"", "../x", "/abs", ".hidden", "ok/.git", "notes.md", "a/b.swp", "CON"} {
		if err := v.Mkdir(bad); err == nil {
			t.Errorf("Mkdir(%q) succeeded, want error", bad)
		}
	}
	// An empty folder is visible in the tree.
	tree, err := v.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Children) == 0 || tree.Children[0].Name != "a" {
		t.Errorf("tree = %+v, want folder a first", tree.Children)
	}
}

func TestRenameDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "old/one.md", "1")
	write(t, root, "old/sub/two.md", "2")
	write(t, root, "taken/x.md", "x")
	write(t, root, "note.md", "n")
	v := Open(root)

	if err := v.RenameDir("old", "new/place"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got, err := v.Read("new/place/sub/two.md"); err != nil || string(got) != "2" {
		t.Errorf("moved child = %q, %v", got, err)
	}
	if _, err := v.Read("old/one.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old child still readable: %v", err)
	}

	if err := v.RenameDir("new", "taken"); !errors.Is(err, ErrConflict) {
		t.Errorf("onto existing: err = %v, want ErrConflict", err)
	}
	if err := v.RenameDir("new", "new/inside"); !errors.Is(err, ErrConflict) {
		t.Errorf("into itself: err = %v, want ErrConflict", err)
	}
	if err := v.RenameDir("ghost", "g2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing src: err = %v, want ErrNotFound", err)
	}
	if err := v.RenameDir("note.md", "n2"); !errors.Is(err, ErrNotFile) {
		t.Errorf("file src: err = %v, want ErrNotFile", err)
	}
	if err := v.RenameDir("taken", ".hidden"); err == nil {
		t.Error("rename to hidden name succeeded")
	}
	if err := v.RenameDir("taken", "taken"); err != nil {
		t.Errorf("same-path rename: %v", err)
	}
	if err := v.RenameDir("../etc", "x"); err == nil {
		t.Error("escaping src accepted")
	}
}

func TestIsDirAndRemoveDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "full/a.md", "a")
	if err := os.MkdirAll(filepath.Join(root, "skeleton", "x", "y"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "n.md", "n")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := Open(root)

	if ok, err := v.IsDir("empty"); err != nil || !ok {
		t.Errorf("IsDir(empty) = %v, %v", ok, err)
	}
	if ok, err := v.IsDir("n.md"); err != nil || ok {
		t.Errorf("IsDir(n.md) = %v, %v", ok, err)
	}
	if _, err := v.IsDir("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("IsDir(ghost) err = %v", err)
	}

	if err := v.RemoveDir("empty"); err != nil {
		t.Errorf("remove empty: %v", err)
	}
	if err := v.RemoveDir("skeleton"); err != nil {
		t.Errorf("remove empty skeleton: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skeleton")); !os.IsNotExist(err) {
		t.Errorf("skeleton survived: %v", err)
	}
	if err := v.RemoveDir("empty"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: err = %v, want ErrNotFound", err)
	}
	if err := v.RemoveDir("full"); !errors.Is(err, ErrConflict) {
		t.Errorf("remove non-empty: err = %v, want ErrConflict", err)
	}
	if err := v.RemoveDir("n.md"); !errors.Is(err, ErrNotFile) {
		t.Errorf("remove file: err = %v, want ErrNotFile", err)
	}
	// A blocking file leaves empty sibling subfolders untouched too.
	if err := os.MkdirAll(filepath.Join(root, "full", "emptysub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := v.RemoveDir("full"); !errors.Is(err, ErrConflict) {
		t.Errorf("remove mixed: err = %v, want ErrConflict", err)
	}
	if _, err := os.Stat(filepath.Join(root, "full", "emptysub")); err != nil {
		t.Errorf("blocked removal still pruned a subfolder: %v", err)
	}
	if got, _ := v.Read("full/a.md"); string(got) != "a" {
		t.Error("non-empty folder contents were disturbed")
	}
}
