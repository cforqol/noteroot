package state

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrLocked reports a lock held by another holder. ErrNoLock reports
// releasing or querying a path with no live lock.
var (
	ErrLocked = errors.New("locked by another holder")
	ErrNoLock = errors.New("no such lock")
)

// DefaultLockTTL applies when callers pass ttl <= 0. MaxLockTTL caps it.
// Locks are advisory UX state; If-Match stays the correctness backstop.
const (
	DefaultLockTTL = 5 * time.Minute
	MaxLockTTL     = 30 * time.Minute
)

// DB is the per-notebook durable database. It embeds *sql.DB, so all
// database/sql methods promote.
type DB struct {
	*sql.DB
}

// AcquireLock takes or refreshes the lock on path for holder. A live lock
// owned by someone else fails with ErrLocked. Expired rows are deleted first,
// so they never block.
func (db *DB) AcquireLock(ctx context.Context, path, holder string, ttl time.Duration) (time.Time, error) {
	if holder == "" {
		return time.Time{}, errors.New("missing holder")
	}
	switch {
	case ttl <= 0:
		ttl = DefaultLockTTL
	case ttl > MaxLockTTL:
		ttl = MaxLockTTL
	}
	expires := time.Now().Add(ttl).Truncate(time.Second)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE expires_at <= strftime('%s','now')`); err != nil {
		return time.Time{}, err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT holder FROM locks WHERE path = ?`, path).Scan(&current)
	switch {
	case err == nil && current != holder:
		if rerr := tx.Rollback(); rerr != nil {
			return time.Time{}, rerr
		}
		return time.Time{}, ErrLocked
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE locks SET expires_at = ?, acquired_at = strftime('%s','now') WHERE path = ?`,
			expires.Unix(), path); err != nil {
			return time.Time{}, err
		}
	case isNoRows(err):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO locks(path, holder, acquired_at, expires_at)
			 VALUES (?, ?, strftime('%s','now'), ?)`,
			path, holder, expires.Unix()); err != nil {
			return time.Time{}, err
		}
	default:
		return time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

// Lock reports the live lock on path, if any.
func (db *DB) Lock(ctx context.Context, path string) (holder string, expires time.Time, ok bool, err error) {
	var unixExp int64
	err = db.QueryRowContext(ctx,
		`SELECT holder, expires_at FROM locks
		 WHERE path = ? AND expires_at > strftime('%s','now')`, path).Scan(&holder, &unixExp)
	if err != nil {
		if isNoRows(err) {
			return "", time.Time{}, false, nil
		}
		return "", time.Time{}, false, err
	}
	return holder, time.Unix(unixExp, 0), true, nil
}

// ReleaseLock drops holder's lock. A missing lock or another holder's lock
// fails with ErrNoLock.
func (db *DB) ReleaseLock(ctx context.Context, path, holder string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM locks WHERE path = ? AND holder = ?`, path, holder)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoLock
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
