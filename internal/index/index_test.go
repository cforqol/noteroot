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
	dir := t.TempDir()
	idx, rebuilt, err := Open(context.Background(), filepath.Join(dir, "index.db"), filepath.Join(dir, "state.db"))
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
	idx, rebuilt, err := Open(context.Background(), filepath.Join(dir, "index.db"), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()
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
	if _, _, err := Open(context.Background(),
		filepath.Join(blocker, "index.db"), filepath.Join(blocker, "state.db")); err == nil {
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

func TestBuildResolver(t *testing.T) {
	docs := []doc{
		{path: "a.md", title: "Note A", id: "01ARZ3NDEKTSV4RRFFQ69G5FAV00", aliases: "alpha"},
		{path: "b.md", title: "Note B", id: "01ARZ3NDEKTSV4RRFFQ69G5FAV01", aliases: "beta\ngamma"},
		{path: "c.md", title: "Duplicate", id: "ID003", aliases: ""},
		{path: "d.md", title: "Duplicate", id: "", aliases: "alpha"}, // duplicate alias
	}
	res := buildResolver(docs, nil)

	// ID resolution (well-formed ids only)
	if res["01ARZ3NDEKTSV4RRFFQ69G5FAV00"] != "a.md" {
		t.Errorf("01ARZ3NDEKTSV4RRFFQ69G5FAV00 should resolve to a.md")
	}
	if _, ok := res["ID003"]; ok {
		t.Errorf("malformed id must not resolve")
	}

	// Path resolution
	if res["a.md"] != "a.md" {
		t.Errorf("a.md should resolve to a.md")
	}
	if res["a"] != "a.md" {
		t.Errorf("a (stem) should resolve to a.md")
	}

	// Unique title resolution
	if res["Note A"] != "a.md" {
		t.Errorf("Note A should resolve to a.md")
	}

	// Ambiguous title should not resolve
	if _, ok := res["Duplicate"]; ok {
		t.Errorf("Duplicate title should not be in resolver")
	}

	// Unique alias resolution (newline-joined multi-word storage)
	if res["beta"] != "b.md" {
		t.Errorf("beta should resolve to b.md")
	}

	// Ambiguous alias should not resolve
	if _, ok := res["alpha"]; ok {
		t.Errorf("Duplicate alias 'alpha' should not be in resolver")
	}
}

func TestBuildResolver_identityMapWinsAndFiltersStale(t *testing.T) {
	docs := []doc{
		{path: "lost.md", title: "Lost", id: ""}, // frontmatter id lost
		{path: "other.md", title: "Other", id: "01ARZ3NDEKTSV4RRFFQ69G5FAV09"},
	}
	idMap := map[string]string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV00": "lost.md",  // resolves despite lost frontmatter id
		"01ARZ3NDEKTSV4RRFFQ69G5FAV02": "gone.md",  // stale: path not indexed anymore
		"01ARZ3NDEKTSV4RRFFQ69G5FAV09": "other.md", // map owns the id
		"not-a-valid-id":               "other.md", // malformed uid is never a key
	}
	res := buildResolver(docs, idMap)
	if res["01ARZ3NDEKTSV4RRFFQ69G5FAV00"] != "lost.md" {
		t.Errorf("identity map must resolve ids whose frontmatter id was lost, got %q",
			res["01ARZ3NDEKTSV4RRFFQ69G5FAV00"])
	}
	if _, ok := res["01ARZ3NDEKTSV4RRFFQ69G5FAV02"]; ok {
		t.Error("identity row for a path missing from the index must not resolve")
	}
	if _, ok := res["not-a-valid-id"]; ok {
		t.Error("malformed uid must not become a resolver key")
	}
}

func TestBuildResolver_titleBeatsAlias(t *testing.T) {
	docs := []doc{
		{path: "alias.md", title: "Alias Note", aliases: "Shared"},
		{path: "titled.md", title: "Shared"},
	}
	res := buildResolver(docs, nil)
	// Design §5: the title class is consulted before the alias class.
	if res["Shared"] != "titled.md" {
		t.Errorf("Shared = %q, want titled.md (title beats another note's alias)", res["Shared"])
	}
}

func TestLinks_roundtrip(t *testing.T) {
	ctx := context.Background()
	idxDir := t.TempDir()
	idx, _, err := Open(ctx, filepath.Join(idxDir, "index.db"), filepath.Join(idxDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "index.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV00\ntitle: Index\n---\nSee [[projects]] and [[missing]].\n")
	writeNote(t, root, "projects.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV01\ntitle: Projects\n---\nRefers to [[Index]].\n")

	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}

	// Test backlinks
	back, err := idx.Backlinks(ctx, "index.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Path != "projects.md" {
		t.Errorf("Backlinks(index.md) = %+v, want [{Path:projects.md Title:Projects}]", back)
	}

	// Test outgoing
	out, err := idx.Outgoing(ctx, "index.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("Outgoing(index.md) = %+v, want 2 links", out)
	}
	// Sorted by target
	if out[0].Target != "missing" || out[0].Path != "" {
		t.Errorf("out[0] = %+v, want unresolved 'missing'", out[0])
	}
	if out[1].Target != "projects" || out[1].Path != "projects.md" {
		t.Errorf("out[1] = %+v, want resolved 'projects'", out[1])
	}
}

func TestLinks_resolutionModes(t *testing.T) {
	ctx := context.Background()
	idxDir := t.TempDir()
	idx, _, err := Open(ctx, filepath.Join(idxDir, "index.db"), filepath.Join(idxDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "test.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV00\ntitle: Test\naliases: [alias1, alias2]\n---\n"+
		"By ID: [[01ARZ3NDEKTSV4RRFFQ69G5FAV01]]\n"+
		"By path: [[target.md]]\n"+
		"By stem: [[target]]\n"+
		"By title: [[Target Note]]\n"+
		"By alias: [[myalias]]\n")
	writeNote(t, root, "target.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV01\ntitle: Target Note\naliases: [myalias]\n---\nbody\n")

	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}

	out, err := idx.Outgoing(ctx, "test.md")
	if err != nil {
		t.Fatal(err)
	}

	// All 5 links should resolve to target.md
	for _, link := range out {
		if link.Path != "target.md" {
			t.Errorf("link %q resolved to %q, want target.md", link.Target, link.Path)
		}
	}
}

func TestLinks_updatePreservesLinks(t *testing.T) {
	ctx := context.Background()
	idxDir := t.TempDir()
	idx, _, err := Open(ctx, filepath.Join(idxDir, "index.db"), filepath.Join(idxDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "a.md", "---\ntitle: A\n---\nlinks to [[b]]\n")
	writeNote(t, root, "b.md", "---\ntitle: B\n---\nno links\n")

	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}

	// Modify b.md
	time.Sleep(10 * time.Millisecond)
	writeNote(t, root, "b.md", "---\ntitle: B Updated\n---\nstill no links\n")

	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}

	// Links from a.md should still exist and resolve
	out, err := idx.Outgoing(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Path != "b.md" {
		t.Errorf("Outgoing after update = %+v, want link to b.md", out)
	}
}

func TestDebugResolver(t *testing.T) {
	ctx := context.Background()
	idxDir := t.TempDir()
	idx, _, err := Open(ctx, filepath.Join(idxDir, "index.db"), filepath.Join(idxDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "test.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV00\ntitle: Test\naliases: [alias1, alias2]\n---\nBy ID: [[01ARZ3NDEKTSV4RRFFQ69G5FAV01]]\n")
	writeNote(t, root, "target.md", "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV01\ntitle: Target Note\naliases: [myalias]\n---\nbody\n")

	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}

	// Check what's in the database
	rows, err := idx.db.QueryContext(ctx, `SELECT path, id, title, aliases FROM docs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var path, id, title, aliases string
		if err := rows.Scan(&path, &id, &title, &aliases); err != nil {
			t.Fatal(err)
		}
		t.Logf("Doc: path=%s, id=%s, title=%s, aliases=%s", path, id, title, aliases)
	}
}

func TestUpdate_reResolvesWhenTargetTitleChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "a.md", "---\ntitle: A\n---\nsee [[Target Note]]\n")
	writeNote(t, root, "b.md", "---\ntitle: Target Note\n---\nbody\n")
	idx := openTestIndex(t)
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	out, err := idx.Outgoing(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Path != "b.md" {
		t.Fatalf("Outgoing = %+v, want resolved b.md", out)
	}

	// Rename the target's title: the linking note never changed, yet its
	// stored row must go dead — rendered HTML reads this row (§7).
	time.Sleep(10 * time.Millisecond)
	writeNote(t, root, "b.md", "---\ntitle: Renamed\n---\nbody\n")
	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	out, err = idx.Outgoing(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Path != "" {
		t.Fatalf("Outgoing after title change = %+v, want dead link", out)
	}
}

func TestUpdate_reResolvesWhenTargetDeleted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "a.md", "---\ntitle: A\n---\nsee [[b]]\n")
	writeNote(t, root, "b.md", "---\ntitle: B\n---\nbody\n")
	idx := openTestIndex(t)
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	out, err := idx.Outgoing(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("Outgoing = %+v, want 1 row", out)
	}
	if out[0].Path != "" {
		t.Errorf("to_path = %q, want '' (stale link after target deletion)", out[0].Path)
	}
}

func TestLinks_labelStored(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "index.md", "See [[projects/plan|the plan]] and [[missing|gone]].\n")
	writeNote(t, root, "projects/plan.md", "body\n")
	idx := openTestIndex(t)
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	out, err := idx.Outgoing(ctx, "index.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("Outgoing = %+v, want 2 links", out)
	}
	if out[0].Target != "missing" || out[0].Label != "gone" {
		t.Errorf("out[0] = %+v, want target=missing label=gone", out[0])
	}
	if out[1].Target != "projects/plan" || out[1].Path != "projects/plan.md" || out[1].Label != "the plan" {
		t.Errorf("out[1] = %+v, want resolved with label", out[1])
	}
}

func TestLinks_codeSpanNotIndexed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := vault.Open(root)
	writeNote(t, root, "a.md", "literal `[[not_a_link]]` in a code span\n")
	idx := openTestIndex(t)
	if _, err := idx.Build(ctx, v); err != nil {
		t.Fatal(err)
	}
	out, err := idx.Outgoing(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("Outgoing = %+v, want none (code spans are never linkified)", out)
	}
}

func TestUpdate_backfillsIdentityFromVault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := vault.Open(root)
	id1 := "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	id2 := "01ARZ3NDEKTSV4RRFFQ69G5FAV01"
	writeNote(t, root, "external.md", "---\nid: "+id1+"\n---\nauthored outside the app\n")
	idx := openTestIndex(t)

	// First Update after a fresh open: marker missing → full backfill.
	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	if uid, ok, err := idx.st.UIDByPath(ctx, "external.md"); err != nil || !ok || uid != id1 {
		t.Errorf("UIDByPath(external.md) = %q,%v,%v want %q,true,nil", uid, ok, err, id1)
	}

	// A later external edit is picked up incrementally, without a full pass.
	time.Sleep(10 * time.Millisecond)
	writeNote(t, root, "later.md", "---\nid: "+id2+"\n---\nadded later\n")
	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	if uid, ok, err := idx.st.UIDByPath(ctx, "later.md"); err != nil || !ok || uid != id2 {
		t.Errorf("UIDByPath(later.md) = %q,%v,%v want %q,true,nil", uid, ok, err, id2)
	}

	// Deleting a note prunes its row so a stale id never resolves.
	if err := os.Remove(filepath.Join(root, "external.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := idx.st.UIDByPath(ctx, "external.md"); err != nil || ok {
		t.Errorf("UIDByPath(deleted) = ok=%v err=%v, want pruned", ok, err)
	}
}
