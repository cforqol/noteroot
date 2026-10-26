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

func TestTotalStoredBytes(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	sha1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// Empty — want 0.
	n, err := j.TotalStoredBytes(ctx)
	if err != nil || n != 0 {
		t.Fatalf("empty = (%d, %v), want (0, nil)", n, err)
	}

	// Two committed blobs.
	id1, _ := j.Begin("a.jpg", 100, "image/jpeg")
	_ = j.Commit(id1, sha1)
	id2, _ := j.Begin("b.jpg", 200, "image/jpeg")
	_ = j.Commit(id2, sha2)

	n, err = j.TotalStoredBytes(ctx)
	if err != nil || n != 300 {
		t.Errorf("two blobs = (%d, %v), want (300, nil)", n, err)
	}

	// Dedup: same sha1 uploaded again — counted once.
	id3, _ := j.Begin("a-copy.jpg", 100, "image/jpeg")
	_ = j.Commit(id3, sha1)
	n, err = j.TotalStoredBytes(ctx)
	if err != nil || n != 300 {
		t.Errorf("after dedup = (%d, %v), want still (300, nil)", n, err)
	}

	// Pending and aborted entries don't count.
	_, _ = j.Begin("pending.jpg", 999, "image/jpeg")
	id5, _ := j.Begin("aborted.jpg", 500, "image/jpeg")
	_ = j.Abort(id5)
	n, err = j.TotalStoredBytes(ctx)
	if err != nil || n != 300 {
		t.Errorf("with pending/aborted = (%d, %v), want (300, nil)", n, err)
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

func TestReservedBytes_countsYoungPendingOnly(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	young, err := j.Begin("young.bin", 7, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	// Committed and aborted entries release their reservation.
	committed, _ := j.Begin("done.bin", 11, "application/octet-stream")
	_ = j.Commit(committed, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	aborted, _ := j.Begin("bad.bin", 13, "application/octet-stream")
	_ = j.Abort(aborted)
	// A pending entry older than the cutoff (abandoned presign, crash
	// leftover) must not reserve quota forever.
	old, _ := j.Begin("old.bin", 17, "application/octet-stream")
	if _, err := j.db.Exec(`UPDATE upload_journal SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UnixMilli(), old); err != nil {
		t.Fatal(err)
	}

	n, err := j.ReservedBytes(ctx, time.Now().Add(-ReservationTTL))
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("reserved = %d, want 7 (only the young pending entry)", n)
	}
	if e, err := j.Get(young); err != nil || e.State != StatePending {
		t.Errorf("young entry disturbed: %+v, %v", e, err)
	}
}

// QuotaSnapshot reads used+reserved in one explicit transaction —
// the fresh-snapshot reads the upload quota check depends on.
func TestQuotaSnapshot_usedAndReserved(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	used, reserved, err := j.QuotaSnapshot(ctx, time.Now().Add(-ReservationTTL))
	if err != nil || used != 0 || reserved != 0 {
		t.Fatalf("empty = (%d, %d, %v), want (0, 0, nil)", used, reserved, err)
	}

	c, _ := j.Begin("c.bin", 30, "application/octet-stream")
	_ = j.Commit(c, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	p, _ := j.Begin("p.bin", 7, "application/octet-stream")

	used, reserved, err = j.QuotaSnapshot(ctx, time.Now().Add(-ReservationTTL))
	if err != nil {
		t.Fatal(err)
	}
	if used != 30 || reserved != 7 {
		t.Errorf("used, reserved = %d, %d, want 30, 7", used, reserved)
	}

	// An old pending row (beyond the cutoff) is not reserved.
	if _, err := j.db.Exec(`UPDATE upload_journal SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-2*ReservationTTL).UnixMilli(), p); err != nil {
		t.Fatal(err)
	}
	_, reserved, err = j.QuotaSnapshot(ctx, time.Now().Add(-ReservationTTL))
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Errorf("reserved = %d, want 0 for stale pending", reserved)
	}
}

func TestSetSize_correctsPendingOnly(t *testing.T) {
	ctx := context.Background()
	j := newTestJournal(t)

	// Unknown-size flow: Begin charged the policy maximum, SetSize
	// records the real stored size before Commit.
	id, err := j.Begin("chunked.bin", 1<<20, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.SetSize(id, 42); err != nil {
		t.Fatal(err)
	}
	e, _ := j.Get(id)
	if e.Size != 42 {
		t.Errorf("size = %d, want 42", e.Size)
	}
	_ = j.Commit(id, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	total, err := j.TotalStoredBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 42 {
		t.Errorf("total = %d, want 42 (real stored size, not the charged maximum)", total)
	}

	// Missing and non-pending entries are refused.
	if err := j.SetSize("nonexistent", 1); err != ErrEntryNotFound {
		t.Errorf("missing entry err = %v, want ErrEntryNotFound", err)
	}
	if err := j.SetSize(id, 7); err != ErrBadState {
		t.Errorf("committed entry err = %v, want ErrBadState", err)
	}
	if err := j.SetSize("x", -1); err == nil {
		t.Error("negative size accepted, want error")
	}
}

func TestMIMEBySHA_findsOldBlobsPastTheListCap(t *testing.T) {
	j := newTestJournal(t)
	commit := func(name, mime, sha string) {
		id, err := j.Begin(name, 1, mime)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Commit(id, sha); err != nil {
			t.Fatal(err)
		}
	}
	commit("old.png", "image/png", "aaa")
	for i := 0; i < 120; i++ { // more than List's default cap of 100
		commit("f.txt", "text/plain", "sha"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	got, err := j.MIMEBySHA("aaa", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got["aaa"] != "image/png" || len(got) != 1 {
		t.Fatalf("MIMEBySHA = %v", got)
	}
	if m, err := j.MIMEBySHA(); err != nil || len(m) != 0 {
		t.Fatalf("no shas: %v %v", m, err)
	}
}

// LiveSHAs is the reachability set a store sweep must respect: every
// committed blob, and nothing that is pending, aborted or deleted.
func TestLiveSHAs_committedOnly(t *testing.T) {
	j := newTestJournal(t)

	keepSHA := "1111111111111111111111111111111111111111111111111111111111111111"
	id, _ := j.Begin("keep.png", 1, "image/png")
	if err := j.Commit(id, keepSHA); err != nil {
		t.Fatal(err)
	}

	goneSHA := "2222222222222222222222222222222222222222222222222222222222222222"
	id2, _ := j.Begin("gone.png", 1, "image/png")
	if err := j.Commit(id2, goneSHA); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkDeleted(goneSHA); err != nil {
		t.Fatal(err)
	}

	id3, _ := j.Begin("aborted.png", 1, "image/png")
	_ = j.Abort(id3)

	id4, _ := j.Begin("pending.png", 1, "image/png")
	_ = id4

	live, err := j.LiveSHAs()
	if err != nil {
		t.Fatal(err)
	}
	if !live[keepSHA] {
		t.Error("committed blob missing from the live set")
	}
	if live[goneSHA] {
		t.Error("soft-deleted blob is still considered live")
	}
	if len(live) != 1 {
		t.Errorf("live = %v, want exactly the committed sha", live)
	}
}

// An empty journal must report an empty set rather than an error: the
// caller decides whether sweeping against it is safe.
func TestLiveSHAs_emptyJournal(t *testing.T) {
	j := newTestJournal(t)
	live, err := j.LiveSHAs()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("live = %v, want empty", live)
	}
}
