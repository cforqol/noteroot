package httpapi

import (
	"context"
	"net/http"
	"time"
)

const gcGrace = 24 * time.Hour

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
	blobN, err := store.GC(gcGrace)
	if err != nil {
		s.log.Warn("gc: store failed", "notebook", nb.Manifest.Slug, "err", err)
	}
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
	for _, nb := range s.reg.List() {
		store, journal, cleanup, err := s.openResources(ctx, nb)
		if err != nil {
			s.log.Warn("gc sweep: open failed", "notebook", nb.Manifest.Slug, "err", err)
			continue
		}
		journalN, _ := journal.GC(gcGrace)
		blobN, _ := store.GC(gcGrace)
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
