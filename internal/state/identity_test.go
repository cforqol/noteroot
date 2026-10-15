package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncIdentityFromVault_prunesGonePaths(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	v := newTestVault(t)
	id1 := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	id2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV01"
	writeVaultFile(t, v, "a.md", "---\nid: "+id1+"\n---\nbody\n")
	writeVaultFile(t, v, "b.md", "---\nid: "+id2+"\n---\nbody\n")
	if _, err := db.SyncIdentityFromVault(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(v.Root(), "a.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SyncIdentityFromVault(ctx, v); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM note_identity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1 (gone path pruned)", count)
	}
	if _, ok, _ := db.UIDByPath(ctx, "a.md"); ok {
		t.Error("pruned path still resolves in UIDByPath")
	}
}

func TestSyncIdentityFromVault_skipsDuplicateUID(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	v := newTestVault(t)
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	// Two notes copy-pasted with the same id: the pass must not fail,
	// and the uid stays bound to the first path seen.
	writeVaultFile(t, v, "a.md", "---\nid: "+uid+"\n---\nfirst\n")
	writeVaultFile(t, v, "b.md", "---\nid: "+uid+"\n---\nsecond\n")
	n, err := db.SyncIdentityFromVault(ctx, v)
	if err != nil {
		t.Fatalf("duplicate uid must not abort sync: %v", err)
	}
	if n != 1 {
		t.Errorf("written = %d, want 1 (second path skipped)", n)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM note_identity WHERE uid = ?`, uid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("uid rows = %d, want 1", count)
	}
	if p, ok, _ := db.PathByUID(ctx, uid); !ok || p != "a.md" {
		t.Errorf("PathByUID = %q,%v want a.md,true", p, ok)
	}
}

func TestIdentityMap(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	id1 := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	id2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV01"
	if err := db.UpsertIdentity(ctx, "a.md", id1); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertIdentity(ctx, "b.md", id2); err != nil {
		t.Fatal(err)
	}
	m, err := db.IdentityMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[id1] != "a.md" || m[id2] != "b.md" {
		t.Errorf("map = %v, want two id→path entries", m)
	}
}

func TestDeleteIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	if err := db.UpsertIdentity(ctx, "a.md", uid); err != nil {
		t.Fatal(err)
	}
	// Deleting an unknown path is not an error.
	if err := db.DeleteIdentity(ctx, "missing.md"); err != nil {
		t.Errorf("DeleteIdentity(missing): %v", err)
	}
	if err := db.DeleteIdentity(ctx, "a.md"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.UIDByPath(ctx, "a.md"); ok {
		t.Error("deleted path still resolves")
	}
}
