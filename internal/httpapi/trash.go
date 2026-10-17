package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}

	s.reindex(r.Context(), nb, v)
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
	days := nb.Manifest.RetentionDays()
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		m := map[string]any{
			"name": e.Name, "origPath": e.OrigPath, "actor": e.Actor,
			"sha": e.Sha, "size": e.Size, "deletedAt": e.DeletedAt.Unix(),
		}
		// Only dated when the notebook actually sweeps; "never" leaves
		// the field out rather than inventing a deadline.
		if days != notebooks.RetentionNever {
			m["expiresAt"] = e.DeletedAt.AddDate(0, 0, days).Unix()
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":       out,
		"retentionDays": days,
	})
}

// putTrashSettings sets the notebook's trash retention in days, or 0 to
// keep trashed notes until somebody removes them. Shortening retention
// sweeps straight away, so the setting and what the trash holds never
// disagree while the server stays up.
func (s *Server) putTrashSettings(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var body struct {
		RetentionDays *int `json:"retentionDays"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.RetentionDays == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing retentionDays"})
		return
	}
	nb, err := s.reg.SetTrashRetention(slug, *body.RetentionDays)
	if err != nil {
		switch {
		case errors.Is(err, notebooks.ErrBadRetention):
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("retentionDays must be between 0 and %d", notebooks.MaxTrashRetentionDays),
			})
		case errors.Is(err, notebooks.ErrNoNotebook):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		default:
			s.log.Warn("trash retention save failed", "notebook", slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the setting"})
		}
		return
	}
	days := nb.Manifest.RetentionDays()
	purged := s.purgeNotebookTrash(nb)
	s.emit(r, nb, "trash.settings", "", map[string]any{"retentionDays": days, "purged": purged})
	writeJSON(w, http.StatusOK, map[string]any{"retentionDays": days, "purged": purged})
}

// purgeTrashNow runs the retention sweep on demand; ?all=1 empties the
// trash outright.
func (s *Server) purgeTrashNow(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	all := r.URL.Query().Get("all") == "1"
	var (
		n   int
		err error
	)
	if all {
		n, err = vault.OpenTrash(vault.Open(nb.Layout.Vault()), nb.Layout.Trash()).Empty()
		if err != nil {
			s.log.Warn("trash empty failed", "notebook", nb.Manifest.Slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not empty the trash"})
			return
		}
	} else {
		n = s.purgeNotebookTrash(nb)
	}
	s.emit(r, nb, "trash.purge", "", map[string]any{"purged": n, "all": all})
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}

// deleteTrashEntry removes one trashed note for good.
func (s *Server) deleteTrashEntry(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing name"})
		return
	}
	tr := vault.OpenTrash(vault.Open(nb.Layout.Vault()), nb.Layout.Trash())
	if err := tr.Delete(name); err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such trash entry"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emit(r, nb, "trash.delete", "", map[string]any{"name": name})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
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
	s.reindex(r.Context(), nb, v)
	actor, _ := r.Context().Value(keyUser).(string)
	s.markGitDirty(nb, git.Op{Action: "restore", Path: rel, Actor: actor})
	s.emit(r, nb, "note.restore", rel, nil)
	writeJSON(w, http.StatusOK, map[string]string{"path": rel})
}

// reindex refreshes search after a mutation. Index failure only degrades
// search freshness — the mutation already succeeded.
func (s *Server) reindex(ctx context.Context, nb *notebooks.Notebook, v *vault.Vault) {
	idx, _, err := index.Open(ctx, nb.Layout.IndexDB(), nb.Layout.StateDB())
	if err != nil {
		s.log.Warn("index open failed", "notebook", nb.Manifest.Slug, "err", err)
		return
	}
	defer func() {
		if err := idx.Close(); err != nil {
			s.log.Warn("index close failed", "notebook", nb.Manifest.Slug, "err", err)
		}
	}()
	if _, err := idx.Update(ctx, v); err != nil {
		s.log.Warn("index update failed", "notebook", nb.Manifest.Slug, "err", err)
	}
}
