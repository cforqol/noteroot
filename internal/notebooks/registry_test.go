package notebooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func writeManifest(t *testing.T, dir string, m Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := toml.NewEncoder(f).Encode(m); err != nil {
		t.Fatal(err)
	}
}

func validManifest(slug string) Manifest {
	return Manifest{
		ID:            "id-" + slug,
		Slug:          slug,
		Name:          slug,
		LayoutVersion: LayoutVersion,
		Created:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestOpen_notExist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	reg, problems := Open(dir)
	if len(problems) != 0 {
		t.Errorf("expected no problems, got %v", problems)
	}
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if len(reg.List()) != 0 {
		t.Error("expected empty registry")
	}
}

func TestOpen_emptyNotebooksDir(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "notebooks"), 0755); err != nil {
		t.Fatal(err)
	}
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
	if len(reg.List()) != 0 {
		t.Error("expected empty registry")
	}
}

func TestOpen_validNotebook(t *testing.T) {
	dataDir := t.TempDir()
	slug := "work"
	writeManifest(t, filepath.Join(dataDir, "notebooks", slug), validManifest(slug))

	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
	nbs := reg.List()
	if len(nbs) != 1 {
		t.Fatalf("expected 1 notebook, got %d", len(nbs))
	}
	if nbs[0].Manifest.Slug != slug {
		t.Errorf("slug = %q, want %q", nbs[0].Manifest.Slug, slug)
	}
}

func TestOpen_badSlug(t *testing.T) {
	dataDir := t.TempDir()
	badDir := filepath.Join(dataDir, "notebooks", "Bad_Slug!")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	_, problems := Open(dataDir)
	if len(problems) == 0 {
		t.Error("expected a problem for bad slug, got none")
	}
}

func TestOpen_slugMismatch(t *testing.T) {
	dataDir := t.TempDir()
	slug := "work"
	m := validManifest(slug)
	m.Slug = "other"
	writeManifest(t, filepath.Join(dataDir, "notebooks", slug), m)

	_, problems := Open(dataDir)
	if len(problems) == 0 {
		t.Error("expected a problem for slug mismatch, got none")
	}
}

func TestOpen_futureLayoutVersion(t *testing.T) {
	dataDir := t.TempDir()
	slug := "work"
	m := validManifest(slug)
	m.LayoutVersion = LayoutVersion + 1
	writeManifest(t, filepath.Join(dataDir, "notebooks", slug), m)

	_, problems := Open(dataDir)
	if len(problems) == 0 {
		t.Error("expected a problem for unsupported layout version, got none")
	}
}

func TestOpen_sorted(t *testing.T) {
	dataDir := t.TempDir()
	for _, slug := range []string{"zebra", "alpha", "mango"} {
		writeManifest(t, filepath.Join(dataDir, "notebooks", slug), validManifest(slug))
	}
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	nbs := reg.List()
	order := []string{"alpha", "mango", "zebra"}
	if len(nbs) != len(order) {
		t.Fatalf("got %d notebooks, want %d", len(nbs), len(order))
	}
	for i, nb := range nbs {
		if nb.Manifest.Slug != order[i] {
			t.Errorf("[%d] slug = %q, want %q", i, nb.Manifest.Slug, order[i])
		}
	}
}

func TestOpen_ignoresDotDirs(t *testing.T) {
	dataDir := t.TempDir()
	hidden := filepath.Join(dataDir, "notebooks", ".hidden")
	if err := os.MkdirAll(hidden, 0755); err != nil {
		t.Fatal(err)
	}
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
	if len(reg.List()) != 0 {
		t.Error("expected empty registry — hidden dir should be skipped")
	}
}

func TestOpen_ignoresFiles(t *testing.T) {
	dataDir := t.TempDir()
	nbDir := filepath.Join(dataDir, "notebooks")
	if err := os.MkdirAll(nbDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a plain file inside notebooks/ — should be ignored
	if err := os.WriteFile(filepath.Join(nbDir, "readme.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
	if len(reg.List()) != 0 {
		t.Error("expected empty registry — files should be skipped")
	}
}

func TestOpen_badNotebookDoesNotBlockOthers(t *testing.T) {
	dataDir := t.TempDir()
	// Valid notebook
	writeManifest(t, filepath.Join(dataDir, "notebooks", "good"), validManifest("good"))
	// Notebook with a bad manifest (missing file)
	if err := os.MkdirAll(filepath.Join(dataDir, "notebooks", "bad"), 0755); err != nil {
		t.Fatal(err)
	}

	reg, problems := Open(dataDir)
	if len(problems) == 0 {
		t.Error("expected at least one problem for the bad notebook")
	}
	nbs := reg.List()
	if len(nbs) != 1 || nbs[0].Manifest.Slug != "good" {
		t.Errorf("expected only 'good' notebook in registry, got %v", nbs)
	}
}

func TestRegistry_Get(t *testing.T) {
	dataDir := t.TempDir()
	slug := "work"
	writeManifest(t, filepath.Join(dataDir, "notebooks", slug), validManifest(slug))
	reg, _ := Open(dataDir)

	nb, ok := reg.Get(slug)
	if !ok || nb == nil {
		t.Fatalf("Get(%q) = %v, %v; want non-nil, true", slug, nb, ok)
	}
	if nb.Manifest.Slug != slug {
		t.Errorf("slug = %q, want %q", nb.Manifest.Slug, slug)
	}

	_, ok = reg.Get("missing")
	if ok {
		t.Error("Get(\"missing\") returned true, want false")
	}
}

func TestRegistry_List_empty(t *testing.T) {
	dataDir := t.TempDir()
	reg, _ := Open(dataDir)
	if nbs := reg.List(); len(nbs) != 0 {
		t.Errorf("List() = %d notebooks, want 0", len(nbs))
	}
}

func TestOpen_trailingHyphenSlug(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "notebooks", "bad-"), 0755); err != nil {
		t.Fatal(err)
	}
	_, problems := Open(dataDir)
	if len(problems) == 0 {
		t.Error("expected a problem for trailing-hyphen slug, got none")
	}
}

func TestOpen_singleCharSlug(t *testing.T) {
	dataDir := t.TempDir()
	slug := "a"
	writeManifest(t, filepath.Join(dataDir, "notebooks", slug), validManifest(slug))
	reg, problems := Open(dataDir)
	if len(problems) != 0 {
		t.Errorf("unexpected problems for single-char slug: %v", problems)
	}
	if len(reg.List()) != 1 {
		t.Errorf("expected 1 notebook, got %d", len(reg.List()))
	}
}

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, problems := Open(t.TempDir())
	if len(problems) != 0 {
		t.Fatalf("Open problems: %v", problems)
	}
	return reg
}

func TestCreate(t *testing.T) {
	reg := newRegistry(t)

	nb, err := reg.Create("work", "Work notes")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if nb.Manifest.Slug != "work" || nb.Manifest.Name != "Work notes" {
		t.Errorf("manifest = %+v", nb.Manifest)
	}
	if nb.Manifest.ID == "" || nb.Manifest.Profile != "plaintext" || nb.Manifest.LayoutVersion != LayoutVersion {
		t.Errorf("manifest defaults missing: %+v", nb.Manifest)
	}
	for _, d := range nb.Layout.Dirs() {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("dir %s missing: %v", d, err)
		}
	}
	if _, err := os.Stat(nb.Layout.Manifest()); err != nil {
		t.Errorf("manifest file missing: %v", err)
	}
	if got := len(reg.List()); got != 1 {
		t.Errorf("registry has %d notebooks, want 1", got)
	}

	// Empty name defaults to the slug.
	nb2, err := reg.Create("solo", "")
	if err != nil {
		t.Fatalf("Create empty name: %v", err)
	}
	if nb2.Manifest.Name != "solo" {
		t.Errorf("name = %q, want %q", nb2.Manifest.Name, "solo")
	}

	// Duplicates and bad slugs.
	if _, err := reg.Create("work", ""); !errors.Is(err, ErrSlugExists) {
		t.Errorf("duplicate = %v, want ErrSlugExists", err)
	}
	if _, err := reg.Create("Bad_Slug", ""); !errors.Is(err, ErrBadSlug) {
		t.Errorf("bad slug = %v, want ErrBadSlug", err)
	}
	if _, err := reg.Create("-lead", ""); !errors.Is(err, ErrBadSlug) {
		t.Errorf("leading dash = %v, want ErrBadSlug", err)
	}
}

func TestCreate_survivesRestart(t *testing.T) {
	root := t.TempDir()
	reg, _ := Open(root)
	if _, err := reg.Create("keep", "Keep"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg2, problems := Open(root)
	if len(problems) != 0 {
		t.Fatalf("reopen problems: %v", problems)
	}
	nb, ok := reg2.Get("keep")
	if !ok || nb.Manifest.Name != "Keep" {
		t.Fatalf("reopened notebook = %+v, %v", nb, ok)
	}
}

func TestRename(t *testing.T) {
	root := t.TempDir()
	reg, _ := Open(root)
	if _, err := reg.Create("old-name", "Old"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	nb, err := reg.Rename("old-name", "new-name")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if nb.Manifest.Slug != "new-name" {
		t.Errorf("slug = %q, want new-name", nb.Manifest.Slug)
	}
	if nb.Manifest.Name != "Old" {
		t.Errorf("name changed on rename: %q", nb.Manifest.Name)
	}
	if _, ok := reg.Get("old-name"); ok {
		t.Error("old slug still in registry")
	}
	if _, ok := reg.Get("new-name"); !ok {
		t.Error("new slug missing from registry")
	}
	if _, err := os.Stat(filepath.Join(root, "notebooks", "new-name", ManifestFile)); err != nil {
		t.Errorf("renamed dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notebooks", "old-name")); !os.IsNotExist(err) {
		t.Error("old dir survived rename")
	}

	// The manifest slug key followed the rename on disk.
	var m Manifest
	if _, err := toml.DecodeFile(filepath.Join(root, "notebooks", "new-name", ManifestFile), &m); err != nil {
		t.Fatal(err)
	}
	if m.Slug != "new-name" {
		t.Errorf("disk manifest slug = %q, want new-name", m.Slug)
	}

	// Rename survives restart.
	reg2, problems := Open(root)
	if len(problems) != 0 {
		t.Fatalf("reopen problems: %v", problems)
	}
	if _, ok := reg2.Get("new-name"); !ok {
		t.Error("renamed slug missing after restart")
	}

	// Error cases.
	if _, err := reg.Rename("new-name", "old-name"); err != nil {
		t.Errorf("rename back: %v", err)
	}
	if _, err := reg.Rename("ghost", "x"); !errors.Is(err, ErrNoNotebook) {
		t.Errorf("missing = %v, want ErrNoNotebook", err)
	}
	if _, err := reg.Create("taken", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Rename("old-name", "taken"); !errors.Is(err, ErrSlugExists) {
		t.Errorf("collision = %v, want ErrSlugExists", err)
	}
	if _, err := reg.Rename("old-name", "Bad_Slug"); !errors.Is(err, ErrBadSlug) {
		t.Errorf("bad target = %v, want ErrBadSlug", err)
	}
	// Same slug is a no-op, not an error.
	if nb, err := reg.Rename("old-name", "old-name"); err != nil || nb.Manifest.Slug != "old-name" {
		t.Errorf("self-rename = %v, %v", nb, err)
	}
}

func TestDelete(t *testing.T) {
	root := t.TempDir()
	reg, _ := Open(root)
	if _, err := reg.Create("gone", "Bye"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	archived, err := reg.Delete("gone")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := reg.Get("gone"); ok {
		t.Error("deleted slug still in registry")
	}
	if got := len(reg.List()); got != 0 {
		t.Errorf("registry has %d notebooks, want 0", got)
	}
	fi, err := os.Stat(archived)
	if err != nil || !fi.IsDir() {
		t.Fatalf("archive missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archived, ManifestFile)); err != nil {
		t.Errorf("archive has no manifest: %v", err)
	}

	// The archive hides from a restart, but the slug is free to reuse.
	reg2, problems := Open(root)
	if len(problems) != 0 {
		t.Fatalf("reopen problems: %v", problems)
	}
	if got := len(reg2.List()); got != 0 {
		t.Errorf("archive leaked into registry: %d notebooks", got)
	}
	if _, err := reg2.Create("gone", "Again"); err != nil {
		t.Errorf("slug not reusable after archive: %v", err)
	}

	if _, err := reg.Delete("ghost"); !errors.Is(err, ErrNoNotebook) {
		t.Errorf("missing = %v, want ErrNoNotebook", err)
	}
}
