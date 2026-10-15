package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// Locks are advisory: they tell other tabs a note is being edited, but no
// write path consults them. If-Match stays the correctness backstop, so a
// crashed tab can never wedge the vault.
func (s *Server) putLock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	holder := r.URL.Query().Get("holder")
	if rel == "" || holder == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path or holder"})
		return
	}
	v := vault.Open(nb.Layout.Vault())
	if _, err := v.Read(rel); err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}

	db, _, err := state.Open(r.Context(), nb.Layout.StateDB())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer func() { _ = db.Close() }()
	expires, err := db.AcquireLock(r.Context(), rel, holder, lockTTL(r))
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			current, exp, _, _ := db.Lock(r.Context(), rel)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "locked", "holder": current, "expiresAt": exp.Unix(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"holder": holder, "expiresAt": expires.Unix()})
}

func (s *Server) deleteLock(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	holder := r.URL.Query().Get("holder")
	if rel == "" || holder == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path or holder"})
		return
	}
	db, _, err := state.Open(r.Context(), nb.Layout.StateDB())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer func() { _ = db.Close() }()
	if err := db.ReleaseLock(r.Context(), rel, holder); err != nil {
		if errors.Is(err, state.ErrNoLock) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such lock"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unlock failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"released": rel})
}

func lockTTL(r *http.Request) time.Duration {
	n, err := strconv.Atoi(r.URL.Query().Get("ttl"))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
