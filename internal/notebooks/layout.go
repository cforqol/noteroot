package notebooks

import "path/filepath"

// Notebook directory layout. Three entries: manifest, vault, app dir.
const (
	ManifestFile = "notebook.toml"
	VaultDir     = "vault"
	AppDir       = ".notebook"

	ResourcesDir = "resources"
	CacheDir     = "cache"
	DraftsDir    = "drafts"
	TrashDir     = "trash"
	StateDB      = "state.db"
	IndexDB      = "index.db"
	PlatformDB   = "platform.db"
)

const (
	LayoutVersion = 1
	BackupsDir    = "backups"
)

type Layout struct{ root string }

func NewLayout(root string) Layout { return Layout{root: root} }

func (l Layout) Root() string       { return l.root }
func (l Layout) Manifest() string   { return filepath.Join(l.root, ManifestFile) }
func (l Layout) Vault() string      { return filepath.Join(l.root, VaultDir) }
func (l Layout) App() string        { return filepath.Join(l.root, AppDir) }
func (l Layout) Resources() string  { return filepath.Join(l.root, AppDir, ResourcesDir) }
func (l Layout) Cache() string      { return filepath.Join(l.root, AppDir, CacheDir) }
func (l Layout) Drafts() string     { return filepath.Join(l.root, AppDir, DraftsDir) }
func (l Layout) Trash() string      { return filepath.Join(l.root, AppDir, TrashDir) }
func (l Layout) StateDB() string    { return filepath.Join(l.root, AppDir, StateDB) }
func (l Layout) IndexDB() string    { return filepath.Join(l.root, AppDir, IndexDB) }
func (l Layout) PlatformDB() string { return filepath.Join(l.root, AppDir, PlatformDB) }
func (l Layout) Backups() string    { return filepath.Join(l.root, AppDir, BackupsDir) }

func (l Layout) Dirs() []string {
	return []string{l.Vault(), l.App(), l.Resources(), l.Cache(), l.Drafts(), l.Trash()}
}
