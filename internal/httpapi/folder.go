package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"noteroot/internal/git"
	"noteroot/internal/notebooks"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// createFolder handles POST /n/{slug}/folder — create an empty folder.
//
// Request body:
//
//	{"path": "projects/2026"}
//
// Response (201):
//
//	{"path": "projects/2026"}
func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	if !s.lockGuard(w, r, r.PathValue("slug"), req.Path) {
		return
	}

	v := vault.Open(nb.Layout.Vault())
	if err := v.Mkdir(req.Path); err != nil {
		switch {
		case errors.Is(err, vault.ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "path already exists"})
		default:
			writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		}
		return
	}

	s.markGitDirty(nb, git.Op{Action: "create", Path: req.Path, Actor: actorFrom(r)})
	s.emit(r, nb, "folder.create", req.Path, nil)
	writeJSON(w, http.StatusCreated, map[string]string{"path": req.Path})
}

// moveFolder handles PATCH /n/{slug}/folder — rename or move a folder
// and every note under it in one atomic filesystem rename.
//
// Request body:
//
//	{"from": "old/dir", "to": "new/dir"}
//
// Response (200):
//
//	{"from": "old/dir", "to": "new/dir"}
func (s *Server) moveFolder(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	if req.From == "" || req.To == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to are required"})
		return
	}
	slug := r.PathValue("slug")
	if !s.lockGuard(w, r, slug, req.From) || !s.lockGuard(w, r, slug, req.To) {
		return
	}

	v := vault.Open(nb.Layout.Vault())
	if err := v.RenameDir(req.From, req.To); err != nil {
		switch {
		case errors.Is(err, vault.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "source not found"})
		case errors.Is(err, vault.ErrNotFile):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source is not a folder"})
		case errors.Is(err, vault.ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "destination already exists"})
		case errors.Is(err, vault.ErrEscapes):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path escapes vault"})
		default:
			writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		}
		return
	}

	s.renameIdentityPrefix(r, nb, req.From, req.To)
	s.reindex(r.Context(), nb, v)
	s.markGitDirty(nb, git.Op{Action: "move", Path: req.To, Actor: actorFrom(r)})
	s.emit(r, nb, "folder.rename", req.To, map[string]any{"from": req.From})

	writeJSON(w, http.StatusOK, map[string]any{"from": req.From, "to": req.To})
}

// deleteFolder handles DELETE /n/{slug}/folder?path= — moves every note
// under the folder to trash (each restorable individually), then
// removes the folder itself. No If-Match: like directory trash, the
// folder moves as one unit.
//
// Response (200):
//
//	{"path": "dir", "trashed": ["dir/a.md", "dir/b.md"]}
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	if !s.lockGuard(w, r, r.PathValue("slug"), rel) {
		return
	}

	v := vault.Open(nb.Layout.Vault())
	isDir, err := v.IsDir(rel)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such folder"})
		} else {
			writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		}
		return
	}
	if !isDir {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a folder"})
		return
	}

	actor := actorFrom(r)
	tr := vault.OpenTrash(v, nb.Layout.Trash())
	entries, err := tr.Put(rel, vault.WriteOpts{Actor: actor})
	if err != nil && !errors.Is(err, vault.ErrNotFound) {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}

	// Remove the folder itself: empty after Put; if only ignored files
	// remain it blocks removal and the leftover is harmless (Tree skips
	// non-note files either way). Notes are already trashed — never fail
	// the request over the rmdir.
	if err := v.RemoveDir(rel); err != nil && !errors.Is(err, vault.ErrConflict) && !errors.Is(err, vault.ErrNotFound) {
		s.log.Warn("folder delete: remove failed", "notebook", nb.Manifest.Slug, "path", rel, "err", err)
	}

	s.reindex(r.Context(), nb, v)
	s.markGitDirty(nb, git.Op{Action: "delete", Path: rel, Actor: actor})
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	s.emit(r, nb, "folder.delete", rel, map[string]any{"trashed": names})
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "trashed": names})
}

// renameIdentityPrefix rewrites note_identity rows under a moved folder.
// Failure is logged but never surfaced — state is rebuildable.
func (s *Server) renameIdentityPrefix(r *http.Request, nb *notebooks.Notebook, fromDir, toDir string) {
	db, _, err := state.Open(r.Context(), nb.Layout.StateDB())
	if err != nil {
		s.log.Warn("folder move: state open failed", "notebook", nb.Manifest.Slug, "err", err)
		return
	}
	defer func() { _ = db.Close() }()
	if err := db.RenameIdentityPrefix(r.Context(), fromDir, toDir); err != nil {
		s.log.Warn("folder move: identity prefix update failed",
			"from", fromDir, "to", toDir, "err", err)
	}
}
