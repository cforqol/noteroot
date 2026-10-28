package index

import (
	"context"
	"database/sql"
	"fmt"
)

// Forward-only. Never edit an applied migration; append a new one.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);`,
	`CREATE TABLE IF NOT EXISTS docs (
		path  TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT '',
		mtime INTEGER NOT NULL,
		size  INTEGER NOT NULL
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
		path UNINDEXED, title, tags, body, tokenize='porter'
	);
	CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
	`ALTER TABLE docs ADD COLUMN id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE docs ADD COLUMN aliases TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE IF NOT EXISTS links (
		from_path TEXT NOT NULL,
		target    TEXT NOT NULL,
		to_path   TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (from_path, target)
	);`,
	`ALTER TABLE links ADD COLUMN label TEXT NOT NULL DEFAULT '';`,
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
