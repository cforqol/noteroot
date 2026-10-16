package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"noteroot/internal/git"
	"noteroot/internal/notebooks"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// moveNote handles PATCH /n/{slug}/note — rename or move a note within
// the vault. The uid (ULID) travels with the file so no link rewriting
// is needed.
//
// Request body:
//
//	{"from": "old/path.md", "to": "new/path.md"}
//
// Response (200):
//
//	{"from": "old/path.md", "to": "new/path.md", "uid": "01ABCDEF..."}
func (s *Server) moveNote(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	if req.From == "" || req.To == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}

	v := vault.Open(nb.Layout.Vault())
	if err := v.Rename(req.From, req.To); err != nil {
		switch {
		case errors.Is(err, vault.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "source not found"})
		case errors.Is(err, vault.ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "destination already exists"})
		case errors.Is(err, vault.ErrEscapes):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path escapes vault"})
		default:
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		}
		return
	}

	uid := s.moveIdentity(r, nb, req.From, req.To)
	s.reindex(r, nb, v)
	s.markGitDirty(nb, git.Op{Action: "move", Path: req.To, Actor: actorFrom(r)})
	s.emit(r, nb, "note.rename", req.To, map[string]any{"from": req.From, "uid": uid})

	writeJSON(w, http.StatusOK, map[string]any{
		"from": req.From,
		"to":   req.To,
		"uid":  uid,
	})
}

// moveIdentity updates note_identity for the rename and returns the uid.
// Failure is logged but never surfaced — state is rebuildable.
func (s *Server) moveIdentity(r *http.Request, nb *notebooks.Notebook, oldPath, newPath string) string {
	db, _, err := state.Open(r.Context(), nb.Layout.StateDB())
	if err != nil {
		s.log.Warn("move: state open failed", "notebook", nb.Manifest.Slug, "err", err)
		return ""
	}
	defer func() { _ = db.Close() }()
	uid, _, _ := db.UIDByPath(r.Context(), oldPath)
	if err := db.RenameIdentity(r.Context(), oldPath, newPath); err != nil {
		s.log.Warn("move: identity update failed",
			"from", oldPath, "to", newPath, "err", err)
	}
	return uid
}

func actorFrom(r *http.Request) string {
	actor, _ := r.Context().Value(keyUser).(string)
	return actor
}
