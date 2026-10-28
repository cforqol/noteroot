package platform

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// KV stores small rebuildable key/value pairs, namespaced per consumer
// ("plugins/<name>", "integration/<name>", …). Values are plain strings;
// anything sensitive belongs in Secrets.
func (d *DB) KVSet(ctx context.Context, namespace, key, value string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO kv(namespace, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value = excluded.value, updated_at = excluded.updated_at`,
		namespace, key, value, time.Now().Unix())
	return err
}

// KVGet returns the value and whether the key exists.
func (d *DB) KVGet(ctx context.Context, namespace, key string) (string, bool, error) {
	var v string
	err := d.QueryRowContext(ctx,
		`SELECT value FROM kv WHERE namespace = ? AND key = ?`,
		namespace, key).Scan(&v)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false, err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	return v, err == nil, nil
}

func (d *DB) KVDelete(ctx context.Context, namespace, key string) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM kv WHERE namespace = ? AND key = ?`, namespace, key)
	return err
}

// KVList returns every key/value in the namespace.
func (d *DB) KVList(ctx context.Context, namespace string) (map[string]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT key, value FROM kv WHERE namespace = ? ORDER BY key`, namespace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
