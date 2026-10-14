package notebooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var (
	ErrBadSlug       = errors.New("invalid slug")
	ErrLayoutVersion = errors.New("unsupported layout version")
)

type Manifest struct {
	ID            string    `toml:"id"`
	Slug          string    `toml:"slug"`
	Name          string    `toml:"name"`
	Profile       string    `toml:"profile"`
	LayoutVersion int       `toml:"layout_version"`
	QuotaMB       int       `toml:"quota_mb"`
	Created       time.Time `toml:"created"`
}

type Notebook struct {
	Manifest Manifest
	Layout   Layout
}

type Registry struct {
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
	nb, ok := r.byS[slug]
	return nb, ok
}

func (r *Registry) List() []*Notebook {
	out := make([]*Notebook, 0, len(r.order))
	for _, s := range r.order {
		out = append(out, r.byS[s])
	}
	return out
}
