package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Content locks are advisory access control: a password gate in the UI,
// not encryption. The bytes stay plaintext on disk, in git, in the search
// index and in backups, so a lock hides content from someone using the
// app — it does not protect against someone reading the files. The name
// is distinct from the edit locks in locks.go, which only coordinate
// concurrent editors.

// ErrNoLockConfig reports a path with no password set.
var ErrNoLockConfig = errors.New("no lock on that path")

// PathKind is how a locked path applies.
type PathKind string

const (
	// KindNotebook locks the whole notebook (path "").
	KindNotebook PathKind = "notebook"
	// KindFolder locks a folder; descendants are covered too.
	KindFolder PathKind = "folder"
	// KindNote locks a single note.
	KindNote PathKind = "note"
)

// LockConfig is one stored password: where it applies and the argon2id
// material needed to verify it. The plaintext password is never stored.
type LockConfig struct {
	Path      string
	Kind      PathKind
	Hash      string
	Salt      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SessionTTL bounds how long an unlock lasts in a browser. Sessions are
// cheap to reconstruct (re-enter the password), so the window is short.
const SessionTTL = 12 * time.Hour

// SetLockConfig stores or replaces the password material for a path.
func (db *DB) SetLockConfig(ctx context.Context, path string, kind PathKind, hash, salt string) error {
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `
		INSERT INTO locks_config(path, kind, hash, salt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			kind = excluded.kind, hash = excluded.hash, salt = excluded.salt,
			updated_at = excluded.updated_at`,
		path, string(kind), hash, salt, now, now)
	return err
}

// GetLockConfig returns the password material for an exact path.
func (db *DB) GetLockConfig(ctx context.Context, path string) (LockConfig, bool, error) {
	var c LockConfig
	var kind string
	var created, updated int64
	err := db.QueryRowContext(ctx,
		`SELECT path, kind, hash, salt, created_at, updated_at FROM locks_config WHERE path = ?`,
		path).Scan(&c.Path, &kind, &c.Hash, &c.Salt, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return LockConfig{}, false, nil
	}
	if err != nil {
		return LockConfig{}, false, err
	}
	c.Kind = PathKind(kind)
	c.CreatedAt = time.Unix(created, 0)
	c.UpdatedAt = time.Unix(updated, 0)
	return c, true, nil
}

// DeleteLockConfig removes a path's password and every session that
// unlocked it, so a removed lock cannot be re-entered with a stale
// session. Descendant locks are left alone.
func (db *DB) DeleteLockConfig(ctx context.Context, path string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM locks_config WHERE path = ?`, path)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoLockConfig
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lock_sessions WHERE path = ?`, path); err != nil {
		return err
	}
	return tx.Commit()
}

// LockConfigs returns every stored lock, shortest path first.
func (db *DB) LockConfigs(ctx context.Context) ([]LockConfig, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT path, kind, hash, salt, created_at, updated_at FROM locks_config ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LockConfig
	for rows.Next() {
		var c LockConfig
		var kind string
		var created, updated int64
		if err := rows.Scan(&c.Path, &kind, &c.Hash, &c.Salt, &created, &updated); err != nil {
			return nil, err
		}
		c.Kind = PathKind(kind)
		c.CreatedAt = time.Unix(created, 0)
		c.UpdatedAt = time.Unix(updated, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ancestors returns the paths that can lock target: the notebook root
// ("") always, every folder prefix, and the exact path itself when it is
// a note. For example target "a/b/c.md" yields "", "a/", "a/b/", and
// "a/b/c.md". A trailing-slash target (a folder) yields its own prefixes
// but not the bare path, since a folder's config is stored with a slash.
func ancestors(target string) []string {
	out := []string{""}
	if target == "" {
		return out
	}
	if strings.HasSuffix(target, "/") {
		trimmed := strings.TrimSuffix(target, "/")
		parts := strings.Split(trimmed, "/")
		prefix := ""
		for _, p := range parts {
			if p == "" {
				continue
			}
			prefix += p + "/"
			out = append(out, prefix)
		}
		return out
	}
	parts := strings.Split(target, "/")
	prefix := ""
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == len(parts)-1 {
			out = append(out, prefix+p)
		} else {
			prefix += p + "/"
			out = append(out, prefix)
		}
	}
	return out
}

// LocksFor returns the locks that apply to target, i.e. every stored
// lock on an ancestor path (or the path itself). Empty when unlock is
// unrestricted.
func (db *DB) LocksFor(ctx context.Context, target string) ([]LockConfig, error) {
	paths := ancestors(target)
	if len(paths) == 0 {
		return nil, nil
	}
	ph := strings.Repeat("?,", len(paths))
	ph = ph[:len(ph)-1]
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	rows, err := db.QueryContext(ctx,
		`SELECT path, kind, hash, salt, created_at, updated_at FROM locks_config
		 WHERE path IN (`+ph+`) ORDER BY length(path)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []LockConfig
	for rows.Next() {
		var c LockConfig
		var kind string
		var created, updated int64
		if err := rows.Scan(&c.Path, &kind, &c.Hash, &c.Salt, &created, &updated); err != nil {
			return nil, err
		}
		c.Kind = PathKind(kind)
		c.CreatedAt = time.Unix(created, 0)
		c.UpdatedAt = time.Unix(updated, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateSession records that path's password was entered, returning the
// opaque token to hand back as a cookie.
func (db *DB) CreateSession(ctx context.Context, path, token string) (time.Time, error) {
	now := time.Now()
	expires := now.Add(SessionTTL).Truncate(time.Second)
	_, err := db.ExecContext(ctx,
		`INSERT INTO lock_sessions(token, path, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, path, now.Unix(), expires.Unix())
	return expires, err
}

// SessionPaths returns the live session paths for the given tokens, so a
// caller can check coverage without a query per token. Expired rows are
// ignored.
func (db *DB) SessionPaths(ctx context.Context, tokens []string) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	ph := strings.Repeat("?,", len(tokens))
	ph = ph[:len(ph)-1]
	args := make([]any, len(tokens))
	for i, t := range tokens {
		args[i] = t
	}
	rows, err := db.QueryContext(ctx,
		`SELECT path FROM lock_sessions
		 WHERE token IN (`+ph+`) AND expires_at > strftime('%s','now')`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteSession revokes one session token.
func (db *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM lock_sessions WHERE token = ?`, token)
	return err
}

// PurgeSessions drops expired sessions.
func (db *DB) PurgeSessions(ctx context.Context) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM lock_sessions WHERE expires_at <= strftime('%s','now')`)
	return err
}
