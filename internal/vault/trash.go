package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrOccupied reports a restore whose original path is reoccupied.
var ErrOccupied = errors.New("target occupied")

// TrashEntry records one trashed note. The JSON sidecar next to the trashed
// file is the record; the trashed filename alone carries no meaning.
type TrashEntry struct {
	Name      string    `json:"name"`
	OrigPath  string    `json:"origPath"`
	Actor     string    `json:"actor"`
	Sha       string    `json:"sha"`
	Size      int64     `json:"size"`
	DeletedAt time.Time `json:"deletedAt"`
}

// Trash is a per-notebook soft-delete area. Files are moved, never copied;
// restore moves them back. The trash directory lives outside the vault, so
// tree walks and the index never see trashed notes.
type Trash struct {
	vault *Vault
	dir   string
}

func OpenTrash(v *Vault, dir string) *Trash { return &Trash{vault: v, dir: dir} }

// Put moves a note — or every note under a directory prefix — to trash.
// A BaseSha mismatch fails the single-file case with ErrConflict; directory
// prefixes move unconditionally. Returns one entry per moved note.
func (t *Trash) Put(rel string, opts WriteOpts) ([]TrashEntry, error) {
	clean, err := Rel(rel)
	if err != nil {
		return nil, err
	}
	mu := stripeFor(clean)
	mu.Lock()
	defer mu.Unlock()

	root, err := filepath.EvalSymlinks(t.vault.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(clean))); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	parent, err := t.vault.resolveParent(clean)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(parent, path.Base(clean))

	fi, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return t.putDir(root, target, clean, opts.Actor)
	}

	if opts.BaseSha != "" {
		cur, err := os.ReadFile(target)
		if err != nil {
			return nil, ErrConflict
		}
		if shaHex(cur) != opts.BaseSha {
			return nil, ErrConflict
		}
	}
	e, err := t.move(target, clean, opts.Actor)
	if err != nil {
		return nil, err
	}
	return []TrashEntry{e}, nil
}

func (t *Trash) putDir(root, abs, clean, actor string) ([]TrashEntry, error) {
	var files []string
	err := filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		relOS, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if Ignored(filepath.ToSlash(relOS)) {
			return nil
		}
		if info, err := d.Info(); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, ErrNotFound
	}
	var out []TrashEntry
	for _, f := range files {
		relOS, err := filepath.Rel(root, f)
		if err != nil {
			return out, err
		}
		e, err := t.move(f, filepath.ToSlash(relOS), actor)
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

// trashMoveMu serializes the pick-a-name + rename in move. Trash handles
// are created per request, so a per-struct mutex cannot help; and a Put on
// a parent prefix holds a different stripe than a concurrent Put on a
// child, so stripes cannot cover the collision check either. Trash moves
// are rare; process-wide serialization is cheap. It is always a leaf lock:
// callers may hold a stripe while taking it, never the reverse.
var trashMoveMu sync.Mutex

func (t *Trash) move(abs, rel, actor string) (TrashEntry, error) {
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return TrashEntry{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return TrashEntry{}, err
	}
	sum, err := shaFile(abs)
	if err != nil {
		return TrashEntry{}, err
	}
	name := strings.ReplaceAll(rel, "/", "_")
	e := TrashEntry{
		Name:      name,
		OrigPath:  rel,
		Actor:     actor,
		Sha:       sum,
		Size:      fi.Size(),
		DeletedAt: time.Now(),
	}
	// A same-second double delete of one path must not collide.
	base := strings.ReplaceAll(rel, "/", "_")
	trashMoveMu.Lock()
	defer trashMoveMu.Unlock()
	for i := 1; ; i++ {
		if _, err := os.Lstat(filepath.Join(t.dir, e.Name)); os.IsNotExist(err) {
			break
		} else if err != nil {
			return TrashEntry{}, err
		}
		e.Name = base + "-" + strconv.Itoa(i)
	}
	if err := os.Rename(abs, filepath.Join(t.dir, e.Name)); err != nil {
		return TrashEntry{}, err
	}
	if err := writeSidecar(filepath.Join(t.dir, e.Name+".json"), e); err != nil {
		_ = os.Rename(filepath.Join(t.dir, e.Name), abs)
		return TrashEntry{}, err
	}
	return e, nil
}

func shaFile(abs string) (string, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return shaHex(b), nil
}

func writeSidecar(p string, e TrashEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// List returns trash entries newest first. Files without a readable sidecar
// are listed with best-effort metadata and an empty OrigPath.
func (t *Trash) List() ([]TrashEntry, error) {
	files, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []TrashEntry
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		out = append(out, t.entry(f.Name()))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeletedAt.Equal(out[j].DeletedAt) {
			return out[i].Name > out[j].Name
		}
		return out[i].DeletedAt.After(out[j].DeletedAt)
	})
	return out, nil
}

// entry reads one trashed note's record. A note without a readable
// sidecar is described from the file itself, with an empty OrigPath.
func (t *Trash) entry(name string) TrashEntry {
	e := TrashEntry{Name: name}
	if b, err := os.ReadFile(filepath.Join(t.dir, name+".json")); err == nil {
		_ = json.Unmarshal(b, &e)
		e.Name = name
	} else if fi, err := os.Stat(filepath.Join(t.dir, name)); err == nil {
		e.Size = fi.Size()
		e.DeletedAt = fi.ModTime()
	}
	return e
}

// validName reports whether name can only refer to a note this trash
// directory owns: a bare file name, as List reports them.
func validName(name string) bool {
	return name != "" && name == path.Base(name) && strings.HasSuffix(name, ".md")
}

// Read returns a trashed note's record and contents, for previewing it
// without restoring it.
func (t *Trash) Read(name string) (TrashEntry, []byte, error) {
	if !validName(name) {
		return TrashEntry{}, nil, errors.New("invalid trash name")
	}
	b, err := os.ReadFile(filepath.Join(t.dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return TrashEntry{}, nil, ErrNotFound
		}
		return TrashEntry{}, nil, err
	}
	return t.entry(name), b, nil
}

// RestoreSkip is a note a bulk restore left in the trash, and why.
type RestoreSkip struct {
	Name     string
	OrigPath string
	Err      error
}

// within reports whether rel is folder or lies inside it; the empty
// folder is the whole notebook.
func within(rel, folder string) bool {
	return folder == "" || rel == folder || strings.HasPrefix(rel, folder+"/")
}

// RestoreUnder restores every trashed note that came from folder (the
// empty folder restores the whole trash), recreating folders on the
// way. Newest first: when one path was trashed twice, the latest
// version comes back and the older one stays, reported as occupied, as
// does any note whose path has been taken since. err is set only when
// the trash cannot be read at all.
func (t *Trash) RestoreUnder(folder string) (restored []string, skipped []RestoreSkip, err error) {
	if folder, err = cleanFolder(folder); err != nil {
		return nil, nil, err
	}
	entries, err := t.List()
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.OrigPath == "" || !within(e.OrigPath, folder) {
			continue
		}
		rel, err := t.Restore(e.Name)
		if err != nil {
			skipped = append(skipped, RestoreSkip{Name: e.Name, OrigPath: e.OrigPath, Err: err})
			continue
		}
		restored = append(restored, rel)
	}
	return restored, skipped, nil
}

// DeleteUnder removes every trashed note that came from folder for good.
func (t *Trash) DeleteUnder(folder string) (int, error) {
	folder, err := cleanFolder(folder)
	if err != nil {
		return 0, err
	}
	return t.removeWhere(func(e TrashEntry) bool {
		return e.OrigPath != "" && within(e.OrigPath, folder)
	})
}

func cleanFolder(folder string) (string, error) {
	if folder == "" {
		return "", nil
	}
	return Rel(folder)
}

// Restore moves a trashed note back to its original path.
func (t *Trash) Restore(name string) (string, error) {
	if name == "" || name != path.Base(name) {
		return "", errors.New("invalid trash name")
	}
	src := filepath.Join(t.dir, name)
	b, err := os.ReadFile(src + ".json")
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	var e TrashEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return "", err
	}
	clean, err := Rel(e.OrigPath)
	if err != nil || clean == "" {
		return "", errors.New("no origin recorded")
	}
	root, err := filepath.EvalSymlinks(t.vault.root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(clean))); err == nil {
		return "", ErrOccupied
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := t.vault.resolveParent(clean)
	if err != nil {
		return "", err
	}
	// Hold the destination stripe across the occupied check and the rename
	// so a concurrent Write or Restore to the same path cannot slip in
	// between and lose a note.
	mu := stripeFor(clean)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Lstat(filepath.Join(parent, path.Base(clean))); err == nil {
		return "", ErrOccupied
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(src, filepath.Join(parent, path.Base(clean))); err != nil {
		return "", err
	}
	_ = os.Remove(src + ".json")
	return clean, nil
}

// Purge deletes entries older than the cutoff. Per-file errors are collected
// and returned joined; a purge failure must never abort the sweep.
func (t *Trash) Purge(olderThan time.Time) (int, error) {
	return t.removeWhere(func(e TrashEntry) bool { return e.DeletedAt.Before(olderThan) })
}

// Empty deletes every trashed note for good.
func (t *Trash) Empty() (int, error) {
	return t.removeWhere(func(TrashEntry) bool { return true })
}

// Delete removes a single trashed note and its sidecar for good. The name
// is matched against the listing rules, so it can only ever name a file
// this trash directory owns.
func (t *Trash) Delete(name string) error {
	if !validName(name) {
		return errors.New("invalid trash name")
	}
	if err := os.Remove(filepath.Join(t.dir, name)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	_ = os.Remove(filepath.Join(t.dir, name+".json"))
	return nil
}

// removeWhere deletes the entries the predicate selects. Per-file errors
// are collected and returned joined: one unreadable entry must never
// strand the rest.
func (t *Trash) removeWhere(due func(TrashEntry) bool) (int, error) {
	entries, err := t.List()
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, e := range entries {
		if !due(e) {
			continue
		}
		if err := os.Remove(filepath.Join(t.dir, e.Name)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		_ = os.Remove(filepath.Join(t.dir, e.Name+".json"))
		n++
	}
	return n, errors.Join(errs...)
}
