package platform

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"noteroot/internal/frontmatter"
)

// APIToken is a machine credential scoped to one notebook. Only the
// SHA-256 hash of the secret is stored; the plaintext exists once, in
// the create response. Revocation is a timestamp so audit trails can
// still name the token after it dies.
type APIToken struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hash       string    `json:"-"` // never serialized
	Scopes     []string  `json:"scopes"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	RevokedAt  time.Time `json:"revoked_at"`
}

// Revoked reports whether the token was revoked.
func (t *APIToken) Revoked() bool { return !t.RevokedAt.IsZero() }

// Spec returns the stored scope list in canonical space-separated form.
func (t *APIToken) Spec() string { return strings.Join(t.Scopes, " ") }

// TokenCreate stores a new token. The caller generates id-independent
// material: hash must be the SHA-256 of the plaintext secret, scopes a
// validated grant list (ParseGrants already accepted it).
func (d *DB) TokenCreate(ctx context.Context, name, hash string, scopes []string) (*APIToken, error) {
	t := &APIToken{
		ID:        frontmatter.NewID(),
		Name:      name,
		Hash:      hash,
		Scopes:    scopes,
		CreatedAt: time.Now().UTC(),
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO api_tokens(id, name, hash, scopes, created_at, last_used, revoked_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0)`,
		t.ID, t.Name, t.Hash, t.Spec(), t.CreatedAt.Unix())
	if err != nil {
		return nil, err
	}
	return t, nil
}

// TokenNameTaken reports whether a live (non-revoked) token already
// carries name. Revoked names are reusable.
func (d *DB) TokenNameTaken(ctx context.Context, name string) (bool, error) {
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_tokens WHERE name = ? AND revoked_at = 0`,
		name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// IsNameConflict reports whether err is the live-name unique index
// firing (a concurrent create won the race TokenNameTaken missed).
// The driver reports the column, not the index name.
func IsNameConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: api_tokens.name")
}

// TokenByHash looks a token up by secret hash. Missing returns
// (nil, nil) — callers treat missing and revoked alike at the HTTP
// edge, so no existence oracle leaks through error shape.
func (d *DB) TokenByHash(ctx context.Context, hash string) (*APIToken, error) {
	return d.scanToken(d.QueryRowContext(ctx,
		`SELECT id, name, hash, scopes, created_at, last_used, revoked_at
		 FROM api_tokens WHERE hash = ?`, hash))
}

// TokenGet looks a token up by id (management endpoints).
func (d *DB) TokenGet(ctx context.Context, id string) (*APIToken, error) {
	return d.scanToken(d.QueryRowContext(ctx,
		`SELECT id, name, hash, scopes, created_at, last_used, revoked_at
		 FROM api_tokens WHERE id = ?`, id))
}

// TokenList returns every token, newest first, hash included (the
// caller strips it before responding).
func (d *DB) TokenList(ctx context.Context) ([]APIToken, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, name, hash, scopes, created_at, last_used, revoked_at
		 FROM api_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var created, lastUsed, revoked int64
		var scopes string
		if err := rows.Scan(&t.ID, &t.Name, &t.Hash, &scopes, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		t.Scopes = splitScopes(scopes)
		t.CreatedAt = time.Unix(created, 0).UTC()
		if lastUsed > 0 {
			t.LastUsedAt = time.Unix(lastUsed, 0).UTC()
		}
		if revoked > 0 {
			t.RevokedAt = time.Unix(revoked, 0).UTC()
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TokenRevoke marks a token revoked. The second return reports whether
// this call did the revoking (false = already revoked); an unknown id
// is sql.ErrNoRows.
func (d *DB) TokenRevoke(ctx context.Context, id string) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ?
		 WHERE id = ? AND revoked_at = 0`, time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	var exists int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_tokens WHERE id = ?`, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, sql.ErrNoRows
	}
	return false, nil
}

// TokenTouch records successful use. Best-effort by contract: a failed
// touch must never fail the request it authenticated.
func (d *DB) TokenTouch(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE api_tokens SET last_used = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (d *DB) scanToken(row *sql.Row) (*APIToken, error) {
	var t APIToken
	var scopes string
	var created, lastUsed, revoked int64
	err := row.Scan(&t.ID, &t.Name, &t.Hash, &scopes, &created, &lastUsed, &revoked)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Scopes = splitScopes(scopes)
	t.CreatedAt = time.Unix(created, 0).UTC()
	if lastUsed > 0 {
		t.LastUsedAt = time.Unix(lastUsed, 0).UTC()
	}
	if revoked > 0 {
		t.RevokedAt = time.Unix(revoked, 0).UTC()
	}
	return &t, nil
}

func splitScopes(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
