package platform

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Secrets holds values that must survive a platform.db rebuild in the
// sense that matters: nothing else can reconstruct them, so treat loss
// as a real incident (re-enter them). At-rest protection is the disk/
// backup encryption story from the threat model — the platform never
// returns secret values over HTTP, only reads them itself.

// SecretSet stores or replaces a secret value.
func (d *DB) SecretSet(ctx context.Context, namespace, key, value string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO secrets(namespace, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value = excluded.value, updated_at = excluded.updated_at`,
		namespace, key, value, time.Now().Unix())
	return err
}

// SecretGet returns the value and whether the key exists.
func (d *DB) SecretGet(ctx context.Context, namespace, key string) (string, bool, error) {
	var v string
	err := d.QueryRowContext(ctx,
		`SELECT value FROM secrets WHERE namespace = ? AND key = ?`,
		namespace, key).Scan(&v)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false, err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	return v, err == nil, nil
}

// SecretDelete removes a secret.
func (d *DB) SecretDelete(ctx context.Context, namespace, key string) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM secrets WHERE namespace = ? AND key = ?`, namespace, key)
	return err
}

// SecretList returns the keys in the namespace — never the values.
func (d *DB) SecretList(ctx context.Context, namespace string) ([]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT key FROM secrets WHERE namespace = ? ORDER BY key`, namespace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
