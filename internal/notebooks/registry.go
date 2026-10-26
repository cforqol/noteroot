package notebooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"noteroot/internal/frontmatter"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var (
	ErrBadSlug       = errors.New("invalid slug")
	ErrLayoutVersion = errors.New("unsupported layout version")
	ErrNoNotebook    = errors.New("no such notebook")
	ErrBadRetention  = errors.New("retention out of range")
	ErrSlugExists    = errors.New("slug already in use")
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

// Create makes a new notebook: the layout directories, a manifest, and
// an empty vault. The slug doubles as the directory name, so it must
// satisfy slugRe; an empty name defaults to the slug.
func (r *Registry) Create(slug, name string) (*Notebook, error) {
	if !slugRe.MatchString(slug) {
		return nil, ErrBadSlug
	}
	if name == "" {
		name = slug
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byS[slug]; ok {
		return nil, ErrSlugExists
	}
	l := Layout{root: filepath.Join(r.root, slug)}
	if _, err := os.Lstat(l.Root()); err == nil {
		return nil, ErrSlugExists
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for _, d := range l.Dirs() {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	m := Manifest{
		ID:            frontmatter.NewID(),
		Slug:          slug,
		Name:          name,
		Profile:       "plaintext",
		LayoutVersion: LayoutVersion,
		Created:       time.Now().UTC(),
	}
	if err := writeNewManifest(l, m); err != nil {
		return nil, err
	}
	nb := &Notebook{Manifest: m, Layout: l}
	r.byS[slug] = nb
	r.order = append(r.order, slug)
	sort.Strings(r.order)
	return nb, nil
}

// Rename moves a notebook to a new slug: directory, manifest key and
// in-memory entry all follow. Pending runtime state keyed by the old
// slug is the caller's to drop — see httpapi's notebook handlers.
func (r *Registry) Rename(oldSlug, newSlug string) (*Notebook, error) {
	if !slugRe.MatchString(newSlug) {
		return nil, ErrBadSlug
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.byS[oldSlug]
	if !ok {
		return nil, ErrNoNotebook
	}
	if newSlug == oldSlug {
		return old, nil
	}
	if _, taken := r.byS[newSlug]; taken {
		return nil, ErrSlugExists
	}
	newLayout := Layout{root: filepath.Join(r.root, newSlug)}
	if _, err := os.Lstat(newLayout.Root()); err == nil {
		return nil, ErrSlugExists
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(old.Layout.Root(), newLayout.Root()); err != nil {
		return nil, err
	}
	if err := writeManifestKey(newLayout, "slug", newSlug); err != nil {
		// Roll the directory back so disk and memory stay consistent.
		_ = os.Rename(newLayout.Root(), old.Layout.Root())
		return nil, err
	}
	m := old.Manifest
	m.Slug = newSlug
	nb := &Notebook{Manifest: m, Layout: newLayout}
	delete(r.byS, oldSlug)
	r.byS[newSlug] = nb
	for i, s := range r.order {
		if s == oldSlug {
			r.order[i] = newSlug
			break
		}
	}
	sort.Strings(r.order)
	return nb, nil
}

// Delete archives a notebook: the directory moves under the hidden
// notebooks/.archive/ dir, which Open skips, so nothing is erased —
// recovery is a rename back. Returns the archive path.
func (r *Registry) Delete(slug string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nb, ok := r.byS[slug]
	if !ok {
		return "", ErrNoNotebook
	}
	archiveDir := filepath.Join(r.root, ".archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(archiveDir, slug+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	if err := os.Rename(nb.Layout.Root(), dst); err != nil {
		return "", err
	}
	delete(r.byS, slug)
	for i, s := range r.order {
		if s == slug {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return dst, nil
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

// writeNewManifest writes a brand-new manifest. Unlike writeManifestKey
// there is no existing file to preserve, so the struct is encoded
// directly.
func writeNewManifest(l Layout, m Manifest) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
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
