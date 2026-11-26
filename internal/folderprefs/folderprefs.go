// Package folderprefs persists the per-folder emoji and name color a
// user assigns from the sidebar, as the notebook's own server-side
// state rather than the browser's localStorage, so the customization
// follows the user to any device or browser.
//
// This lives in its own JSON file rather than a state.db table on
// purpose: a folder's emoji/color has no vault-derived source, and
// state.db's rebuild path (docs/adr/0002-state-db-is-rebuildable.md)
// discards anything that cannot be reconstructed.
package folderprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Pref is one folder's user-assigned emoji and name color.
type Pref struct {
	Emoji string `json:"emoji"`
	Color string `json:"color"`
}

// fileLocks serializes load-modify-save cycles per file path, the same
// shape as state.lockOpen: two concurrent writers to the same
// notebook's file must not race a read-modify-write and drop one's
// change. Lockers of *different* paths never wait on each other.
var (
	fileLocksMu sync.Mutex
	fileLocks   = map[string]*sync.Mutex{}
)

func lockFile(path string) (unlock func()) {
	fileLocksMu.Lock()
	mu, ok := fileLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		fileLocks[path] = mu
	}
	fileLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Load reads every folder's decoration, keyed by folder path. A
// missing or corrupt file degrades to empty rather than erroring —
// the same best-effort rule the browser-side version followed for a
// corrupt localStorage entry.
func Load(path string) (map[string]Pref, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]Pref{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out map[string]Pref
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]Pref{}, nil
	}
	return out, nil
}

// save writes prefs atomically: a temp file in the same directory,
// fsynced, then renamed over the target.
func save(path string, prefs map[string]Pref) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".folder-prefs-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Set stores (or, when both fields are empty, clears) one folder's
// decoration. Returns every folder's decoration after the change.
func Set(path, folder string, pref Pref) (map[string]Pref, error) {
	unlock := lockFile(path)
	defer unlock()
	prefs, err := Load(path)
	if err != nil {
		return nil, err
	}
	if pref.Emoji == "" && pref.Color == "" {
		delete(prefs, folder)
	} else {
		prefs[folder] = pref
	}
	if err := save(path, prefs); err != nil {
		return nil, err
	}
	return prefs, nil
}

// RenamePrefix moves a folder's decoration — and every descendant's —
// after the folder was renamed or moved. A missing file is a no-op.
func RenamePrefix(path, fromDir, toDir string) error {
	unlock := lockFile(path)
	defer unlock()
	prefs, err := Load(path)
	if err != nil {
		return err
	}
	if len(prefs) == 0 {
		return nil
	}
	out := make(map[string]Pref, len(prefs))
	prefix := fromDir + "/"
	for k, v := range prefs {
		switch {
		case k == fromDir:
			out[toDir] = v
		case strings.HasPrefix(k, prefix):
			out[toDir+k[len(fromDir):]] = v
		default:
			out[k] = v
		}
	}
	return save(path, out)
}

// DeletePrefix forgets a folder's decoration — and every descendant's
// — after the folder was deleted. A missing file is a no-op.
func DeletePrefix(path, dir string) error {
	unlock := lockFile(path)
	defer unlock()
	prefs, err := Load(path)
	if err != nil {
		return err
	}
	if len(prefs) == 0 {
		return nil
	}
	out := make(map[string]Pref, len(prefs))
	prefix := dir + "/"
	for k, v := range prefs {
		if k == dir || strings.HasPrefix(k, prefix) {
			continue
		}
		out[k] = v
	}
	return save(path, out)
}
