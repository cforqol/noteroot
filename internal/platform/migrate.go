package platform

import (
	"context"
	"database/sql"
	"fmt"
)

// Forward-only. Never edit an applied migration; append a new one.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);`,
	`CREATE TABLE IF NOT EXISTS kv (
		namespace  TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (namespace, key)
	);`,
	`CREATE TABLE IF NOT EXISTS secrets (
		namespace  TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (namespace, key)
	);`,
	`CREATE TABLE IF NOT EXISTS audit (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		ts       INTEGER NOT NULL,
		actor    TEXT NOT NULL,
		action   TEXT NOT NULL,
		resource TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);`,
	`CREATE TABLE IF NOT EXISTS api_tokens (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		hash       TEXT NOT NULL UNIQUE,
		scopes     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_used  INTEGER NOT NULL DEFAULT 0,
		revoked_at INTEGER NOT NULL DEFAULT 0
	);`,
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
