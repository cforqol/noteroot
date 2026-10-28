package httpapi

import (
	"net/http"

	"noteroot/internal/backup"
)

// createBackup handles POST /n/{slug}/backup — creates a git bundle of the
// vault and a VACUUM INTO snapshot of state.db in .notebook/backups/.
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	m, err := backup.Create(
		r.Context(),
		nb.Layout.Vault(),
		nb.Layout.StateDB(),
		nb.Layout.Backups(),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backup failed: " + err.Error()})
		return
	}

	if len(m.Errors) > 0 {
		s.log.Warn("backup completed with errors",
			"notebook", nb.Manifest.Slug,
			"errors", m.Errors,
		)
	}

	writeJSON(w, http.StatusOK, m)
}
