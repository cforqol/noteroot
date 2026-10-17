package state

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent first-opens of the same database path used to race the
// migration transactions: a deferred read transaction cannot upgrade
// to a write under WAL, so losers got SQLITE_BUSY and HTTP handlers
// saw "resources unavailable". Opens of the same path are now
// serialized.
func TestOpen_concurrentSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := Open(context.Background(), path)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
}

// Different paths must not serialize behind each other — and must all
// still work when opened concurrently.
func TestOpen_concurrentDistinctPaths(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := Open(context.Background(), filepath.Join(dir, "db"+string(rune('a'+i))))
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
}
