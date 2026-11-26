package httpapi

// Integration tests for #45: a notebook's state.db rebuild (corrupt DB
// quarantined, fresh one created — see docs/adr/0002) must not lose
// track of a blob that was already confirmed via a presigned S3
// upload. reconcileAfterRebuild (resources_reconcile.go) re-adopts any
// blob the notebook's own notes still link to and that object storage
// still has.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

// writeNote writes a vault-relative note file directly, bypassing the
// HTTP API — used here to plant a reference to a resource sha before
// state.db is corrupted.
func writeNote(t *testing.T, reg *notebooks.Registry, slug, relPath, content string) {
	t.Helper()
	nb, ok := reg.Get(slug)
	if !ok {
		t.Fatalf("no notebook %s", slug)
	}
	p := filepath.Join(nb.Layout.Vault(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// corruptStateDB overwrites a notebook's state.db with garbage bytes so
// the next Open fails PRAGMA integrity_check and triggers a quarantine
// rebuild (same technique as index.TestOpen_corruptRebuilt). The file
// must already exist (created by some earlier request); callers that
// have not made one yet should via get(t, srv, ".../resources/stats", nil) first.
func corruptStateDB(t *testing.T, reg *notebooks.Registry, slug string) {
	t.Helper()
	nb, ok := reg.Get(slug)
	if !ok {
		t.Fatalf("no notebook %s", slug)
	}
	if err := os.MkdirAll(filepath.Dir(nb.Layout.StateDB()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nb.Layout.StateDB(), []byte("garbage-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resourcesList unwraps GET /n/{slug}/resources's {"resources": [...]}
// envelope.
func resourcesList(t *testing.T, srv *Server, slug string) []map[string]any {
	t.Helper()
	rec := get(t, srv, "/n/"+slug+"/resources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Resources []map[string]any `json:"resources"`
	}
	decodeJSON(t, rec, &resp)
	return resp.Resources
}

func TestReconcileAfterRebuild_recoversReferencedBlob(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("recoverable blob")
	sha := sha256HexOf(content)
	writeNote(t, reg, "work", "note.md", "see /n/work/resources/"+sha+" for the attachment")

	pre := s3Presign(t, srv, sha, int64(len(content)), "a.bin", "application/octet-stream")
	var p map[string]any
	decodeJSON(t, pre, &p)
	putRaw(t, p["uploadURL"].(string), content, "application/octet-stream")
	if c := s3Confirm(t, srv, p["entryID"].(string), sha); c.Code != http.StatusOK {
		t.Fatalf("confirm = %d", c.Code)
	}
	if counts := journalCounts(t, reg); counts["committed"] != 1 {
		t.Fatalf("counts before corruption = %v, want 1 committed", counts)
	}

	corruptStateDB(t, reg, "work")

	// Any request that opens resources (listResources here) triggers
	// openResources -> state.Open (rebuilt=true) -> reconcile.
	entries := resourcesList(t, srv, "work")
	if len(entries) != 1 {
		t.Fatalf("entries after rebuild = %+v, want 1 recovered entry", entries)
	}
	if entries[0]["SHA"] != sha {
		t.Errorf("recovered sha = %v, want %s", entries[0]["SHA"], sha)
	}
	if entries[0]["Size"].(float64) != float64(len(content)) {
		t.Errorf("recovered size = %v, want %d", entries[0]["Size"], len(content))
	}
	if entries[0]["State"] != "committed" {
		t.Errorf("recovered state = %v, want committed", entries[0]["State"])
	}

	if !f.has(sha) {
		t.Fatal("test setup: blob missing from fake S3")
	}
}

// A blob the bucket has but no note references is left alone: nothing
// ties it to this notebook, and every notebook shares one bucket.
func TestReconcileAfterRebuild_ignoresUnreferencedBlob(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	content := []byte("unreferenced blob")
	sha := sha256HexOf(content)
	// No writeNote call: nothing in the vault links to this sha.

	pre := s3Presign(t, srv, sha, int64(len(content)), "a.bin", "application/octet-stream")
	var p map[string]any
	decodeJSON(t, pre, &p)
	putRaw(t, p["uploadURL"].(string), content, "application/octet-stream")
	s3Confirm(t, srv, p["entryID"].(string), sha)

	corruptStateDB(t, reg, "work")

	entries := resourcesList(t, srv, "work")
	if len(entries) != 0 {
		t.Errorf("entries after rebuild = %+v, want none recovered (unreferenced)", entries)
	}
}

// A note links to a sha the bucket does not actually have (e.g. the
// upload never finished): nothing to recover, and no phantom entry
// is created.
func TestReconcileAfterRebuild_referencedButMissingFromStoreIsSkipped(t *testing.T) {
	f := newS3Fake(t)
	reg := resourceFixture(t)
	srv := newS3Server(t, reg, f, nil)

	sha := sha256HexOf([]byte("never uploaded"))
	writeNote(t, reg, "work", "note.md", "dead link: /n/work/resources/"+sha)

	// Create state.db (no prior upload touched it yet) before corrupting it.
	if rec := get(t, srv, "/n/work/resources/stats", nil); rec.Code != http.StatusOK {
		t.Fatalf("initial stats = %d", rec.Code)
	}
	corruptStateDB(t, reg, "work")

	entries := resourcesList(t, srv, "work")
	if len(entries) != 0 {
		t.Errorf("entries after rebuild = %+v, want none (blob never existed)", entries)
	}
}

// A non-S3 (local) store has no reconciliation to do: rebuilding is
// still just "the journal is empty now", nothing more.
func TestReconcileAfterRebuild_noopWithoutS3(t *testing.T) {
	reg := resourceFixture(t)
	srv := newTestServer(t, config.AuthNone, reg)
	writeNote(t, reg, "work", "note.md", "/n/work/resources/"+sha256HexOf([]byte("x")))

	if rec := get(t, srv, "/n/work/resources/stats", nil); rec.Code != http.StatusOK {
		t.Fatalf("initial stats = %d", rec.Code)
	}
	corruptStateDB(t, reg, "work")

	rec := get(t, srv, "/n/work/resources/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats after rebuild (local store) = %d, body = %s", rec.Code, rec.Body.String())
	}
}
