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

func TestRenameIdentity_roundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV02"
	if _, err := db.SyncIdentityPaths(ctx, map[string]string{"a.md": id}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameIdentity(ctx, "a.md", "b.md"); err != nil {
		t.Fatal(err)
	}
	uid, ok, err := db.UIDByPath(ctx, "b.md")
	if err != nil || !ok || uid != id {
		t.Errorf("UIDByPath(b.md) = %q,%v,%v want %q,true,nil", uid, ok, err, id)
	}
	if _, ok, _ := db.UIDByPath(ctx, "a.md"); ok {
		t.Error("old path still resolves after rename")
	}
	p, ok, err := db.PathByUID(ctx, id)
	if err != nil || !ok || p != "b.md" {
		t.Errorf("PathByUID = %q,%v,%v want b.md,true,nil", p, ok, err)
	}
	// Renaming a path with no row is a no-op, not an error.
	if err := db.RenameIdentity(ctx, "ghost.md", "elsewhere.md"); err != nil {
		t.Errorf("rename of missing path: %v", err)
	}
}

func TestSyncIdentityPaths_changedAndGone(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	id1 := "01ARZ3NDEKTSV4RRFFQ69G5FAV03"
	id2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV04"
	if _, err := db.SyncIdentityPaths(ctx, map[string]string{
		"a.md": id1,
		"b.md": id2,
		// Empty id leaves any existing row alone.
		"c.md": "",
	}, nil); err != nil {
		t.Fatal(err)
	}
	n, err := db.SyncIdentityPaths(ctx, map[string]string{"a.md": id2}, []string{"b.md"})
	if err != nil {
		t.Fatal(err)
	}
	_ = n
	if _, ok, _ := db.UIDByPath(ctx, "b.md"); ok {
		t.Error("gone path still resolves")
	}
	if _, ok, _ := db.PathByUID(ctx, id1); ok {
		t.Error("superseded uid still resolves")
	}
	p, ok, err := db.PathByUID(ctx, id2)
	if err != nil || !ok || p != "a.md" {
		t.Errorf("PathByUID(id2) = %q,%v,%v want a.md", p, ok, err)
	}
}

func TestPathByUID_missing(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	if _, ok, err := db.PathByUID(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAVZ9"); err != nil || ok {
		t.Errorf("missing uid: ok=%v err=%v", ok, err)
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

func TestRenameIdentityPrefix(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	ids := map[string]string{
		"old/a.md":     "01ARZ3NDEKTSV4RRFFQ69G5FA1",
		"old/sub/b.md": "01ARZ3NDEKTSV4RRFFQ69G5FA2",
		"older/c.md":   "01ARZ3NDEKTSV4RRFFQ69G5FA3", // shares a name prefix, not a folder
		"other.md":     "01ARZ3NDEKTSV4RRFFQ69G5FA4",
	}
	if _, err := db.SyncIdentityPaths(ctx, ids, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameIdentityPrefix(ctx, "old", "ünï/new"); err != nil {
		t.Fatal(err)
	}
	for oldP, newP := range map[string]string{
		"old/a.md":     "ünï/new/a.md",
		"old/sub/b.md": "ünï/new/sub/b.md",
		"older/c.md":   "older/c.md",
		"other.md":     "other.md",
	} {
		uid, ok, err := db.UIDByPath(ctx, newP)
		if err != nil || !ok || uid != ids[oldP] {
			t.Errorf("UIDByPath(%s) = %q,%v,%v want %q", newP, uid, ok, err, ids[oldP])
		}
	}
	if _, ok, _ := db.UIDByPath(ctx, "old/a.md"); ok {
		t.Error("old path still resolves")
	}
}

func TestRenameIdentityPrefix_skipsCollisions(t *testing.T) {
	ctx := context.Background()
	db := openTestLocksDB(t)
	if _, err := db.SyncIdentityPaths(ctx, map[string]string{
		"old/a.md": "01ARZ3NDEKTSV4RRFFQ69G5FB1",
		"old/b.md": "01ARZ3NDEKTSV4RRFFQ69G5FB2",
		"new/a.md": "01ARZ3NDEKTSV4RRFFQ69G5FB3", // stale row already at the target
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameIdentityPrefix(ctx, "old", "new"); err != nil {
		t.Fatalf("collision must not error: %v", err)
	}
	if uid, ok, _ := db.UIDByPath(ctx, "new/b.md"); !ok || uid != "01ARZ3NDEKTSV4RRFFQ69G5FB2" {
		t.Errorf("non-colliding row not moved: %q %v", uid, ok)
	}
	if uid, _, _ := db.UIDByPath(ctx, "new/a.md"); uid != "01ARZ3NDEKTSV4RRFFQ69G5FB3" {
		t.Errorf("existing target row clobbered: %q", uid)
	}
}
