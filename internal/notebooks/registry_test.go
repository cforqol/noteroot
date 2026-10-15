package notebooks

import (
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
