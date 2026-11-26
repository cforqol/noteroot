package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/vault"
)

func TestReferencedSHAs_findsLinksAndIgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	sha := "a" + strings.Repeat("0", 63)
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(
		"see /n/work/resources/"+sha+" and a duplicate /n/work/resources/"+sha), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-markdown sibling: must not be scanned (and so cannot match).
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(
		"/n/work/resources/"+strings.Repeat("b", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	// A different notebook's slug in the URL must not match either.
	if err := os.WriteFile(filepath.Join(dir, "other.md"), []byte(
		"/n/otherbook/resources/"+strings.Repeat("c", 64)), 0o644); err != nil {
		t.Fatal(err)
	}

	refs, err := referencedSHAs(context.Background(), vault.Open(dir), "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || !refs[sha] {
		t.Errorf("refs = %v, want just {%s}", refs, sha)
	}
}

func TestReferencedSHAs_emptyVault(t *testing.T) {
	refs, err := referencedSHAs(context.Background(), vault.Open(t.TempDir()), "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want empty", refs)
	}
}

func TestReferencedSHAs_canceledContextStopsScanning(t *testing.T) {
	dir := t.TempDir()
	sha := strings.Repeat("d", 64)
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("/n/work/resources/"+sha), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	refs, err := referencedSHAs(ctx, vault.Open(dir), "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want empty: an already-canceled context must stop the scan before reading any file", refs)
	}
}
