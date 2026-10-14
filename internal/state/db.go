package state

import (
	"context"
	"database/sql"
	"os"

	_ "modernc.org/sqlite"
)

const dsnSuffix = "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

// Open creates or opens the durable database. A corrupt file is
// quarantined rather than repaired, then rebuilt from scratch.
func Open(ctx context.Context, path string) (*sql.DB, bool, error) {
	db, err := sql.Open("sqlite", "file:"+path+dsnSuffix)
	if err != nil {
		return nil, false, err
	}
	db.SetMaxOpenConns(1)

	rebuilt := false
	if !integrityOK(ctx, db) {
		_ = db.Close()
		if err := os.Rename(path, path+".corrupt"); err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
		db, err = sql.Open("sqlite", "file:"+path+dsnSuffix)
		if err != nil {
			return nil, false, err
		}
		rebuilt = true
	}
	if err := Migrate(ctx, db); err != nil {
		return nil, rebuilt, err
	}
	return db, rebuilt, nil
}

func integrityOK(ctx context.Context, db *sql.DB) bool {
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return false
	}
	return res == "ok"
}

// NeedsRebuild is the rebuildability guard: a database that opens fine
// but has silently lost rows is more dangerous than one that fails.
func NeedsRebuild(ctx context.Context, db *sql.DB, notesOnDisk int) (bool, string) {
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
