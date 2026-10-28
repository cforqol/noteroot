package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"noteroot/internal/plugins"
)

// listPlugins shows every discovered plugin for the notebook: loaded,
// disabled, or broken (with the problem recorded).
func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	if err := s.plug.Ensure(nb.Manifest.Slug, nb.Layout, s.plat.Bus(nb.Manifest.Slug)); err != nil {
		s.log.Error("plugin ensure failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	infos, err := s.plug.List(nb.Manifest.Slug)
	if err != nil {
		s.log.Error("plugin list failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// reloadPlugins rescans the plugins directory: the way to pick up an
// edited plugin without restarting the server.
func (s *Server) reloadPlugins(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	if err := s.plug.Reload(nb.Manifest.Slug, nb.Layout, s.plat.Bus(nb.Manifest.Slug)); err != nil {
		s.log.Error("plugin reload failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	infos, err := s.plug.List(nb.Manifest.Slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": true, "plugins": infos})
}

// runPluginCommand dispatches POST body JSON as the command's
// argument. Commands are human-triggered (the route is behind
// forward-auth + CSRF); capabilities inside enforce permissions.
func (s *Server) runPluginCommand(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	args, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	result, err := s.plug.RunCommand(r.Context(), slug,
		r.PathValue("name"), r.PathValue("command"), args)
	switch {
	case errors.Is(err, plugins.ErrPluginNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown plugin"})
	case errors.Is(err, plugins.ErrBreakerOpen):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "plugin circuit open"})
	case errors.Is(err, plugins.ErrNotLoaded):
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "plugin not loaded"})
	case errors.Is(err, plugins.ErrUnknownCommand):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case err != nil:
		// Plugin-side failure (Lua error, timeout, bad args): 500 with
		// the message — visible, isolated, breaker-counted.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

// pluginPanels returns the current ui.panels namespace — panel name to
// JSON spec — for the frontend to render.
func (s *Server) pluginPanels(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	if err := s.plug.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	panels, err := s.plug.Panels(slug)
	if err != nil {
		s.log.Error("plugin panels failed", "notebook", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if panels == nil {
		panels = map[string]json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, panels)
}
