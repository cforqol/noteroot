package notebooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var (
	ErrBadSlug       = errors.New("invalid slug")
	ErrLayoutVersion = errors.New("unsupported layout version")
	ErrNoNotebook    = errors.New("no such notebook")
	ErrBadRetention  = errors.New("retention out of range")
)

// Trash retention. A notebook that does not say otherwise keeps trashed
// notes for DefaultTrashRetentionDays; RetentionNever disables the sweep
// so nothing is ever deleted without the user asking.
const (
	DefaultTrashRetentionDays = 30
	RetentionNever            = 0
	MaxTrashRetentionDays     = 3650
)

const trashRetentionKey = "trash_retention_days"

type Manifest struct {
	ID            string    `toml:"id"`
	Slug          string    `toml:"slug"`
	Name          string    `toml:"name"`
	Profile       string    `toml:"profile"`
	LayoutVersion int       `toml:"layout_version"`
	QuotaMB       int       `toml:"quota_mb"`
	Created       time.Time `toml:"created"`

	// TrashRetentionDays is nil when the manifest is silent (use the
	// default) and 0 when the user chose "never". The pointer is what
	// keeps those two cases apart.
	TrashRetentionDays *int `toml:"trash_retention_days,omitempty"`
}

// RetentionDays is the notebook's effective trash retention in days.
// RetentionNever (0) means trashed notes are only ever removed when
// somebody asks for it.
func (m Manifest) RetentionDays() int {
	if m.TrashRetentionDays == nil {
		return DefaultTrashRetentionDays
	}
	if *m.TrashRetentionDays <= 0 {
		return RetentionNever
	}
	return *m.TrashRetentionDays
}

// TrashCutoff is the deadline a purge run compares against: entries
// trashed before it are due. ok is false when retention is "never", in
// which case there is nothing to sweep.
func (m Manifest) TrashCutoff(now time.Time) (cutoff time.Time, ok bool) {
	days := m.RetentionDays()
	if days == RetentionNever {
		return time.Time{}, false
	}
	return now.AddDate(0, 0, -days), true
}

type Notebook struct {
	Manifest Manifest
	Layout   Layout
}

type Registry struct {
	mu    sync.RWMutex
	root  string
	byS   map[string]*Notebook
	order []string
}

// Open never fails on a single bad notebook: one broken notebook
// must not take down the app.
func Open(dataDir string) (*Registry, []error) {
	r := &Registry{root: filepath.Join(dataDir, "notebooks"), byS: map[string]*Notebook{}}
	var problems []error

	entries, err := os.ReadDir(r.root)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return r, []error{err}
	}

	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		slug := e.Name()
		if !slugRe.MatchString(slug) {
			problems = append(problems, fmt.Errorf("%s: %w", slug, ErrBadSlug))
			continue
		}
		nb, err := load(Layout{root: filepath.Join(r.root, slug)})
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", slug, err))
			continue
		}
		if nb.Manifest.Slug != slug {
			problems = append(problems, fmt.Errorf("%s: manifest slug is %q", slug, nb.Manifest.Slug))
			continue
		}
		r.byS[slug] = nb
		r.order = append(r.order, slug)
	}
	sort.Strings(r.order)
	return r, problems
}

func load(l Layout) (*Notebook, error) {
	var m Manifest
	if _, err := toml.DecodeFile(l.Manifest(), &m); err != nil {
		return nil, err
	}
	if m.LayoutVersion > LayoutVersion {
		return nil, fmt.Errorf("%w: %d > %d", ErrLayoutVersion, m.LayoutVersion, LayoutVersion)
	}
	return &Notebook{Manifest: m, Layout: l}, nil
}

func (r *Registry) Get(slug string) (*Notebook, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nb, ok := r.byS[slug]
	return nb, ok
}

func (r *Registry) List() []*Notebook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Notebook, 0, len(r.order))
	for _, s := range r.order {
		out = append(out, r.byS[s])
	}
	return out
}

// SetTrashRetention persists a notebook's trash retention (days, or
// RetentionNever for "keep forever") and publishes the updated notebook.
//
// The notebook is replaced rather than mutated in place: callers hold
// *Notebook pointers for the length of a request, and editing the
// Manifest under them would be a data race. Readers that already have
// the old pointer simply finish with the old value.
func (r *Registry) SetTrashRetention(slug string, days int) (*Notebook, error) {
	if days < 0 || days > MaxTrashRetentionDays {
		return nil, ErrBadRetention
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.byS[slug]
	if !ok {
		return nil, ErrNoNotebook
	}
	if err := writeManifestKey(old.Layout, trashRetentionKey, int64(days)); err != nil {
		return nil, err
	}
	m := old.Manifest
	d := days
	m.TrashRetentionDays = &d
	nb := &Notebook{Manifest: m, Layout: old.Layout}
	r.byS[slug] = nb
	return nb, nil
}

// writeManifestKey sets one key in notebook.toml, atomically.
//
// The file is round-tripped through a map so keys this build does not
// know about — hand-added, or from a newer version — survive the
// rewrite; encoding the Manifest struct instead would silently drop
// them. The temp file lives in the notebook directory so the rename
// stays on one filesystem.
func writeManifestKey(l Layout, key string, value any) error {
	raw := map[string]any{}
	if _, err := toml.DecodeFile(l.Manifest(), &raw); err != nil {
		return err
	}
	raw[key] = value

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return err
	}
	f, err := os.CreateTemp(l.Root(), ".notebook.toml-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(buf.Bytes()); err != nil {
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
	return os.Rename(tmp, l.Manifest())
}
