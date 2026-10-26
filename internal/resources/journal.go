package resources

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Journal states.
const (
	StatePending   = "pending"
	StateCommitted = "committed"
	StateAborted   = "aborted"
	StateDeleted   = "deleted"
)

// JournalEntry is one row of the upload journal.
type JournalEntry struct {
	ID        string
	Filename  string
	Size      int64
	MIME      string
	SHA       string // filled on commit; "" while pending/aborted
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrEntryNotFound is returned for a missing journal ID.
var ErrEntryNotFound = errors.New("journal entry not found")

// ErrBadState is returned when a transition is invalid (e.g. commit twice).
var ErrBadState = errors.New("invalid journal state transition")

// Journal tracks in-flight and historical uploads in the state DB. It is
// the crash-recovery record: a pending entry means the upload started but
// the process died before commit/abort. Reconcile() resolves those.
//
// The journal never talks to the Store directly — the HTTP layer wires
// them together. This keeps the journal testable without filesystem I/O.
type Journal struct {
	db *sql.DB
}

// NewJournal wraps an already-open state DB.
func NewJournal(db *sql.DB) *Journal {
	return &Journal{db: db}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Begin records the start of an upload and returns the new entry ID.
// The entry is StatePending until Commit or Abort.
func (j *Journal) Begin(filename string, size int64, mime string) (string, error) {
	id := newID()
	now := time.Now().UnixMilli()
	_, err := j.db.Exec(`
		INSERT INTO upload_journal (id, filename, size, mime, sha, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', ?, ?, ?)`,
		id, filename, size, mime, StatePending, now, now)
	if err != nil {
		return "", fmt.Errorf("journal begin: %w", err)
	}
	return id, nil
}

// Commit marks a pending entry as committed and records the blob SHA.
// The SHA must be non-empty. Committing a non-pending entry is ErrBadState.
func (j *Journal) Commit(id, sha string) error {
	if sha == "" {
		return errors.New("journal commit: empty sha")
	}
	res, err := j.db.Exec(`
		UPDATE upload_journal
		SET sha = ?, state = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		sha, StateCommitted, time.Now().UnixMilli(), id, StatePending)
	if err != nil {
		return fmt.Errorf("journal commit: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Distinguish "not found" from "wrong state".
		var exists bool
		_ = j.db.QueryRow(`SELECT 1 FROM upload_journal WHERE id = ?`, id).Scan(&exists)
		if !exists {
			return ErrEntryNotFound
		}
		return ErrBadState
	}
	return nil
}

// Abort marks a pending entry as aborted. Aborting a non-pending entry is
// ErrBadState; aborting a missing entry is ErrEntryNotFound.
func (j *Journal) Abort(id string) error {
	res, err := j.db.Exec(`
		UPDATE upload_journal
		SET state = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		StateAborted, time.Now().UnixMilli(), id, StatePending)
	if err != nil {
		return fmt.Errorf("journal abort: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		_ = j.db.QueryRow(`SELECT 1 FROM upload_journal WHERE id = ?`, id).Scan(&exists)
		if !exists {
			return ErrEntryNotFound
		}
		return ErrBadState
	}
	return nil
}

// Get returns one entry by ID.
func (j *Journal) Get(id string) (JournalEntry, error) {
	var e JournalEntry
	var createdAt, updatedAt int64
	err := j.db.QueryRow(`
		SELECT id, filename, size, mime, sha, state, created_at, updated_at
		FROM upload_journal WHERE id = ?`, id).Scan(
		&e.ID, &e.Filename, &e.Size, &e.MIME, &e.SHA, &e.State, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return JournalEntry{}, ErrEntryNotFound
	}
	if err != nil {
		return JournalEntry{}, fmt.Errorf("journal get: %w", err)
	}
	e.CreatedAt = time.UnixMilli(createdAt)
	e.UpdatedAt = time.UnixMilli(updatedAt)
	return e, nil
}

// List returns entries filtered by state ("" = all), newest first.
func (j *Journal) List(state string, limit int) ([]JournalEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
		SELECT id, filename, size, mime, sha, state, created_at, updated_at
		FROM upload_journal`
	args := []any{}
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := j.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("journal list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var createdAt, updatedAt int64
		if err := rows.Scan(&e.ID, &e.Filename, &e.Size, &e.MIME, &e.SHA,
			&e.State, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(createdAt)
		e.UpdatedAt = time.UnixMilli(updatedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MIMEBySHA returns the recorded MIME of committed uploads, keyed by
// blob sha, for the given shas. Unlike List it is not capped: serving a
// blob must find its MIME however many uploads came after it. Shas with
// no committed entry (or an empty MIME) are absent from the result; when
// a blob was uploaded more than once the newest entry wins.
func (j *Journal) MIMEBySHA(shas ...string) (map[string]string, error) {
	out := make(map[string]string, len(shas))
	const chunk = 500
	for start := 0; start < len(shas); start += chunk {
		end := start + chunk
		if end > len(shas) {
			end = len(shas)
		}
		part := shas[start:end]
		marks := make([]byte, 0, len(part)*2)
		args := make([]any, 0, len(part)+1)
		args = append(args, StateCommitted)
		for i, s := range part {
			if i > 0 {
				marks = append(marks, ',')
			}
			marks = append(marks, '?')
			args = append(args, s)
		}
		rows, err := j.db.Query(`
			SELECT sha, mime FROM upload_journal
			WHERE state = ? AND mime != '' AND sha IN (`+string(marks)+`)
			ORDER BY created_at DESC`, args...)
		if err != nil {
			return nil, fmt.Errorf("journal mime lookup: %w", err)
		}
		for rows.Next() {
			var sha, mime string
			if err := rows.Scan(&sha, &mime); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, seen := out[sha]; !seen {
				out[sha] = mime
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// Pending returns all entries still in StatePending — the crash-recovery set.
func (j *Journal) Pending() ([]JournalEntry, error) {
	return j.List(StatePending, 0)
}

// ReconcileResult summarizes what Reconcile did.
type ReconcileResult struct {
	// Committed lists IDs whose blobs were found in the store and promoted
	// from pending to committed (crash between store.Put and journal.Commit).
	Committed []string
	// Aborted lists IDs whose blobs were NOT in the store and were aborted
	// (crash before or during store.Put).
	Aborted []string
}

// Reconcile resolves pending entries after a crash. For each pending entry
// it asks `hasBlob(sha)` — but a pending entry has no SHA yet, so the
// caller passes a resolver that maps entry ID → expected store presence.
//
// In practice the HTTP layer does:
//
//	for each pending entry:
//	    if the upload temp file still exists and hashes to a store blob:
//	        journal.Commit(entry.ID, sha)
//	    else:
//	        journal.Abort(entry.ID)
//
// Because v1 uploads are single-shot (no chunk resume), a pending entry
// after crash almost always means the upload was lost; Reconcile aborts
// them. The API is still useful for observability and for a future
// chunked-resume implementation.
func (j *Journal) Reconcile(shouldAbort func(JournalEntry) bool) (ReconcileResult, error) {
	pending, err := j.Pending()
	if err != nil {
		return ReconcileResult{}, err
	}
	var res ReconcileResult
	for _, e := range pending {
		if shouldAbort(e) {
			if err := j.Abort(e.ID); err != nil {
				return res, fmt.Errorf("reconcile abort %s: %w", e.ID, err)
			}
			res.Aborted = append(res.Aborted, e.ID)
		}
	}
	return res, nil
}

// GC removes aborted entries older than the given age. Committed entries
// are retained as an audit trail; pending entries are never GC'd (they
// are the crash-recovery set until Reconcile runs).
func (j *Journal) GC(olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan).UnixMilli()
	res, err := j.db.Exec(`
		DELETE FROM upload_journal
		WHERE state = ? AND updated_at < ?`,
		StateAborted, cutoff)
	if err != nil {
		return 0, fmt.Errorf("journal gc: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// MarkDeleted transitions all committed entries with a given SHA to the
// deleted state. It is a soft delete: the audit trail is kept, but the
// entries are excluded from the default listing. It is a no-op when no
// committed entry has that SHA (returns nil).
func (j *Journal) MarkDeleted(sha string) error {
	_, err := j.db.Exec(`
		UPDATE upload_journal
		SET state = ?, updated_at = ?
		WHERE sha = ? AND state = ?`,
		StateDeleted, time.Now().UnixMilli(), sha, StateCommitted)
	if err != nil {
		return fmt.Errorf("journal mark deleted: %w", err)
	}
	return nil
}

// TotalStoredBytes returns the sum of sizes for unique committed blobs.
// Because blobs are content-addressed, deduped uploads count once.
func (j *Journal) TotalStoredBytes(ctx context.Context) (int64, error) {
	var total int64
	err := j.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM (SELECT sha, size FROM upload_journal
		      WHERE state = ? AND sha != ''
		      GROUP BY sha)`, StateCommitted).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("journal total bytes: %w", err)
	}
	return total, nil
}

// ReservationTTL is the minimum age at which a pending journal entry
// stops counting toward the quota. Pending entries are the
// crash-recovery set and are never GC'd before Reconcile runs, so an
// abandoned presigned upload must not reserve quota forever; callers
// pass a cutoff of at least this age (longer when the configured
// presign TTL demands it).
const ReservationTTL = 30 * time.Minute

// ReservedBytes sums the sizes of pending entries updated at or after
// updatedAfter — the quota still claimed by uploads that began but
// have not committed or aborted yet. Combined with a mutex around
// check+Begin, this closes the concurrent-upload quota race: every
// in-flight upload is visible to the next checker either as committed
// bytes or as a reservation.
func (j *Journal) ReservedBytes(ctx context.Context, updatedAfter time.Time) (int64, error) {
	var total int64
	err := j.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM upload_journal
		WHERE state = ? AND updated_at >= ?`,
		StatePending, updatedAfter.UnixMilli()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("journal reserved bytes: %w", err)
	}
	return total, nil
}

// QuotaSnapshot reads committed and reserved bytes in ONE explicit
// transaction. Implicit reads on a pooled modernc/sqlite connection
// can piggy-back a stale WAL snapshot left behind by earlier
// statements under concurrency — observed as concurrent upload checks
// silently missing each other's pending rows — while an explicit
// transaction always begins a fresh snapshot. Read-only; rolls back.
func (j *Journal) QuotaSnapshot(ctx context.Context, updatedAfter time.Time) (used, reserved int64, err error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("journal quota snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM (SELECT sha, size FROM upload_journal
		      WHERE state = ? AND sha != ''
		      GROUP BY sha)`, StateCommitted).Scan(&used); err != nil {
		return 0, 0, fmt.Errorf("journal quota snapshot: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size), 0)
		FROM upload_journal
		WHERE state = ? AND updated_at >= ?`,
		StatePending, updatedAfter.UnixMilli()).Scan(&reserved); err != nil {
		return 0, 0, fmt.Errorf("journal quota snapshot: %w", err)
	}
	return used, reserved, nil
}

// SetSize corrects a pending entry's recorded size — Begin may have
// charged a policy maximum when the declared size was unknown (-1),
// and the real size is only known after the blob is stored. Only
// pending entries are updated; a missing or non-pending entry is
// ErrEntryNotFound/ErrBadState.
func (j *Journal) SetSize(id string, size int64) error {
	if size < 0 {
		return errors.New("journal set size: negative size")
	}
	res, err := j.db.Exec(`
		UPDATE upload_journal
		SET size = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		size, time.Now().UnixMilli(), id, StatePending)
	if err != nil {
		return fmt.Errorf("journal set size: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		_ = j.db.QueryRow(`SELECT 1 FROM upload_journal WHERE id = ?`, id).Scan(&exists)
		if !exists {
			return ErrEntryNotFound
		}
		return ErrBadState
	}
	return nil
}

// CountByState returns the number of entries in each state. Useful for
// metrics and the /api/v1/resources/stats endpoint.
func (j *Journal) CountByState() (map[string]int, error) {
	rows, err := j.db.Query(`SELECT state, COUNT(*) FROM upload_journal GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// LiveSHAs returns the set of SHAs a store sweep must never remove:
// every blob with a committed journal entry. Soft-deleted entries are
// not live — MarkDeleted exists precisely so a later GC can take the
// blob — and pending/aborted entries hold no committed sha.
//
// This is the reachability set Store.GC sweeps against. An age-based
// sweep without it deletes every referenced blob, which is exactly the
// bug that emptied a real notebook's store.
func (j *Journal) LiveSHAs() (map[string]bool, error) {
	rows, err := j.db.Query(
		`SELECT DISTINCT sha FROM upload_journal WHERE state = ? AND sha != ''`,
		StateCommitted,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out[sha] = true
	}
	return out, rows.Err()
}
