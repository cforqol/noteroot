package resources

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *LocalStore {
	t.Helper()
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewLocalStore_emptyRoot(t *testing.T) {
	if _, err := NewLocalStore(""); err == nil {
		t.Error("want error for empty root")
	}
}

func TestNewLocalStore_createsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b")
	s, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root not created: %v", err)
	}
	if s.Root() != root {
		t.Errorf("Root() = %q, want %q", s.Root(), root)
	}
}

func TestPut_storesAndReturnsSHA(t *testing.T) {
	s := newTestStore(t)
	content := []byte("hello world")
	sha, size, created, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	// sha256 of "hello world"
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if sha != want {
		t.Errorf("sha = %q, want %q", sha, want)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if !created {
		t.Error("created = false, want true for new blob")
	}
	if !s.Has(sha) {
		t.Error("Has(sha) = false after Put")
	}
}

func TestPut_deduplicates(t *testing.T) {
	s := newTestStore(t)
	content := []byte("same bytes")
	sha1, _, _, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	sha2, _, created, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Errorf("sha mismatch: %q vs %q", sha1, sha2)
	}
	if created {
		t.Error("created = true on dedup hit, want false")
	}
}

func TestPut_fanoutPath(t *testing.T) {
	s := newTestStore(t)
	sha, _, _, err := s.Put(bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Root(), sha[:2], sha)
	if _, err := os.Stat(want); err != nil {
		t.Errorf("blob not at fan-out path %q: %v", want, err)
	}
}

func TestGet_returnsContent(t *testing.T) {
	s := newTestStore(t)
	content := []byte("readable content")
	sha, _, _, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestGet_notFound(t *testing.T) {
	s := newTestStore(t)
	sha := strings.Repeat("a", 64)
	if _, err := s.Get(sha); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_invalidSHA(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("not-a-sha"); err == nil {
		t.Error("want error for invalid sha")
	}
	if _, err := s.Get(strings.Repeat("A", 64)); err == nil {
		t.Error("want error for uppercase sha (non-canonical)")
	}
}

func TestSize(t *testing.T) {
	s := newTestStore(t)
	content := []byte("12345")
	sha, _, _, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	size, err := s.Size(sha)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if _, err := s.Size(strings.Repeat("b", 64)); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHas(t *testing.T) {
	s := newTestStore(t)
	sha, _, _, _ := s.Put(bytes.NewReader([]byte("x")))
	if !s.Has(sha) {
		t.Error("Has = true expected")
	}
	if s.Has(strings.Repeat("c", 64)) {
		t.Error("Has = false expected for missing")
	}
	if s.Has("bad") {
		t.Error("Has = false for invalid sha")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	sha, _, _, _ := s.Put(bytes.NewReader([]byte("delete me")))
	if err := s.Delete(sha); err != nil {
		t.Fatal(err)
	}
	if s.Has(sha) {
		t.Error("still has blob after Delete")
	}
	// Delete of missing blob is not an error.
	if err := s.Delete(sha); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

func TestDelete_prunesEmptyFanoutDir(t *testing.T) {
	s := newTestStore(t)
	sha, _, _, _ := s.Put(bytes.NewReader([]byte("only one")))
	_ = s.Delete(sha)
	dir := filepath.Join(s.Root(), sha[:2])
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("fan-out dir not pruned: %v", err)
	}
}

func TestTouch_updatesMtime(t *testing.T) {
	s := newTestStore(t)
	sha, _, _, _ := s.Put(bytes.NewReader([]byte("touch")))
	p := filepath.Join(s.Root(), sha[:2], sha)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	s.Touch(sha)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Error("Touch did not update mtime")
	}
}

func TestGC_removesOldBlobs(t *testing.T) {
	s := newTestStore(t)
	oldSha, _, _, _ := s.Put(bytes.NewReader([]byte("old")))
	newSha, _, _, _ := s.Put(bytes.NewReader([]byte("new")))

	// Age the old blob.
	oldPath := filepath.Join(s.Root(), oldSha[:2], oldSha)
	ago := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, ago, ago); err != nil {
		t.Fatal(err)
	}

	removed, err := s.GC(24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if s.Has(oldSha) {
		t.Error("old blob still present after GC")
	}
	if !s.Has(newSha) {
		t.Error("new blob incorrectly removed by GC")
	}
}

// A blob the journal still references must survive GC no matter how old
// its file is: reachability, not age, decides what is swept.
func TestGC_keepsReferencedBlobs(t *testing.T) {
	s := newTestStore(t)
	oldSha, _, _, _ := s.Put(bytes.NewReader([]byte("referenced and old")))
	otherSha, _, _, _ := s.Put(bytes.NewReader([]byte("orphan and old")))

	ago := time.Now().Add(-72 * time.Hour)
	for _, sha := range []string{oldSha, otherSha} {
		p := filepath.Join(s.Root(), sha[:2], sha)
		if err := os.Chtimes(p, ago, ago); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.GC(24*time.Hour, map[string]bool{oldSha: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only the orphan)", removed)
	}
	if !s.Has(oldSha) {
		t.Error("referenced blob was reclaimed — GC ignored the reachability set")
	}
	if s.Has(otherSha) {
		t.Error("orphan blob still present after GC")
	}

	// Dropping it from the set makes it collectable on the next pass.
	removed, err = s.GC(24*time.Hour, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || s.Has(oldSha) {
		t.Errorf("after unreference: removed = %d (want 1), has = %v (want false)",
			removed, s.Has(oldSha))
	}
}

func TestGC_orphanTempFiles(t *testing.T) {
	s := newTestStore(t)
	tmp := filepath.Join(s.Root(), ".upload-stale")
	if err := os.WriteFile(tmp, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age it past one hour.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(tmp, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GC(0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("orphan temp file not removed")
	}
}

func TestPut_largeContent(t *testing.T) {
	s := newTestStore(t)
	// 5 MiB of random-ish data.
	content := make([]byte, 5<<20)
	for i := range content {
		content[i] = byte(i)
	}
	sha, size, created, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if !created {
		t.Error("created = false")
	}
	// Round-trip verify.
	rc, err := s.Get(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("round-trip mismatch")
	}
}

func TestStoreInterface(t *testing.T) {
	// Compile-time check that LocalStore satisfies Store.
	var _ Store = (*LocalStore)(nil)
}
