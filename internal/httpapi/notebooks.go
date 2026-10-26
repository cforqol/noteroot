package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"noteroot/internal/git"
	"noteroot/internal/notebooks"
)

// Notebook CRUD. Create/Rename/Delete are session-surface only (no
// bearer scope exists for them) and go through mutating(), so they
// carry CSRF protection. Delete archives rather than erases: the
// directory moves under notebooks/.archive/, which Registry.Open skips.

func (s *Server) createNotebook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	nb, err := s.reg.Create(body.Slug, body.Name)
	if err != nil {
		s.notebookError(w, "create", body.Slug, err)
		return
	}
	slug := nb.Manifest.Slug
	// Best-effort: a vault without a repo only loses autocommit history,
	// never writes. The Flusher degrades quietly either way.
	if err := git.Init(nb.Layout.Vault()); err != nil {
		s.log.Warn("git init failed", "notebook", slug, "err", err)
	}
	if err := s.plug.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("plugin load failed", "notebook", slug, "err", err)
	}
	if err := s.integ.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("integration load failed", "notebook", slug, "err", err)
	}
	writeJSON(w, http.StatusCreated, notebookJSON(nb))
}

func (s *Server) renameNotebook(w http.ResponseWriter, r *http.Request) {
	oldSlug := r.PathValue("slug")
	var body struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if _, ok := s.reg.Get(oldSlug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	// Flush and drop the old runtime while its directory still exists,
	// so pending autocommits land in the right place.
	s.dropNotebookRuntime(oldSlug)
	nb, err := s.reg.Rename(oldSlug, body.Slug)
	if err != nil {
		// The notebook is still on disk under the old slug (rename
		// rolled back or never happened); restore the runtime.
		if old, ok := s.reg.Get(oldSlug); ok {
			s.ensureRuntime(old)
		}
		s.notebookError(w, "rename", oldSlug, err)
		return
	}
	if nb.Manifest.Slug != oldSlug {
		s.ensureRuntime(nb)
	}
	writeJSON(w, http.StatusOK, notebookJSON(nb))
}

func (s *Server) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	s.dropNotebookRuntime(slug)
	archived, err := s.reg.Delete(slug)
	if err != nil {
		if nb0, ok0 := s.reg.Get(slug); ok0 {
			s.ensureRuntime(nb0)
		}
		s.notebookError(w, "delete", slug, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"deleted":  slug,
		"archived": archived,
	})
}

// dropNotebookRuntime closes everything the server holds for a
// notebook that is about to be renamed or deleted: the git flusher
// (after flushing) and the plugin/integration runtimes and bus.
func (s *Server) dropNotebookRuntime(slug string) {
	s.gitMu.Lock()
	if f := s.git[slug]; f != nil {
		if err := f.Close(); err != nil {
			s.log.Warn("git flush failed", "notebook", slug, "err", err)
		}
		delete(s.git, slug)
	}
	s.gitMu.Unlock()
	s.plug.Remove(slug)
	s.integ.Remove(slug)
	s.plat.Drop(slug)
}

// ensureRuntime loads extensions for a freshly created or renamed
// notebook. Failures degrade; they never fail the HTTP call.
func (s *Server) ensureRuntime(nb *notebooks.Notebook) {
	slug := nb.Manifest.Slug
	if err := s.plug.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("plugin load failed", "notebook", slug, "err", err)
	}
	if err := s.integ.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("integration load failed", "notebook", slug, "err", err)
	}
}

func (s *Server) notebookError(w http.ResponseWriter, op, slug string, err error) {
	switch {
	case errors.Is(err, notebooks.ErrBadSlug):
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "slug must be lowercase letters, digits and dashes"})
	case errors.Is(err, notebooks.ErrSlugExists):
		writeJSON(w, http.StatusConflict,
			map[string]string{"error": "a notebook with that slug already exists"})
	case errors.Is(err, notebooks.ErrNoNotebook):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
	default:
		s.log.Error("notebook "+op+" failed", "notebook", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "could not " + op + " the notebook"})
	}
}

func notebookJSON(nb *notebooks.Notebook) map[string]string {
	return map[string]string{
		"slug":    nb.Manifest.Slug,
		"name":    nb.Manifest.Name,
		"profile": nb.Manifest.Profile,
	}
}
