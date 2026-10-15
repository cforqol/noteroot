package state

import (
	"context"
	"database/sql"
	"fmt"
)

// Forward-only. Never edit an applied migration; append a new one.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);`,
	`CREATE TABLE IF NOT EXISTS note_identity (
		path       TEXT PRIMARY KEY,
		uid        TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS note_identity_uid ON note_identity(uid);`,
	`CREATE TABLE IF NOT EXISTS resources (
		blob_sha    TEXT PRIMARY KEY,
		blob_key    TEXT NOT NULL,
		size        INTEGER NOT NULL,
		mime        TEXT NOT NULL,
		verified_at INTEGER NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS locks (
		path        TEXT PRIMARY KEY,
		holder      TEXT NOT NULL,
		acquired_at INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS upload_journal (
		id          TEXT PRIMARY KEY,
		filename    TEXT NOT NULL,
		size        INTEGER NOT NULL,
		mime        TEXT NOT NULL,
		sha         TEXT,
		state       TEXT NOT NULL,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS upload_journal_state ON upload_journal(state);`,
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, migrations[0]); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read version: %w", err)
	}
	for i := current; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
