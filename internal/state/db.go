package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const dsnSuffix = "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

// openLocks serializes Open per database path. Two concurrent opens of
// the same file race the migration transactions: a deferred read
// transaction cannot upgrade to a write in WAL mode, and the loser
// gets SQLITE_BUSY regardless of busy_timeout (the corrupt-quarantine
// path would then rename the other's live file out from under it).
// Openers of *different* paths never wait on each other.
var (
	openLocksMu sync.Mutex
	openLocks   = map[string]*sync.Mutex{}
)

func lockOpen(path string) (unlock func()) {
	openLocksMu.Lock()
	mu, ok := openLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		openLocks[path] = mu
	}
	openLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Open creates or opens the durable database. A corrupt file is
// quarantined rather than repaired, then rebuilt from scratch.
func Open(ctx context.Context, path string) (*DB, bool, error) {
	return OpenDB(ctx, path, Migrate)
}

// OpenDB is Open with a caller-supplied migration set, so derived
// databases like the search index reuse the quarantine behavior.
func OpenDB(ctx context.Context, path string, migrate func(context.Context, *sql.DB) error) (*DB, bool, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, false, err
		}
	}
	defer lockOpen(path)()
	db, err := sql.Open("sqlite", "file:"+path+dsnSuffix)
	if err != nil {
		return nil, false, err
	}
	out := &DB{db}
	out.SetMaxOpenConns(1)

	rebuilt := false
	if !integrityOK(ctx, out) {
		_ = out.Close()
		if err := os.Rename(path, path+".corrupt"); err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
		raw, err := sql.Open("sqlite", "file:"+path+dsnSuffix)
		if err != nil {
			return nil, false, err
		}
		out = &DB{raw}
		rebuilt = true
	}
	if err := migrate(ctx, out.DB); err != nil {
		_ = out.Close()
		return nil, rebuilt, err
	}
	return out, rebuilt, nil
}

func integrityOK(ctx context.Context, db *DB) bool {
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return false
	}
	return res == "ok"
}

// NeedsRebuild is the rebuildability guard: a database that opens fine
// but has silently lost rows is more dangerous than one that fails.
func NeedsRebuild(ctx context.Context, db *DB, notesOnDisk int) (bool, string) {
	var ids int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM note_identity`).Scan(&ids); err != nil {
		return true, "identity table unreadable"
	}
	if notesOnDisk > 0 && ids == 0 {
		return true, "notes exist on disk but identity map is empty"
	}
	return false, ""
}

// Rebuildable is implemented by each derived table.
type Rebuildable interface {
	Rebuild(ctx context.Context) error
}
