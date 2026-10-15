package resources

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"noteroot/internal/state"
)

func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, _, err := state.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewJournal(db.DB)
}

func TestBegin_createsPendingEntry(t *testing.T) {
	j := newTestJournal(t)
	id, err := j.Begin("photo.jpg", 1024, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	e, err := j.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.State != StatePending {
		t.Errorf("state = %q, want %q", e.State, StatePending)
	}
	if e.Filename != "photo.jpg" || e.Size != 1024 || e.MIME != "image/jpeg" {
		t.Errorf("entry = %+v", e)
	}
	if e.SHA != "" {
		t.Errorf("sha = %q, want empty for pending", e.SHA)
	}
}

func TestCommit_pendingToCommitted(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	sha := "d2a84f4b8b650937ec8f73cd8be2c74add5a911ba64df27458ed8229da804a26"
	if err := j.Commit(id, sha); err != nil {
		t.Fatal(err)
	}
	e, _ := j.Get(id)
	if e.State != StateCommitted {
		t.Errorf("state = %q, want %q", e.State, StateCommitted)
	}
	if e.SHA != sha {
		t.Errorf("sha = %q, want %q", e.SHA, sha)
	}
}

func TestCommit_emptySHARejected(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	if err := j.Commit(id, ""); err == nil {
		t.Error("want error for empty sha")
	}
}

func TestCommit_twiceRejected(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	sha := "d2a84f4b8b650937ec8f73cd8be2c74add5a911ba64df27458ed8229da804a26"
	if err := j.Commit(id, sha); err != nil {
		t.Fatal(err)
	}
	if err := j.Commit(id, sha); err != ErrBadState {
		t.Errorf("err = %v, want ErrBadState", err)
	}
}

func TestCommit_missingEntry(t *testing.T) {
	j := newTestJournal(t)
	if err := j.Commit("nonexistent", "abc"); err != ErrEntryNotFound {
		t.Errorf("err = %v, want ErrEntryNotFound", err)
	}
}

func TestAbort_pendingToAborted(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	if err := j.Abort(id); err != nil {
		t.Fatal(err)
	}
	e, _ := j.Get(id)
	if e.State != StateAborted {
		t.Errorf("state = %q, want %q", e.State, StateAborted)
	}
}

func TestAbort_twiceRejected(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	if err := j.Abort(id); err != nil {
		t.Fatal(err)
	}
	if err := j.Abort(id); err != ErrBadState {
		t.Errorf("err = %v, want ErrBadState", err)
	}
}

func TestAbort_committedRejected(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 10, "image/png")
	sha := "d2a84f4b8b650937ec8f73cd8be2c74add5a911ba64df27458ed8229da804a26"
	_ = j.Commit(id, sha)
	if err := j.Abort(id); err != ErrBadState {
		t.Errorf("err = %v, want ErrBadState", err)
	}
}

func TestAbort_missingEntry(t *testing.T) {
	j := newTestJournal(t)
	if err := j.Abort("nonexistent"); err != ErrEntryNotFound {
		t.Errorf("err = %v, want ErrEntryNotFound", err)
	}
}

func TestGet_missingEntry(t *testing.T) {
	j := newTestJournal(t)
	if _, err := j.Get("nonexistent"); err != ErrEntryNotFound {
		t.Errorf("err = %v, want ErrEntryNotFound", err)
	}
}

func TestList_filtersByState(t *testing.T) {
	j := newTestJournal(t)
	id1, _ := j.Begin("a.png", 1, "image/png")
	id2, _ := j.Begin("b.png", 2, "image/png")
	id3, _ := j.Begin("c.png", 3, "image/png")
	_ = j.Commit(id1, "sha1")
	_ = j.Abort(id2)

	pending, _ := j.List(StatePending, 0)
	if len(pending) != 1 || pending[0].ID != id3 {
		t.Errorf("pending = %v, want [%s]", pending, id3)
	}

	committed, _ := j.List(StateCommitted, 0)
	if len(committed) != 1 || committed[0].ID != id1 {
		t.Errorf("committed = %v, want [%s]", committed, id1)
	}

	aborted, _ := j.List(StateAborted, 0)
	if len(aborted) != 1 || aborted[0].ID != id2 {
		t.Errorf("aborted = %v, want [%s]", aborted, id2)
	}

	all, _ := j.List("", 0)
	if len(all) != 3 {
		t.Errorf("all = %d entries, want 3", len(all))
	}
}

func TestList_limit(t *testing.T) {
	j := newTestJournal(t)
	for i := 0; i < 5; i++ {
		_, _ = j.Begin("f.png", int64(i), "image/png")
	}
	entries, _ := j.List("", 3)
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

func TestPending_returnsOnlyPending(t *testing.T) {
	j := newTestJournal(t)
	id1, _ := j.Begin("a.png", 1, "image/png")
	id2, _ := j.Begin("b.png", 2, "image/png")
	_ = j.Commit(id1, "sha1")

	pending, _ := j.Pending()
	if len(pending) != 1 || pending[0].ID != id2 {
		t.Errorf("pending = %v, want [%s]", pending, id2)
	}
}

func TestReconcile_abortsWhenShouldAbort(t *testing.T) {
	j := newTestJournal(t)
	id1, _ := j.Begin("a.png", 1, "image/png")
	id2, _ := j.Begin("b.png", 2, "image/png")

	res, err := j.Reconcile(func(JournalEntry) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Aborted) != 2 {
		t.Errorf("aborted = %v, want 2 entries", res.Aborted)
	}
	// Both should be aborted now.
	e1, _ := j.Get(id1)
	e2, _ := j.Get(id2)
	if e1.State != StateAborted || e2.State != StateAborted {
		t.Errorf("states = %q, %q; want aborted, aborted", e1.State, e2.State)
	}
}

func TestReconcile_keepsWhenShouldNotAbort(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 1, "image/png")
	res, err := j.Reconcile(func(JournalEntry) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Aborted) != 0 {
		t.Errorf("aborted = %v, want none", res.Aborted)
	}
	e, _ := j.Get(id)
	if e.State != StatePending {
		t.Errorf("state = %q, want pending", e.State)
	}
}

func TestGC_removesOldAborted(t *testing.T) {
	j := newTestJournal(t)
	id, _ := j.Begin("a.png", 1, "image/png")
	_ = j.Abort(id)

	// Backdate the entry.
	_, err := j.db.Exec(`UPDATE upload_journal SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-48*time.Hour).UnixMilli(), id)
	if err != nil {
		t.Fatal(err)
	}

	n, err := j.GC(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if _, err := j.Get(id); err != ErrEntryNotFound {
		t.Errorf("entry still present after GC")
	}
}

func TestGC_keepsRecentAndCommitted(t *testing.T) {
	j := newTestJournal(t)
	id1, _ := j.Begin("a.png", 1, "image/png")
	_ = j.Abort(id1) // recent aborted — should survive
	id2, _ := j.Begin("b.png", 2, "image/png")
	_ = j.Commit(id2, "sha") // committed — should survive

	n, err := j.GC(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestCountByState(t *testing.T) {
	j := newTestJournal(t)
	id1, _ := j.Begin("a.png", 1, "image/png")
	_, _ = j.Begin("b.png", 2, "image/png")
	id3, _ := j.Begin("c.png", 3, "image/png")
	_ = j.Commit(id1, "sha")
	_ = j.Abort(id3)

	counts, err := j.CountByState()
	if err != nil {
		t.Fatal(err)
	}
	if counts[StatePending] != 1 || counts[StateCommitted] != 1 || counts[StateAborted] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestJournal_withStateDB_migrationApplied(t *testing.T) {
	// Verify the upload_journal table exists after Open.
	j := newTestJournal(t)
	var name string
	err := j.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='upload_journal'`).Scan(&name)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if name != "upload_journal" {
		t.Errorf("table %q not found", name)
	}
}
