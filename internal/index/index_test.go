package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"noteroot/internal/vault"
)

func writeNote(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	idx, rebuilt, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt {
		t.Fatal("fresh index must not report rebuild")
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func buildFixture(t *testing.T) (*vault.Vault, *Index) {
	t.Helper()
	root := t.TempDir()
	writeNote(t, root, "index.md", "---\ntitle: Home\ntags: [start, hub]\n---\nWelcome home body\n")
	writeNote(t, root, "projects/plan.md", "---\ntitle: Plan\n---\nShip the **bold** feature\n")
	writeNote(t, root, "plain.md", "just some words about gardening\n")
	writeNote(t, root, "skip.txt", "not a note\n")
	v := vault.Open(root)
	return v, openTestIndex(t)
}

func TestOpen_createsSchema(t *testing.T) {
	_, idx := buildFixture(t)
	var name string
	for _, want := range []string{"docs", "docs_fts", "docs_fts_data", "meta"} {
		if err := idx.db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, want).Scan(&name); err != nil {
			t.Errorf("table %q missing: %v", want, err)
		}
	}
}

func TestOpen_corruptRebuilt(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	if err := os.WriteFile(dbPath, []byte("garbage-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, rebuilt, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if !rebuilt {
		t.Error("corrupt index.db: expected rebuilt=true")
	}
	if _, err := os.Stat(dbPath + ".corrupt"); err != nil {
		t.Errorf("expected quarantine file: %v", err)
	}
}

func TestBuild_indexesNotes(t *testing.T) {
	v, idx := buildFixture(t)
	stats, err := idx.Build(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 {
		t.Errorf("Files = %d, want 3 (skip.txt must not be indexed)", stats.Files)
	}

	hits, err := idx.Search(context.Background(), "bold", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "projects/plan.md" {
		t.Fatalf("hits = %+v, want [projects/plan.md]", hits)
	}
	if hits[0].Title != "Plan" {
		t.Errorf("Title = %q, want Plan", hits[0].Title)
	}
	if hits[0].Snippet == "" {
		t.Error("expected non-empty snippet")
	}
}

func TestBuild_titleFallsBackToFilename(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "gardening", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "plain" {
		t.Fatalf("hits = %+v, want title fallback 'plain'", hits)
	}
}

func TestBuild_tagsSearchable(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "hub", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "index.md" {
		t.Fatalf("hits = %+v, want [index.md]", hits)
	}
}

func TestBuild_idempotent(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	stats, err := idx.Build(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 {
		t.Errorf("Files = %d, want 3", stats.Files)
	}
	var n int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM docs`).Scan(&n); err != nil || n != 3 {
		t.Errorf("docs rows = %d, %v; want 3", n, err)
	}
}

func TestBuild_emptyVault(t *testing.T) {
	v := vault.Open(t.TempDir())
	idx := openTestIndex(t)
	stats, err := idx.Build(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 0 {
		t.Errorf("Files = %d, want 0", stats.Files)
	}
	hits, err := idx.Search(context.Background(), "anything", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("hits = %+v, want none", hits)
	}
}

func TestBuild_malformedFrontmatterStillIndexed(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "n.md", "---\n: : [\n---\nfindable body text")
	v := vault.Open(root)
	idx := openTestIndex(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "findable", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want 1", hits)
	}
}

func TestUpdate_picksUpModifiedFile(t *testing.T) {
	v, idx := buildFixture(t)
	ctx := context.Background()
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	root := v.Root()
	writeNote(t, root, "plain.md", "just some words about submarines\n")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "plain.md"), past, past); err != nil {
		t.Fatal(err)
	}

	stats, err := idx.Update(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Errorf("Updated = %d, want 1", stats.Updated)
	}
	hits, err := idx.Search(ctx, "submarines", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "plain.md" {
		t.Fatalf("hits = %+v, want [plain.md]", hits)
	}
	if hits, err := idx.Search(ctx, "gardening", 10); err != nil || len(hits) != 0 {
		t.Errorf("stale term hits = %+v, %v; want none", hits, err)
	}
}

func TestUpdate_noChanges(t *testing.T) {
	v, idx := buildFixture(t)
	ctx := context.Background()
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	stats, err := idx.Update(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 0 || stats.Deleted != 0 {
		t.Errorf("stats = %+v, want no updates or deletes", stats)
	}
}

func TestUpdate_removesDeletedFile(t *testing.T) {
	v, idx := buildFixture(t)
	ctx := context.Background()
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(v.Root(), "plain.md")); err != nil {
		t.Fatal(err)
	}
	stats, err := idx.Update(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", stats.Deleted)
	}
	if hits, err := idx.Search(ctx, "gardening", 10); err != nil || len(hits) != 0 {
		t.Errorf("deleted note hits = %+v, %v; want none", hits, err)
	}
}

func TestUpdate_skipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	writeNote(t, root, "ok.md", "visible content\n")
	writeNote(t, root, "locked.md", "hidden content\n")
	if err := os.Chmod(filepath.Join(root, "locked.md"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked.md"), 0o644) })

	v := vault.Open(root)
	idx := openTestIndex(t)
	ctx := context.Background()
	stats, err := idx.Build(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want 2 scanned and 1 skipped", stats)
	}
	if hits, err := idx.Search(ctx, "visible", 10); err != nil || len(hits) != 1 {
		t.Errorf("readable note hits = %+v, %v; want 1", hits, err)
	}
	if hits, err := idx.Search(ctx, "hidden", 10); err != nil || len(hits) != 0 {
		t.Errorf("unreadable note hits = %+v, %v; want none", hits, err)
	}
}

func TestSearch_noMatch(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "xyznonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("hits = %+v, want none", hits)
	}
}

func TestSearch_badQueryIsError(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Search(context.Background(), `"unterminated`, 10); err == nil {
		t.Error("malformed FTS5 query: expected error, not a panic or silent match")
	}
}

func TestSearch_limit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writeNote(t, root, filepath.Join("n", string(rune('a'+i))+".md"), "commonword filler\n")
	}
	v := vault.Open(root)
	idx := openTestIndex(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "commonword", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("len(hits) = %d, want 2", len(hits))
	}
}

func TestBuild_missingVaultRoot(t *testing.T) {
	idx := openTestIndex(t)
	missing := vault.Open(filepath.Join(t.TempDir(), "gone"))
	if _, err := idx.Build(context.Background(), missing); err == nil {
		t.Error("Build on missing vault root: expected error")
	}
	if _, err := idx.Update(context.Background(), missing); err == nil {
		t.Error("Update on missing vault root: expected error")
	}
}

func TestBuild_cancelledContext(t *testing.T) {
	v, idx := buildFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := idx.Build(ctx, v); err == nil {
		t.Error("Build with cancelled context: expected error")
	}
}

func TestOpen_unwritablePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(context.Background(), filepath.Join(blocker, "index.db")); err == nil {
		t.Error("Open under a regular file: expected error")
	}
}

func TestSearch_defaultLimit(t *testing.T) {
	v, idx := buildFixture(t)
	if _, err := idx.Build(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "welcome", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("limit 0: hits = %+v, want 1 (default limit applies)", hits)
	}
}
