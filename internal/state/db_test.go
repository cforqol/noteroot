package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpen_fresh(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, rebuilt, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if rebuilt {
		t.Error("rebuilt = true for a fresh DB, want false")
	}
}

func TestOpen_existingIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()

	db, _, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db2, rebuilt, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() { _ = db2.Close() }()

	if rebuilt {
		t.Error("rebuilt = true for existing intact DB, want false")
	}
}

func TestOpen_migratesOnOpen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, _, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Verify note_identity table exists (created by migration)
	var count int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='note_identity'").Scan(&count)
	if err != nil || count == 0 {
		t.Error("note_identity table not found — migrations did not run")
	}
}

func TestNeedsRebuild_noNotesOnDisk(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, _, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	needs, _ := NeedsRebuild(ctx, db, 0)
	if needs {
		t.Error("NeedsRebuild = true when notesOnDisk = 0, want false")
	}
}

func TestNeedsRebuild_notesExistButDBEmpty(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, _, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	needs, reason := NeedsRebuild(ctx, db, 5)
	if !needs {
		t.Error("NeedsRebuild = false when notes exist on disk but DB is empty, want true")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestNeedsRebuild_populatedDB(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, _, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(ctx,
		`INSERT INTO note_identity(path, uid, first_seen, last_seen) VALUES ('a.md', 'uid-1', 1, 1)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	needs, _ := NeedsRebuild(ctx, db, 1)
	if needs {
		t.Error("NeedsRebuild = true when DB has a row matching disk count, want false")
	}
}
