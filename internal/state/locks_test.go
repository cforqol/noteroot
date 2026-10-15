package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestLocksDB(t *testing.T) *DB {
	t.Helper()
	db, _, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAcquire_roundtrip(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	exp, err := db.AcquireLock(ctx, "a.md", "tab-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 4*time.Minute || d > 5*time.Minute {
		t.Errorf("default TTL expiry in %v, want ~5m", d)
	}
	holder, _, ok, err := db.Lock(ctx, "a.md")
	if err != nil || !ok || holder != "tab-1" {
		t.Errorf("Lock = %q, %v, %v; want tab-1, true, nil", holder, ok, err)
	}
	if err := db.ReleaseLock(ctx, "a.md", "tab-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := db.Lock(ctx, "a.md"); ok {
		t.Error("released lock still present")
	}
}

func TestAcquire_conflict(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	if _, err := db.AcquireLock(ctx, "a.md", "tab-1", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireLock(ctx, "a.md", "tab-2", 0); !errors.Is(err, ErrLocked) {
		t.Errorf("err = %v, want locked", err)
	}
	holder, exp, ok, err := db.Lock(ctx, "a.md")
	if err != nil || !ok || holder != "tab-1" || exp.IsZero() {
		t.Errorf("Lock = %q, %v, %v, %v", holder, exp, ok, err)
	}
	if err := db.ReleaseLock(ctx, "a.md", "tab-2"); !errors.Is(err, ErrNoLock) {
		t.Errorf("release by other err = %v, want no-lock", err)
	}
	if err := db.ReleaseLock(ctx, "missing.md", "tab-1"); !errors.Is(err, ErrNoLock) {
		t.Errorf("release missing err = %v, want no-lock", err)
	}
}

func TestAcquire_refreshExtends(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	first, err := db.AcquireLock(ctx, "a.md", "tab-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.AcquireLock(ctx, "a.md", "tab-1", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !second.After(first) {
		t.Errorf("refresh did not extend: %v -> %v", first, second)
	}
}

func TestAcquire_ttlClamped(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	exp, err := db.AcquireLock(ctx, "a.md", "tab-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d > 30*time.Minute {
		t.Errorf("TTL not capped: expiry in %v", d)
	}
}

func TestAcquire_expiredTreatedAbsent(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO locks(path, holder, acquired_at, expires_at) VALUES (?,?,?,?)`,
		"a.md", "gone-tab", 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireLock(ctx, "a.md", "tab-2", 0); err != nil {
		t.Errorf("expired lock blocks: %v", err)
	}
	holder, _, ok, err := db.Lock(ctx, "a.md")
	if err != nil || !ok || holder != "tab-2" {
		t.Errorf("Lock = %q, %v, %v", holder, ok, err)
	}
}

func TestAcquire_emptyHolder(t *testing.T) {
	db := openTestLocksDB(t)
	if _, err := db.AcquireLock(context.Background(), "a.md", "", 0); err == nil {
		t.Error("empty holder: expected error")
	}
}

func TestLocks_closedDB(t *testing.T) {
	db := openTestLocksDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.AcquireLock(ctx, "a.md", "t", 0); err == nil {
		t.Error("acquire on closed DB: expected error")
	}
	if _, _, _, err := db.Lock(ctx, "a.md"); err == nil {
		t.Error("query on closed DB: expected error")
	}
	if err := db.ReleaseLock(ctx, "a.md", "t"); err == nil {
		t.Error("release on closed DB: expected error")
	}
}
