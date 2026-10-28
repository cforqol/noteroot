package plugins

// Proof-plugin tests: the three plugins shipped in the repo's
// plugins/ directory (theme, word-count, tagger) are loaded from
// disk exactly as a user would install them — copy the directory
// into the notebook, Ensure, exercise commands and hooks against
// the real Lua runtime.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
)

// newRepoFixture copies repo-root plugins/<name> into a fresh
// notebook's plugins directory.
func newRepoFixture(t *testing.T, name string) *fixture {
	t.Helper()
	src := filepath.Join("..", "..", "plugins", name)
	if _, err := os.Stat(filepath.Join(src, ManifestFile)); err != nil {
		t.Fatalf("repo plugin %s missing: %v", name, err)
	}
	layout := notebooks.NewLayout(t.TempDir())
	dst := filepath.Join(layout.Plugins(), name)
	copyTree(t, src, dst)
	store := newFakeStore()
	bus := platform.NewBus("work", testLogger())
	mgr := NewManager(testLogger(), func(string) NoteStore { return store })
	t.Cleanup(func() { mgr.Close(); bus.Close() })
	return &fixture{layout: layout, store: store, bus: bus, mgr: mgr, dir: dst}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runNamed(t *testing.T, f *fixture, plugin, cmd, args string) (any, error) {
	t.Helper()
	return f.mgr.RunCommand(context.Background(), "work", plugin, cmd, []byte(args))
}

func TestProof_tagger(t *testing.T) {
	f := newRepoFixture(t, "tagger")
	f.store.notes["projects/a.md"] = "work in progress\n"
	f.ensure(t)

	f.bus.Publish(platform.Event{
		Type: "note.write", Slug: "work", Path: "projects/a.md",
		Actor: "alice", Origin: "http", Count: 1,
	})
	f.bus.Flush()
	eventually(t, "tagger writes tags", func() bool {
		return strings.Contains(f.store.get("projects/a.md"), "project")
	})

	wr, ok := f.store.lastWrite()
	if !ok {
		t.Fatal("no write recorded")
	}
	if wr.Actor != "plugin:tagger" || wr.Origin != "plugin:tagger" {
		t.Errorf("attribution = %+v", wr)
	}
	if !strings.Contains(wr.Content, "work in progress") {
		t.Errorf("content lost: %q", wr.Content)
	}

	// Second identical event: tags already match, no rewrite.
	before := f.store.writeCount()
	f.bus.Publish(platform.Event{
		Type: "note.write", Slug: "work", Path: "projects/a.md",
		Actor: "alice", Origin: "http", Count: 1,
	})
	f.bus.Flush()
	time.Sleep(700 * time.Millisecond)
	if f.store.writeCount() != before {
		t.Errorf("tagger rewrote unchanged tags: %d -> %d", before, f.store.writeCount())
	}

	// Command: read tags back.
	v, err := runNamed(t, f, "tagger", "tags", `{"path":"projects/a.md"}`)
	if err != nil {
		t.Fatalf("tags command: %v", err)
	}
	m, _ := v.(map[string]any)
	tags, _ := m["tags"].([]any)
	if len(tags) != 1 || tags[0] != "project" {
		t.Errorf("tags result = %v", v)
	}
}

func TestProof_wordCount(t *testing.T) {
	f := newRepoFixture(t, "word-count")
	f.store.notes["a.md"] = "one two three four\n"
	f.ensure(t)

	f.bus.Publish(platform.Event{
		Type: "note.write", Slug: "work", Path: "a.md",
		Actor: "alice", Origin: "http", Count: 1,
	})
	f.bus.Flush()
	eventually(t, "word-count panel published", func() bool {
		panels, err := f.mgr.Panels("work")
		if err != nil {
			return false
		}
		raw, ok := panels["word-count"]
		return ok && strings.Contains(string(raw), "4 words")
	})

	v, err := runNamed(t, f, "word-count", "count", `{"path":"a.md"}`)
	if err != nil {
		t.Fatalf("count command: %v", err)
	}
	m, _ := v.(map[string]any)
	if m["words"] != float64(4) {
		t.Errorf("count result = %v", v)
	}

	if _, err := runNamed(t, f, "word-count", "count", ``); err == nil {
		t.Error("count without args should fail")
	}
}

func TestProof_theme(t *testing.T) {
	f := newRepoFixture(t, "theme")
	f.ensure(t)

	// Default preference applies at load.
	eventually(t, "auto theme panel", func() bool {
		panels, err := f.mgr.Panels("work")
		if err != nil {
			return false
		}
		raw, ok := panels["theme"]
		return ok && strings.Contains(string(raw), `"auto"`)
	})

	if _, err := runNamed(t, f, "theme", "set-theme", `{"theme":"dark"}`); err != nil {
		t.Fatalf("set-theme: %v", err)
	}
	v, err := runNamed(t, f, "theme", "get-theme", ``)
	if err != nil {
		t.Fatalf("get-theme: %v", err)
	}
	if m, _ := v.(map[string]any); m["theme"] != "dark" {
		t.Errorf("get-theme = %v", v)
	}
	panels, err := f.mgr.Panels("work")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(panels["theme"], &spec); err != nil {
		t.Fatal(err)
	}
	if spec["type"] != "theme" || spec["name"] != "dark" {
		t.Errorf("theme panel = %v", spec)
	}

	// Preference survives a reload (kv in platform.db).
	if err := f.mgr.Reload("work", f.layout, f.bus); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dark restored after reload", func() bool {
		panels, err := f.mgr.Panels("work")
		if err != nil {
			return false
		}
		return strings.Contains(string(panels["theme"]), `"dark"`)
	})

	if _, err := runNamed(t, f, "theme", "set-theme", `{"theme":"neon"}`); err == nil {
		t.Error("invalid theme should fail")
	}
}
