package httpapi

import (
	"context"
	"net/http"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
)

const gcGrace = 24 * time.Hour

// sweepBlobs removes unreachable blobs from the store and returns how
// many went. Blobs with a committed journal entry are always kept: the
// journal is the reachability set, and an age-only sweep would reclaim
// every referenced blob the first time it runs (it did once).
//
// When the journal holds no rows at all there is nothing to check a
// store against — a lost or freshly recreated state DB must not be read
// as "nothing is referenced", so the store is left alone and the caller
// is told via a warning.
func (s *Server) sweepBlobs(nb *notebooks.Notebook, store resources.Store, journal *resources.Journal) int {
	live, err := journal.LiveSHAs()
	if err != nil {
		s.log.Warn("gc: live shas unreadable; skipping store sweep",
			"notebook", nb.Manifest.Slug, "err", err)
		return 0
	}
	if len(live) == 0 {
		counts, cerr := journal.CountByState()
		if cerr == nil && len(counts) == 0 {
			s.log.Warn("gc: journal empty; refusing to sweep store",
				"notebook", nb.Manifest.Slug, "blobs_maybe_present", true)
			return 0
		}
	}
	n, err := store.GC(gcGrace, live)
	if err != nil {
		s.log.Warn("gc: store failed", "notebook", nb.Manifest.Slug, "err", err)
		return 0
	}
	return n
}

// gcResources handles POST /n/{slug}/resources/gc — on-demand GC sweep.
// It removes old aborted journal entries and unreachable store blobs.
func (s *Server) gcResources(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resources unavailable"})
		return
	}
	defer cleanup()

	journalN, err := journal.GC(gcGrace)
	if err != nil {
		s.log.Warn("gc: journal failed", "notebook", nb.Manifest.Slug, "err", err)
	}
	blobN := s.sweepBlobs(nb, store, journal)
	writeJSON(w, http.StatusOK, map[string]any{
		"journalEntriesRemoved": journalN,
		"blobsRemoved":          blobN,
	})
}

// startGC launches the background GC goroutine. It runs once per GCInterval.
// Call stopGC() (returned) to stop it on shutdown.
func (s *Server) startGC() (stopGC func()) {
	d, err := time.ParseDuration(s.cfg.GCInterval)
	if err != nil || d <= 0 {
		d = 24 * time.Hour
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.runGCSweep()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// runGCSweep runs a GC pass over all notebooks. Errors are logged and skipped.
func (s *Server) runGCSweep() {
	ctx := context.Background()
	// Trash retention rides the same ticker: a long-lived server would
	// otherwise only ever sweep the trash at boot.
	s.purgeTrash()
	for _, nb := range s.reg.List() {
		store, journal, cleanup, err := s.openResources(ctx, nb)
		if err != nil {
			s.log.Warn("gc sweep: open failed", "notebook", nb.Manifest.Slug, "err", err)
			continue
		}
		journalN, _ := journal.GC(gcGrace)
		blobN := s.sweepBlobs(nb, store, journal)
		cleanup()
		if journalN > 0 || blobN > 0 {
			s.log.Info("gc sweep",
				"notebook", nb.Manifest.Slug,
				"journal_entries_removed", journalN,
				"blobs_removed", blobN,
			)
		}
	}
}
