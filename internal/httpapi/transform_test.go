package httpapi

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransformCacheKey(t *testing.T) {
	a := transformCacheKey("abc123", "thumb", "400", "400")
	b := transformCacheKey("abc123", "thumb", "800", "400")
	if a == b {
		t.Error("different params produced the same cache key")
	}
	if transformCacheKey("abc123", "thumb", "400", "400") != a {
		t.Error("same params produced different cache keys")
	}
	if transformCacheKey("abc123", "poster") == a {
		t.Error("different kinds produced the same cache key")
	}
	if transformCacheKey("zzz999", "thumb", "400", "400") == a {
		t.Error("different shas produced the same cache key")
	}
}

func TestCachedTransform_hitMiss(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	genCalls := 0
	gen := func() ([]byte, error) {
		genCalls++
		return []byte("jpeg-bytes"), nil
	}

	out, hit, err := cachedTransform(ctx, dir, "k.jpg", gen)
	if err != nil || hit || string(out) != "jpeg-bytes" || genCalls != 1 {
		t.Fatalf("miss = %q,%v,%v calls=%d", out, hit, err, genCalls)
	}
	out, hit, err = cachedTransform(ctx, dir, "k.jpg", gen)
	if err != nil || !hit || string(out) != "jpeg-bytes" || genCalls != 1 {
		t.Fatalf("hit = %q,%v,%v calls=%d", out, hit, err, genCalls)
	}
}

func TestCachedTransform_genError(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("ffmpeg died")
	_, _, err := cachedTransform(context.Background(), dir, "k.jpg", func() ([]byte, error) {
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestCachedTransform_emptyCacheRefills(t *testing.T) {
	dir := t.TempDir()
	// A zero-length cache file (crashed write) must not be served.
	if err := os.WriteFile(filepath.Join(dir, "k.jpg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, hit, err := cachedTransform(context.Background(), dir, "k.jpg", func() ([]byte, error) {
		return []byte("fresh"), nil
	})
	if err != nil || hit || string(out) != "fresh" {
		t.Fatalf("empty cache served: %q hit=%v err=%v", out, hit, err)
	}
}

func TestCachedTransform_concurrent(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	calls := 0
	gen := func() ([]byte, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return []byte("jpeg-bytes"), nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := cachedTransform(context.Background(), dir, "k.jpg", gen)
			if err != nil || string(out) != "jpeg-bytes" {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent caller failed: %v", err)
	}
}

type memStore struct{ blobs map[string][]byte }

func (m memStore) Put(r io.Reader) (string, int64, bool, error) { panic("unused") }
func (m memStore) Get(sha string) (io.ReadCloser, error) {
	b, ok := m.blobs[sha]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}
func (m memStore) Has(sha string) bool { _, ok := m.blobs[sha]; return ok }
func (m memStore) Size(sha string) (int64, error) {
	b, ok := m.blobs[sha]
	if !ok {
		return 0, errors.New("not found")
	}
	return int64(len(b)), nil
}
func (m memStore) Delete(sha string) error                            { delete(m.blobs, sha); return nil }
func (m memStore) GC(_ time.Duration, _ map[string]bool) (int, error) { return 0, nil }
func (m memStore) Touch(_ string)                                     {}

func TestMaterializeSource_roundTrip(t *testing.T) {
	st := memStore{blobs: map[string][]byte{"abc": []byte("hello")}}
	p, cleanup, err := materializeSource(st, "abc")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "hello" {
		t.Fatalf("materialized = %q, %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the temp file")
	}
}

func TestMaterializeSource_missing(t *testing.T) {
	st := memStore{blobs: map[string][]byte{}}
	if _, _, err := materializeSource(st, "nope"); err == nil {
		t.Error("missing blob must fail")
	}
}
