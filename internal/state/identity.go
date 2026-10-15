package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"noteroot/internal/frontmatter"
	"noteroot/internal/vault"
)

// UpsertIdentity records or refreshes the durable path→uid map row for a
// note. A changed uid (id reminted after corruption) overwrites the old
// mapping; the same uid on a different path is rejected by the unique
// index.
func (db *DB) UpsertIdentity(ctx context.Context, p, uid string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO note_identity(path, uid, first_seen, last_seen)
		VALUES (?, ?, strftime('%s','now'), strftime('%s','now'))
		ON CONFLICT(path) DO UPDATE SET uid=excluded.uid, last_seen=excluded.last_seen`,
		p, uid)
	if err != nil {
		return fmt.Errorf("upsert identity %s: %w", p, err)
	}
	return nil
}

// SyncIdentityFromVault walks the vault and inserts or refreshes a
// note_identity row for every note with a valid frontmatter id that is
// missing or stale in the map. Notes without a valid id are left alone —
// the write path mints on next save. Returns the number of rows written.
func (db *DB) SyncIdentityFromVault(ctx context.Context, v *vault.Vault) (int, error) {
	t, err := v.Tree()
	if err != nil {
		return 0, err
	}
	var notes []*vault.Node
	var walk func(n *vault.Node)
	walk = func(n *vault.Node) {
		for _, c := range n.Children {
			if c.IsDir {
				walk(c)
			} else {
				notes = append(notes, c)
			}
		}
	}
	walk(t)

	written := 0
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		src, err := v.Read(n.Path)
		if err != nil {
			continue // unreadable note; never fatal
		}
		doc := frontmatter.Parse(src)
		if doc.ID == "" {
			continue
		}
		var existing string
		err = db.QueryRowContext(ctx,
			`SELECT uid FROM note_identity WHERE path = ?`, n.Path).Scan(&existing)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// missing: insert
		case err != nil:
			return written, err
		case existing == doc.ID:
			continue // already current
		}
		if err := db.UpsertIdentity(ctx, n.Path, doc.ID); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// UIDByPath looks up the durable identity for a path. ok=false when the
// path is unknown or has no valid id yet.
func (db *DB) UIDByPath(ctx context.Context, p string) (string, bool, error) {
	var uid string
	err := db.QueryRowContext(ctx,
		`SELECT uid FROM note_identity WHERE path = ?`, p).Scan(&uid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return uid, true, nil
}

// PathByUID resolves an identity to its current path. ok=false when the
// uid is unknown.
func (db *DB) PathByUID(ctx context.Context, uid string) (string, bool, error) {
	var p string
	err := db.QueryRowContext(ctx,
		`SELECT path FROM note_identity WHERE uid = ?`, uid).Scan(&p)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return p, true, nil
}
