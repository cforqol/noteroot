package httpapi

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/vault"
)

// referencedSHAs scans every markdown file in the vault for a
// /n/{slug}/resources/{sha} link — the URL shape lib/upload.js embeds
// for every attachment a note links to — and returns the set of
// referenced SHAs. It is the forward twin of listResourceRefs (which
// goes sha -> paths); this goes vault -> shas. Bounded the same way:
// a 3-second timeout and a 2000-file cap, so a huge vault cannot block
// a request indefinitely.
func referencedSHAs(ctx context.Context, v *vault.Vault, slug string) (map[string]bool, error) {
	re := regexp.MustCompile(`/n/` + regexp.QuoteMeta(slug) + `/resources/([a-f0-9]{64})`)
	out := map[string]bool{}
	scanned := 0
	const maxFiles = 2000

	err := filepath.WalkDir(v.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		scanned++
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range re.FindAllSubmatch(data, -1) {
			out[string(bytes.ToLower(m[1]))] = true
		}
		return nil
	})
	return out, err
}

// reconcileAfterRebuild closes the gap a state.db rebuild opens for
// object storage: the upload journal — the only local record of which
// blobs this notebook owns — was just wiped, so a blob that was
// confirmed (journal.Commit) before the rebuild would otherwise look
// gone, and become eligible for GC despite still being in the bucket
// and still linked from a note.
//
// It does not list the bucket wholesale: every notebook shares one S3
// bucket (openResources builds S3Config from server-wide config, not
// per-notebook), so a raw listing can't tell one notebook's blobs from
// another's. The vault's own note text is the ownership signal instead
// — the same role docs/adr/0002's "resource_sources: note refs plus
// resource metadata" row describes — checked against the store so
// nothing is adopted that was never actually uploaded.
//
// Called at most meaningfully once: openResources only passes
// rebuilt=true on the Open call that itself performed the rebuild.
func (s *Server) reconcileAfterRebuild(ctx context.Context, nb *notebooks.Notebook, journal *resources.Journal, store resources.Store) {
	s3Store, ok := store.(*resources.S3Store)
	if !ok {
		return
	}
	scanCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	refs, err := referencedSHAs(scanCtx, vault.Open(nb.Layout.Vault()), nb.Manifest.Slug)
	if err != nil {
		s.log.Warn("resources: rebuild reconcile scan failed", "notebook", nb.Manifest.Slug, "err", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	live, err := journal.LiveSHAs()
	if err != nil {
		s.log.Warn("resources: rebuild reconcile live-set failed", "notebook", nb.Manifest.Slug, "err", err)
		return
	}
	recovered := 0
	for sha := range refs {
		if live[sha] {
			continue
		}
		size, err := s3Store.Size(sha)
		if err != nil {
			continue // not in the bucket (or unreachable): nothing to recover
		}
		if err := journal.AdoptRecovered(sha, size); err != nil {
			s.log.Warn("resources: rebuild reconcile adopt failed",
				"notebook", nb.Manifest.Slug, "sha", sha, "err", err)
			continue
		}
		recovered++
	}
	if recovered > 0 {
		s.log.Info("resources: recovered blobs from object storage after a state.db rebuild",
			"notebook", nb.Manifest.Slug, "count", recovered)
	}
}
