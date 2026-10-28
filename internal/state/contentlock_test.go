package state

import (
	"context"
	"errors"
	"testing"
)

func TestLockConfigCRUD(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()

	if err := db.SetLockConfig(ctx, "dir/", KindFolder, "h", "s"); err != nil {
		t.Fatal(err)
	}
	c, ok, err := db.GetLockConfig(ctx, "dir/")
	if err != nil || !ok {
		t.Fatalf("GetLockConfig = %v, %v", ok, err)
	}
	if c.Kind != KindFolder || c.Hash != "h" || c.Salt != "s" {
		t.Errorf("got %+v", c)
	}

	// Replace keeps one row.
	if err := db.SetLockConfig(ctx, "dir/", KindFolder, "h2", "s2"); err != nil {
		t.Fatal(err)
	}
	all, err := db.LockConfigs(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("LockConfigs = %v, %v", all, err)
	}
	if all[0].Hash != "h2" {
		t.Errorf("replace failed: %+v", all[0])
	}

	if err := db.DeleteLockConfig(ctx, "dir/"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GetLockConfig(ctx, "dir/"); ok {
		t.Error("row survived delete")
	}
	if err := db.DeleteLockConfig(ctx, "dir/"); !errors.Is(err, ErrNoLockConfig) {
		t.Errorf("delete missing = %v, want ErrNoLockConfig", err)
	}
}

func TestLocksForAncestors(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	_ = db.SetLockConfig(ctx, "", KindNotebook, "h", "s")
	_ = db.SetLockConfig(ctx, "a/", KindFolder, "h", "s")
	_ = db.SetLockConfig(ctx, "a/b.md", KindNote, "h", "s")
	_ = db.SetLockConfig(ctx, "other/", KindFolder, "h", "s")

	cases := []struct {
		target string
		want   []string
	}{
		{"a/b.md", []string{"", "a/", "a/b.md"}},
		{"a/c.md", []string{"", "a/"}},
		{"a/", []string{"", "a/"}},
		{"", []string{""}},
		{"other/x.md", []string{"", "other/"}},
		{"z.md", []string{""}},
	}
	for _, c := range cases {
		got, err := db.LocksFor(ctx, c.target)
		if err != nil {
			t.Fatalf("LocksFor(%q): %v", c.target, err)
		}
		if len(got) != len(c.want) {
			t.Errorf("LocksFor(%q) len = %d (%v), want %d", c.target, len(got), pathsOf(got), len(c.want))
			continue
		}
		for i, p := range c.want {
			if got[i].Path != p {
				t.Errorf("LocksFor(%q)[%d] = %q, want %q", c.target, i, got[i].Path, p)
			}
		}
	}
}

func pathsOf(cs []LockConfig) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Path
	}
	return out
}

func TestDeleteLockConfigClearsSessions(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	_ = db.SetLockConfig(ctx, "a/", KindFolder, "h", "s")
	if _, err := db.CreateSession(ctx, "a/", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLockConfig(ctx, "a/"); err != nil {
		t.Fatal(err)
	}
	paths, err := db.SessionPaths(ctx, []string{"tok"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("session survived config delete: %v", paths)
	}
}

func TestSessionLifecycle(t *testing.T) {
	db := openTestLocksDB(t)
	ctx := context.Background()
	exp, err := db.CreateSession(ctx, "dir/", "tok1")
	if err != nil {
		t.Fatal(err)
	}
	if exp.IsZero() {
		t.Error("zero expiry")
	}
	paths, err := db.SessionPaths(ctx, []string{"tok1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "dir/" {
		t.Errorf("SessionPaths = %v", paths)
	}
	if err := db.DeleteSession(ctx, "tok1"); err != nil {
		t.Fatal(err)
	}
	if paths, _ := db.SessionPaths(ctx, []string{"tok1"}); len(paths) != 0 {
		t.Errorf("session not deleted: %v", paths)
	}
}

func TestSessionPathsEmpty(t *testing.T) {
	db := openTestLocksDB(t)
	paths, err := db.SessionPaths(context.Background(), nil)
	if err != nil || paths != nil {
		t.Errorf("SessionPaths(nil) = %v, %v", paths, err)
	}
}
