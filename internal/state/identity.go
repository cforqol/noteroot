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

// SyncIdentityFromVault is the backfill pass run from index Build/Update.
// It prunes rows whose path is gone from the vault, then inserts or
// refreshes a note_identity row for every note with a valid frontmatter
// id that is missing or stale in the map. Notes without a valid id are
// left alone — the write path mints on next save. Returns the number of
// rows written.
func (db *DB) SyncIdentityFromVault(ctx context.Context, v *vault.Vault) (int, error) {
	t, err := v.Tree()
	if err != nil {
		return 0, err
	}
	var notes []*vault.Node
	live := make(map[string]bool)
	var walk func(n *vault.Node)
	walk = func(n *vault.Node) {
		for _, c := range n.Children {
			if c.IsDir {
				walk(c)
			} else {
				notes = append(notes, c)
				live[c.Path] = true
			}
		}
	}
	walk(t)

	// Prune first: a row whose path is gone (deleted, trashed, renamed
	// outside the app) must not resolve to a missing file. Restore
	// re-inserts the row on the next pass — the frontmatter id travels
	// with the file.
	rows, err := db.QueryContext(ctx, `SELECT path FROM note_identity`)
	if err != nil {
		return 0, err
	}
	var gone []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !live[p] {
			gone = append(gone, p)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	for _, p := range gone {
		if err := db.DeleteIdentity(ctx, p); err != nil {
			return 0, err
		}
	}

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
		ok, err := db.syncNoteIdentity(ctx, n.Path, doc.ID)
		if err != nil {
			return written, err
		}
		if ok {
			written++
		}
	}
	return written, nil
}

// SyncIdentityPaths is the incremental twin of SyncIdentityFromVault:
// index Update already knows which paths changed or vanished, so only
// those are touched instead of re-reading the whole vault. changed maps
// path → frontmatter id (empty id leaves any existing row alone so old
// links survive until a new id is minted on next save). Returns the
// number of rows written.
func (db *DB) SyncIdentityPaths(ctx context.Context, changed map[string]string, gone []string) (int, error) {
	for _, p := range gone {
		if err := db.DeleteIdentity(ctx, p); err != nil {
			return 0, err
		}
	}
	written := 0
	for p, id := range changed {
		if id == "" {
			continue
		}
		ok, err := db.syncNoteIdentity(ctx, p, id)
		if err != nil {
			return written, err
		}
		if ok {
			written++
		}
	}
	return written, nil
}

// syncNoteIdentity refreshes the row for one path. id=="" leaves the
// row untouched; a uid already bound to a different path is skipped so
// a copy-pasted id never aborts the pass or steals the mapping (the
// UNIQUE(uid) index owns that guarantee). ok reports a written row.
func (db *DB) syncNoteIdentity(ctx context.Context, p, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var existing string
	err := db.QueryRowContext(ctx,
		`SELECT uid FROM note_identity WHERE path = ?`, p).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// missing: insert below
	case err != nil:
		return false, err
	case existing == id:
		return false, nil // already current
	}
	var bound string
	err = db.QueryRowContext(ctx,
		`SELECT path FROM note_identity WHERE uid = ?`, id).Scan(&bound)
	switch {
	case err == nil:
		return false, nil // uid already bound to another path
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	if err := db.UpsertIdentity(ctx, p, id); err != nil {
		return false, err
	}
	return true, nil
}

// IdentityMap returns the full uid→path map from note_identity. Callers
// use it to resolve wikilink targets that are exact ids (design §5.1);
// entries whose path no longer exists are the caller's to ignore.
func (db *DB) IdentityMap(ctx context.Context) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT uid, path FROM note_identity`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var uid, p string
		if err := rows.Scan(&uid, &p); err != nil {
			return nil, err
		}
		out[uid] = p
	}
	return out, rows.Err()
}

// DeleteIdentity removes the durable mapping for a path. Missing rows
// are not an error: callers use this to forget deleted notes.
func (db *DB) DeleteIdentity(ctx context.Context, p string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM note_identity WHERE path = ?`, p); err != nil {
		return fmt.Errorf("delete identity %s: %w", p, err)
	}
	return nil
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

// RenameIdentity updates the path of a note_identity row without changing
// its uid. If oldPath is not found the update is a no-op (the row will be
// re-created on the next index sync). A conflict with newPath (duplicate
// path) returns an error.
func (db *DB) RenameIdentity(ctx context.Context, oldPath, newPath string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE note_identity SET path = ? WHERE path = ?`, newPath, oldPath)
	if err != nil {
		return fmt.Errorf("rename identity %s → %s: %w", oldPath, newPath, err)
	}
	return nil
}

// RenameIdentityPrefix rewrites every note_identity row under a folder
// prefix after the folder was renamed or moved; uids never change. Rows
// whose rewritten path already exists (stale leftovers of a deleted
// note) are skipped so the UNIQUE(path) primary key is never violated —
// identity is rebuildable, and the index sync re-points the stale row
// on its next pass.
func (db *DB) RenameIdentityPrefix(ctx context.Context, fromDir, toDir string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE note_identity AS n
		SET path = ?1 || SUBSTR(n.path, LENGTH(?2) + 1)
		WHERE SUBSTR(n.path, 1, LENGTH(?2) + 1) = ?2 || '/'
		  AND NOT EXISTS (
			SELECT 1 FROM note_identity x
			WHERE x.path = ?1 || SUBSTR(n.path, LENGTH(?2) + 1)
		  )`, toDir, fromDir)
	if err != nil {
		return fmt.Errorf("rename identity prefix %s → %s: %w", fromDir, toDir, err)
	}
	return nil
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
