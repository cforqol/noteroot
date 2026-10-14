package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path+dsnSuffix)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrate_fresh(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	tables := []string{"schema_version", "note_identity", "resources", "locks"}
	for _, table := range tables {
		var count int
		err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
		if err != nil || count == 0 {
			t.Errorf("table %q not found after migration", table)
		}
	}
}

func TestMigrate_idempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
}

func TestMigrate_version(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var maxVersion int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if maxVersion != len(migrations) {
		t.Errorf("max version = %d, want %d", maxVersion, len(migrations))
	}
}

func TestMigrate_uniqueIndex(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Insert a row
	_, err := db.ExecContext(ctx,
		`INSERT INTO note_identity(path, uid, first_seen, last_seen) VALUES ('a.md', 'uid-1', 1, 1)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Duplicate uid must be rejected
	_, err = db.ExecContext(ctx,
		`INSERT INTO note_identity(path, uid, first_seen, last_seen) VALUES ('b.md', 'uid-1', 1, 1)`)
	if err == nil {
		t.Error("expected unique constraint error for duplicate uid, got nil")
	}
}
