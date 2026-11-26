package folderprefs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoad_missingFile(t *testing.T) {
	prefs, err := Load(filepath.Join(t.TempDir(), "folder-prefs.json"))
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if len(prefs) != 0 {
		t.Errorf("Load() = %v, want empty", prefs)
	}
}

func TestLoad_corruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	if err := save(path, map[string]Pref{"work": {Emoji: "💼"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prefs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v, want nil (corrupt degrades to empty)", err)
	}
	if len(prefs) != 0 {
		t.Errorf("Load() = %v, want empty on corrupt file", prefs)
	}
}

func TestLoad_readErrorOtherThanNotExist(t *testing.T) {
	// A directory where a file is expected: ReadFile fails with
	// something other than "not exist".
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Error("Load() on a directory err = nil, want an error")
	}
}

func TestSet_mkdirFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// blocker is a regular file, so MkdirAll(blocker/sub) must fail.
	path := filepath.Join(blocker, "sub", "folder-prefs.json")
	if _, err := Set(path, "f", Pref{Emoji: "x"}); err == nil {
		t.Error("Set() with an unmakeable directory err = nil, want an error")
	}
}

func TestSet_createTempFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folder-prefs.json")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	if _, err := Set(path, "f", Pref{Emoji: "x"}); err == nil {
		t.Error("Set() in a read-only directory err = nil, want an error")
	}
}

func TestSet_storeAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")

	prefs, err := Set(path, "work", Pref{Emoji: "💼", Color: "#aabbcc"})
	if err != nil {
		t.Fatalf("Set() err = %v", err)
	}
	if prefs["work"] != (Pref{Emoji: "💼", Color: "#aabbcc"}) {
		t.Errorf("Set() = %v", prefs)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if loaded["work"] != (Pref{Emoji: "💼", Color: "#aabbcc"}) {
		t.Errorf("Load() after Set = %v", loaded)
	}

	// Both fields empty clears the entry rather than storing a blank row.
	prefs, err = Set(path, "work", Pref{})
	if err != nil {
		t.Fatalf("Set() clear err = %v", err)
	}
	if _, ok := prefs["work"]; ok {
		t.Errorf("Set() with empty pref left an entry: %v", prefs)
	}
}

func TestSet_concurrentWritesDoNotLoseUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		folder := "folder" + string(rune('a'+i))
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			if _, err := Set(path, folder, Pref{Emoji: "x"}); err != nil {
				t.Errorf("Set(%s) err = %v", folder, err)
			}
		}(folder)
	}
	wg.Wait()

	prefs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if len(prefs) != 20 {
		t.Errorf("Load() has %d entries, want 20 (lost a concurrent write): %v", len(prefs), prefs)
	}
}

func TestRenamePrefix_movesFolderAndChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	mustSet(t, path, "old", Pref{Emoji: "O"})
	mustSet(t, path, "old/sub", Pref{Emoji: "S"})
	mustSet(t, path, "older", Pref{Emoji: "X"}) // sibling with a shared prefix, must not move

	if err := RenamePrefix(path, "old", "new/place"); err != nil {
		t.Fatalf("RenamePrefix() err = %v", err)
	}
	prefs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	want := map[string]Pref{
		"new/place":     {Emoji: "O"},
		"new/place/sub": {Emoji: "S"},
		"older":         {Emoji: "X"},
	}
	if len(prefs) != len(want) {
		t.Fatalf("RenamePrefix() = %v, want %v", prefs, want)
	}
	for k, v := range want {
		if prefs[k] != v {
			t.Errorf("RenamePrefix()[%s] = %v, want %v", k, prefs[k], v)
		}
	}
}

func TestRenamePrefix_loadErrorPropagates(t *testing.T) {
	dir := t.TempDir() // a directory, not a file: Load fails reading it
	if err := RenamePrefix(dir, "a", "b"); err == nil {
		t.Error("RenamePrefix() err = nil, want an error")
	}
}

func TestRenamePrefix_missingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	if err := RenamePrefix(path, "a", "b"); err != nil {
		t.Fatalf("RenamePrefix() on missing file err = %v, want nil", err)
	}
}

func TestDeletePrefix_dropsSubtreeOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	mustSet(t, path, "gone", Pref{Emoji: "G"})
	mustSet(t, path, "gone/child", Pref{Emoji: "C"})
	mustSet(t, path, "gone2", Pref{Emoji: "K"})

	if err := DeletePrefix(path, "gone"); err != nil {
		t.Fatalf("DeletePrefix() err = %v", err)
	}
	prefs, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if len(prefs) != 1 || prefs["gone2"] != (Pref{Emoji: "K"}) {
		t.Errorf("DeletePrefix() left = %v, want only gone2", prefs)
	}
}

func TestDeletePrefix_loadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	if err := DeletePrefix(dir, "a"); err == nil {
		t.Error("DeletePrefix() err = nil, want an error")
	}
}

func TestDeletePrefix_missingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-prefs.json")
	if err := DeletePrefix(path, "a"); err != nil {
		t.Fatalf("DeletePrefix() on missing file err = %v, want nil", err)
	}
}

func mustSet(t *testing.T, path, folder string, pref Pref) {
	t.Helper()
	if _, err := Set(path, folder, pref); err != nil {
		t.Fatalf("Set(%s) err = %v", folder, err)
	}
}
