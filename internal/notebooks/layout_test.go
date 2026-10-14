package notebooks

import (
	"path/filepath"
	"testing"
)

func TestLayout_paths(t *testing.T) {
	root := "/data/notebooks/work"
	l := NewLayout(root)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Root", l.Root(), root},
		{"Manifest", l.Manifest(), filepath.Join(root, ManifestFile)},
		{"Vault", l.Vault(), filepath.Join(root, VaultDir)},
		{"App", l.App(), filepath.Join(root, AppDir)},
		{"Resources", l.Resources(), filepath.Join(root, AppDir, ResourcesDir)},
		{"Cache", l.Cache(), filepath.Join(root, AppDir, CacheDir)},
		{"Drafts", l.Drafts(), filepath.Join(root, AppDir, DraftsDir)},
		{"Trash", l.Trash(), filepath.Join(root, AppDir, TrashDir)},
		{"StateDB", l.StateDB(), filepath.Join(root, AppDir, StateDB)},
		{"IndexDB", l.IndexDB(), filepath.Join(root, AppDir, IndexDB)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestLayout_Dirs(t *testing.T) {
	root := "/data/notebooks/work"
	l := NewLayout(root)
	dirs := l.Dirs()
	want := []string{l.Vault(), l.App(), l.Resources(), l.Cache(), l.Drafts(), l.Trash()}
	if len(dirs) != len(want) {
		t.Fatalf("Dirs() len = %d, want %d", len(dirs), len(want))
	}
	for i, d := range dirs {
		if d != want[i] {
			t.Errorf("Dirs()[%d] = %q, want %q", i, d, want[i])
		}
	}
}
