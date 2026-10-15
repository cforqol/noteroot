package vault

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrNotFile  = errors.New("not a regular file")
	ErrTooLarge = errors.New("file too large")
	ErrEscapes  = errors.New("path escapes vault")
)

const MaxReadBytes = 8 << 20

type Vault struct{ root string }

func Open(root string) *Vault { return &Vault{root: root} }

func (v *Vault) Root() string { return v.root }

// Stat returns size and modtime for a vault-relative path.
func (v *Vault) Stat(rel string) (int64, time.Time, error) {
	clean, err := Rel(rel)
	if err != nil {
		return 0, time.Time{}, err
	}
	abs, err := v.resolve(clean)
	if err != nil {
		return 0, time.Time{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, time.Time{}, ErrNotFound
		}
		return 0, time.Time{}, err
	}
	return fi.Size(), fi.ModTime(), nil
}

// Read returns the contents of a vault-relative path.
func (v *Vault) Read(rel string) ([]byte, error) {
	clean, err := Rel(rel)
	if err != nil {
		return nil, err
	}
	abs, err := v.resolve(clean)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, ErrNotFile
	}
	if fi.Size() > MaxReadBytes {
		return nil, ErrTooLarge
	}
	return os.ReadFile(abs)
}

// resolve maps a cleaned vault-relative path to an absolute path and
// rejects anything whose symlinks lead outside the vault root.
func (v *Vault) resolve(clean string) (string, error) {
	root, err := filepath.EvalSymlinks(v.root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	abs, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(clean)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", ErrEscapes
	}
	return abs, nil
}

type Node struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	IsDir    bool    `json:"isDir"`
	Size     int64   `json:"size,omitempty"`
	ModTime  int64   `json:"modTime,omitempty"`
	Children []*Node `json:"children,omitempty"`
}

// parentKey normalizes path.Dir so the vault root keys on "".
func parentKey(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

// Tree walks the vault and returns a nested structure.
// Directories sort before files, then case-insensitively by name.
func (v *Vault) Tree() (*Node, error) {
	root := &Node{IsDir: true}
	byPath := map[string]*Node{"": root}

	err := filepath.WalkDir(v.root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == v.root {
				// The vault root itself is gone or unreadable: a silent empty
				// tree would masquerade as an empty notebook.
				if os.IsNotExist(err) {
					return ErrNotFound
				}
				return err
			}
			return nil // one bad entry must not abort the walk
		}
		relOS, err := filepath.Rel(v.root, abs)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relOS)
		if rel == "." {
			return nil
		}
		if Ignored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		parent := byPath[parentKey(rel)]
		if parent == nil {
			return nil
		}

		if d.IsDir() {
			n := &Node{Name: d.Name(), Path: rel, IsDir: true}
			parent.Children = append(parent.Children, n)
			byPath[rel] = n
			return nil
		}
		if !IsNote(rel) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		parent.Children = append(parent.Children, &Node{
			Name:    d.Name(),
			Path:    rel,
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortTree(root)
	return root, nil
}

func sortTree(n *Node) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for _, c := range n.Children {
		sortTree(c)
	}
}
