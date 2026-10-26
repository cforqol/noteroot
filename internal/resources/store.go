package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is the interface every resource backend implements. LocalStore is
// the v1 implementation; a future S3/MinIO store will satisfy the same
// interface so the journal and HTTP layers never change.
type Store interface {
	// Put streams content into the store, returning the hex sha256 of the
	// bytes written, the size, and whether the blob was newly created
	// (false = dedup hit, already present).
	Put(r io.Reader) (sha string, size int64, created bool, err error)
	// Get opens a reader for the blob. Caller closes it.
	Get(sha string) (io.ReadCloser, error)
	// Has reports whether the blob exists.
	Has(sha string) bool
	// Size returns the blob size in bytes. Errors if missing.
	Size(sha string) (int64, error)
	// Delete removes the blob. Missing blobs are not an error.
	Delete(sha string) error
	// GC removes blobs that are NOT in keep and whose last-access time
	// is older than grace, returning the number removed. keep is the
	// reachability set from the upload journal (Journal.LiveSHAs): a
	// blob in keep is referenced by a committed upload and is never
	// removed, however old it is. Passing nil or an empty map means
	// "nothing is reachable" — callers must only do that after
	// confirming the journal really holds no committed blobs.
	GC(grace time.Duration, keep map[string]bool) (int, error)
	// Touch updates the last-access time (called on every Get).
	Touch(sha string)
}

// ErrNotFound is returned by Get/Size for a missing blob.
var ErrNotFound = errors.New("resource not found")

// LocalStore is a content-addressed blob store on the local filesystem.
// Blobs are stored at <root>/<aa>/<sha> where <aa> is the first two hex
// characters of the sha256 — a two-level fan-out that keeps directory
// sizes reasonable for millions of blobs.
//
// Writes are atomic: a temp file in the target directory, fsync, rename,
// directory fsync. A crash mid-write leaves only a temp file that a later
// GC pass reaps; it never leaves a partial blob that looks valid.
type LocalStore struct {
	root string
}

// NewLocalStore creates the store root if missing and returns the store.
func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, errors.New("resources: empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("resources: mkdir root: %w", err)
	}
	return &LocalStore{root: root}, nil
}

// Root returns the filesystem root of the store.
func (s *LocalStore) Root() string { return s.root }

// pathFor returns the absolute path for a blob sha. The sha must be a
// 64-character lowercase hex string (sha256).
func (s *LocalStore) pathFor(sha string) (string, error) {
	if !validSHA(sha) {
		return "", fmt.Errorf("resources: invalid sha %q", sha)
	}
	return filepath.Join(s.root, sha[:2], sha), nil
}

func validSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if c < '0' || (c > '9' && c < 'a') || c > 'f' {
			return false
		}
	}
	return true
}

// ValidSHA reports whether s is a 64-character lowercase hex digest.
// Exported so handlers can validate client-supplied shas before they
// reach store keys or presigned URLs.
func ValidSHA(s string) bool { return validSHA(s) }

// Put streams r into the store, hashing as it writes. If the blob already
// exists the temp file is discarded (dedup hit, created=false). Delegates
// to PutBuffered — the streaming temp-file path is the only implementation.
func (s *LocalStore) Put(r io.Reader) (string, int64, bool, error) {
	return s.PutBuffered(r)
}

// PutBuffered is the streaming variant: it writes to a temp file while
// hashing, then renames into place. It returns the sha, size, and created
// flag. This is the path the HTTP upload handler uses.
func (s *LocalStore) PutBuffered(r io.Reader) (string, int64, bool, error) {
	// Write to a temp file in the store root, hashing as we go.
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", 0, false, fmt.Errorf("resources: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if renamed

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		_ = tmp.Close()
		return "", 0, false, fmt.Errorf("resources: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, false, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, false, err
	}

	sha := hex.EncodeToString(h.Sum(nil))
	target, err := s.pathFor(sha)
	if err != nil {
		return "", 0, false, err
	}
	if _, err := os.Stat(target); err == nil {
		// Dedup hit.
		s.Touch(sha)
		return sha, n, false, nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", 0, false, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", 0, false, fmt.Errorf("resources: rename: %w", err)
	}
	// fsync the fan-out directory so the rename is durable.
	if d, err := os.Open(filepath.Dir(target)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return sha, n, true, nil
}

// Get opens the blob for reading and updates its access time.
func (s *LocalStore) Get(sha string) (io.ReadCloser, error) {
	p, err := s.pathFor(sha)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.Touch(sha)
	return f, nil
}

// Has reports whether the blob exists on disk.
func (s *LocalStore) Has(sha string) bool {
	p, err := s.pathFor(sha)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Size returns the blob size, or ErrNotFound.
func (s *LocalStore) Size(sha string) (int64, error) {
	p, err := s.pathFor(sha)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return fi.Size(), nil
}

// Delete removes the blob and prunes its empty fan-out directory.
func (s *LocalStore) Delete(sha string) error {
	p, err := s.pathFor(sha)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Prune the two-char fan-out dir if empty.
	dir := filepath.Dir(p)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
	return nil
}

// Touch updates the file's access time to now. Best-effort; failures are
// ignored (some filesystems mount with noatime).
func (s *LocalStore) Touch(sha string) {
	p, err := s.pathFor(sha)
	if err != nil {
		return
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
}

// GC walks the store and removes blobs that are not in keep and whose
// mtime is older than grace — reachable blobs (keep) are never touched
// regardless of age. It also removes orphan temp files (".upload-*")
// older than one hour. Returns the number of blobs removed.
func (s *LocalStore) GC(grace time.Duration, keep map[string]bool) (int, error) {
	cutoff := time.Now().Add(-grace)
	removed := 0

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fmt.Errorf("resources: gc root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			// Orphan temp file at the root (from a crashed PutBuffered).
			if strings.HasPrefix(e.Name(), ".upload-") {
				if info, err := e.Info(); err == nil && info.ModTime().Before(time.Now().Add(-time.Hour)) {
					_ = os.Remove(filepath.Join(s.root, e.Name()))
				}
			}
			continue
		}
		// Each two-char fan-out directory.
		sub := filepath.Join(s.root, e.Name())
		blobs, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, b := range blobs {
			if b.IsDir() {
				continue
			}
			// Referenced by a committed upload: keep it, however old.
			if keep[b.Name()] {
				continue
			}
			bp := filepath.Join(sub, b.Name())
			info, err := b.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if err := os.Remove(bp); err == nil {
					removed++
				}
			}
		}
		// Prune empty fan-out dirs.
		if left, err := os.ReadDir(sub); err == nil && len(left) == 0 {
			_ = os.Remove(sub)
		}
	}
	return removed, nil
}

// NewStore returns the appropriate Store backend. When s3Cfg.Enabled()
// is true it returns an S3Store; otherwise a LocalStore rooted at
// localRoot. This is the single factory the HTTP layer uses so store
// selection lives in one place.
func NewStore(localRoot string, s3Cfg S3Config) (Store, error) {
	if s3Cfg.Enabled() {
		return NewS3Store(s3Cfg)
	}
	return NewLocalStore(localRoot)
}
