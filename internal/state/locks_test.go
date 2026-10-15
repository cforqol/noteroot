package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"noteroot/internal/vault"
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

func TestUpsertIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	path := "notes/hello.md"
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	if err := db.UpsertIdentity(ctx, path, uid); err != nil {
		t.Fatal(err)
	}
	// Re-upsert same path with same uid: no error, no duplicate.
	if err := db.UpsertIdentity(ctx, path, uid); err != nil {
		t.Fatal(err)
	}
	// Re-upsert same path with new uid (id reminted): updates.
	uid2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV01"
	if err := db.UpsertIdentity(ctx, path, uid2); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRowContext(ctx, `SELECT uid FROM note_identity WHERE path = ?`, path).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != uid2 {
		t.Errorf("uid = %q, want %q", got, uid2)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM note_identity WHERE path = ?`, path).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1", count)
	}
}

func TestUpsertIdentity_uniqueUID(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	if err := db.UpsertIdentity(ctx, "a.md", uid); err != nil {
		t.Fatal(err)
	}
	// Same uid on a different path: UNIQUE index should reject.
	if err := db.UpsertIdentity(ctx, "b.md", uid); err == nil {
		t.Error("expected error for duplicate uid on different path")
	}
}

func TestSyncIdentityFromVault(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	v := newTestVault(t)
	id1 := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	id2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV01"
	writeVaultFile(t, v, "a.md", "---\nid: "+id1+"\ntitle: A\n---\nbody\n")
	writeVaultFile(t, v, "b.md", "---\nid: "+id2+"\ntitle: B\n---\nbody\n")
	n, err := db.SyncIdentityFromVault(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("synced = %d, want >= 2", n)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM note_identity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Errorf("rows = %d, want >= 2", count)
	}
}

func newTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	return vault.Open(t.TempDir())
}

func writeVaultFile(t *testing.T, v *vault.Vault, rel, content string) {
	t.Helper()
	if _, err := v.Write(rel, []byte(content), vault.WriteOpts{}); err != nil {
		t.Fatal(err)
	}
}
