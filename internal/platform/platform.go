// Package platform is the extension substrate (stage 9): a per-notebook
// database holding kv, secrets, and audit, plus a per-notebook event bus
// that plugins and integrations subscribe to.
//
// platform follows the same isolation boundary as everything below HTTP:
// one platform.db per notebook. It is rebuildable except secrets — losing
// it means re-registering plugins and re-entering secrets, never losing
// notes (ADR 0002-style contract extended to the substrate).
package platform

import (
	"context"
	"database/sql"

	"noteroot/internal/state"

	_ "modernc.org/sqlite"
)

// DB wraps the per-notebook platform database.
type DB struct {
	*sql.DB
}

// Open creates or opens the platform database at path. Corruption is
// quarantined to path+".corrupt" and rebuilt from scratch — the same
// contract as state.db (state.OpenDB provides it).
func Open(ctx context.Context, path string) (*DB, bool, error) {
	db, rebuilt, err := state.OpenDB(ctx, path, Migrate)
	if err != nil {
		return nil, rebuilt, err
	}
	return &DB{DB: db.DB}, rebuilt, nil
}
