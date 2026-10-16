package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_freshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.db")

	db, rebuilt, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_ = db.Close()
	if rebuilt {
		t.Error("rebuilt = true for a fresh DB, want false")
	}

	db2, rebuilt, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() { _ = db2.Close() }()
	if rebuilt {
		t.Error("rebuilt = true for intact DB, want false")
	}

	// Migrations landed: schema_version covers every migration.
	var version int
	if err := db2.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Errorf("schema version = %d, want %d", version, len(migrations))
	}
}

func TestOpen_corruptQuarantined(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, rebuilt, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if !rebuilt {
		t.Error("rebuilt = false for corrupt DB, want true")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("quarantine file missing: %v", err)
	}
	// Rebuilt DB is usable.
	if err := db.AuditAppend(ctx, "a", "test", "r", ""); err != nil {
		t.Errorf("AuditAppend on rebuilt DB: %v", err)
	}
}

func TestKV_roundTrip(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, ok, err := db.KVGet(ctx, "ns", "missing"); err != nil || ok {
		t.Fatalf("KVGet(missing) = (%v, %v), want (false, nil)", ok, err)
	}
	if err := db.KVSet(ctx, "ns", "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.KVSet(ctx, "ns", "k", "v2"); err != nil { // overwrite
		t.Fatal(err)
	}
	if err := db.KVSet(ctx, "other", "k", "x"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.KVGet(ctx, "ns", "k")
	if err != nil || !ok || v != "v2" {
		t.Fatalf("KVGet = (%q, %v, %v), want (\"v2\", true, nil)", v, ok, err)
	}

	list, err := db.KVList(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list["k"] != "v2" {
		t.Errorf("KVList(ns) = %v, want map[k:v2]", list)
	}

	if err := db.KVDelete(ctx, "ns", "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.KVGet(ctx, "ns", "k"); ok {
		t.Error("KVGet after delete = true, want false")
	}
}

func TestSecrets_roundTripAndListHidesValues(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if err := db.SecretSet(ctx, "hooks", "signing", "s3cr3t"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.SecretGet(ctx, "hooks", "signing")
	if err != nil || !ok || v != "s3cr3t" {
		t.Fatalf("SecretGet = (%q, %v, %v)", v, ok, err)
	}

	keys, err := db.SecretList(ctx, "hooks")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "signing" {
		t.Errorf("SecretList = %v, want [signing]", keys)
	}

	if err := db.SecretDelete(ctx, "hooks", "signing"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.SecretGet(ctx, "hooks", "signing"); ok {
		t.Error("SecretGet after delete = true, want false")
	}
}

func TestAudit_appendQueryPrune(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if err := db.AuditAppend(ctx, "alice", "note.write", "work/a.md", `{"sha":"x"}`); err != nil {
		t.Fatal(err)
	}
	if err := db.AuditAppend(ctx, "bob", "note.delete", "work/b.md", ""); err != nil {
		t.Fatal(err)
	}

	entries, err := db.AuditQuery(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("AuditQuery len = %d, want 2", len(entries))
	}
	if entries[0].Actor != "bob" || entries[1].Actor != "alice" {
		t.Errorf("order = %s,%s; want newest first (bob, alice)", entries[0].Actor, entries[1].Actor)
	}
	if entries[1].Detail != `{"sha":"x"}` {
		t.Errorf("detail = %q", entries[1].Detail)
	}

	// Prune with a future cutoff removes everything.
	n, err := db.AuditPrune(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 2 {
		t.Errorf("AuditPrune = (%d, %v), want (2, nil)", n, err)
	}
	entries, _ = db.AuditQuery(ctx, 10)
	if len(entries) != 0 {
		t.Errorf("AuditQuery after prune len = %d, want 0", len(entries))
	}
}
