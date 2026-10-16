package platform

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, _, err := Open(context.Background(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestToken_createLookupListRevoke(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	created, err := db.TokenCreate(ctx, "ci", "hash-a", []string{"note:read", "search:read"})
	if err != nil {
		t.Fatalf("TokenCreate: %v", err)
	}
	if created.ID == "" || created.Name != "ci" || created.Revoked() {
		t.Fatalf("created = %+v", created)
	}

	got, err := db.TokenByHash(ctx, "hash-a")
	if err != nil {
		t.Fatalf("TokenByHash: %v", err)
	}
	if got == nil || got.ID != created.ID {
		t.Fatalf("lookup = %+v, want id %s", got, created.ID)
	}
	if got.Spec() != "note:read search:read" {
		t.Errorf("spec = %q", got.Spec())
	}
	if got.Revoked() || !got.LastUsedAt.IsZero() {
		t.Errorf("fresh token state wrong: %+v", got)
	}

	missing, err := db.TokenByHash(ctx, "no-such-hash")
	if err != nil || missing != nil {
		t.Errorf("missing lookup = %v, %v; want nil, nil", missing, err)
	}

	if err := db.TokenTouch(ctx, created.ID); err != nil {
		t.Fatalf("TokenTouch: %v", err)
	}
	touched, _ := db.TokenByHash(ctx, "hash-a")
	if touched.LastUsedAt.IsZero() {
		t.Error("last_used not updated after touch")
	}

	list, err := db.TokenList(ctx)
	if err != nil {
		t.Fatalf("TokenList: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	// Revoke: first call revokes, second is a no-op, unknown id is ErrNoRows.
	fresh, err := db.TokenRevoke(ctx, created.ID)
	if err != nil || !fresh {
		t.Fatalf("revoke = %v, %v; want true, nil", fresh, err)
	}
	fresh, err = db.TokenRevoke(ctx, created.ID)
	if err != nil || fresh {
		t.Fatalf("second revoke = %v, %v; want false, nil", fresh, err)
	}
	revoked, err := db.TokenByHash(ctx, "hash-a")
	if err != nil || revoked == nil || !revoked.Revoked() {
		t.Fatalf("revoked lookup = %+v, %v", revoked, err)
	}
	if _, err := db.TokenRevoke(ctx, "missing-id"); err != sql.ErrNoRows {
		t.Errorf("revoke unknown = %v, want ErrNoRows", err)
	}
}

func TestToken_hashIsUnique(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if _, err := db.TokenCreate(ctx, "a", "same-hash", []string{"note:read"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := db.TokenCreate(ctx, "b", "same-hash", []string{"note:read"}); err == nil {
		t.Error("duplicate hash accepted, want unique constraint failure")
	}
}
