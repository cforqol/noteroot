package httpapi

import (
	"errors"
	"net/http"

	"noteroot/internal/git"
	"noteroot/internal/index"
	"noteroot/internal/notebooks"
	"noteroot/internal/vault"
)

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	if r.Header.Get("If-Match") == "" {
		writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "If-Match required"})
		return
	}
	v := vault.Open(nb.Layout.Vault())
	base, currentSha, ok := s.preconditions(w, v, r, rel)
	if !ok {
		return
	}

	actor, _ := r.Context().Value(keyUser).(string)
	tr := vault.OpenTrash(v, nb.Layout.Trash())
	entries, err := tr.Put(rel, vault.WriteOpts{BaseSha: base, Actor: actor})
	if err != nil {
		if errors.Is(err, vault.ErrConflict) {
			writeJSON(w, http.StatusPreconditionFailed, map[string]string{
				"error": "precondition failed", "currentSha": currentSha,
			})
			return
		}
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}

	s.reindex(r, nb, v)
	s.markGitDirty(nb, git.Op{Action: "delete", Path: rel, Actor: actor})
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	s.emit(r, nb, "note.delete", rel, map[string]any{"trashed": names})
	writeJSON(w, http.StatusOK, map[string]any{"trashed": names})
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	entries, err := vault.OpenTrash(vault.Open(nb.Layout.Vault()), nb.Layout.Trash()).List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "trash unreadable"})
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"name": e.Name, "origPath": e.OrigPath, "actor": e.Actor,
			"sha": e.Sha, "size": e.Size, "deletedAt": e.DeletedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing name"})
		return
	}
	v := vault.Open(nb.Layout.Vault())
	rel, err := vault.OpenTrash(v, nb.Layout.Trash()).Restore(name)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such trash entry"})
		case errors.Is(err, vault.ErrOccupied):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "target occupied"})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}
	s.reindex(r, nb, v)
	actor, _ := r.Context().Value(keyUser).(string)
	s.markGitDirty(nb, git.Op{Action: "restore", Path: rel, Actor: actor})
	s.emit(r, nb, "note.restore", rel, nil)
	writeJSON(w, http.StatusOK, map[string]string{"path": rel})
}

// reindex refreshes search after a mutation. Index failure only degrades
// search freshness — the mutation already succeeded.
func (s *Server) reindex(r *http.Request, nb *notebooks.Notebook, v *vault.Vault) {
	idx, _, err := index.Open(r.Context(), nb.Layout.IndexDB(), nb.Layout.StateDB())
	if err != nil {
		s.log.Warn("index open failed", "notebook", nb.Manifest.Slug, "err", err)
		return
	}
	defer func() {
		if err := idx.Close(); err != nil {
			s.log.Warn("index close failed", "notebook", nb.Manifest.Slug, "err", err)
		}
	}()
	if _, err := idx.Update(r.Context(), v); err != nil {
		s.log.Warn("index update failed", "notebook", nb.Manifest.Slug, "err", err)
	}
}
