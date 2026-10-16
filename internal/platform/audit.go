package platform

import (
	"context"
	"time"
)

// AuditEntry is one durable record of something that happened.
type AuditEntry struct {
	ID       int64     `json:"id"`
	Ts       time.Time `json:"ts"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Resource string    `json:"resource"`
	Detail   string    `json:"detail"`
}

// AuditAppend records one entry. Failure is the caller's to log: the
// audit trail degrades before anything else does, and never fails a
// mutation (same contract as the git flusher).
func (d *DB) AuditAppend(ctx context.Context, actor, action, resource, detail string) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO audit(ts, actor, action, resource, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, resource, detail)
	return err
}

// AuditQuery returns up to limit entries, newest first.
func (d *DB) AuditQuery(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, ts, actor, action, resource, detail FROM audit
		 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Resource, &e.Detail); err != nil {
			return nil, err
		}
		e.Ts = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditPrune drops entries older than cutoff. Wired to nothing yet —
// available for a future sweep alongside GC.
func (d *DB) AuditPrune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM audit WHERE ts < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
