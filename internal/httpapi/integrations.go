package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"noteroot/internal/integrations"
)

const maxWebhookBody = 1 << 20 // 1 MiB: note-sized payloads, not file transfers

// listIntegrations shows every discovered outbound webhook config for
// the notebook: loaded, disabled, or problem — secrets are never in
// the file and never in this response (has_secret only).
func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	slug := nb.Manifest.Slug
	if err := s.integ.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("integration ensure failed", "notebook", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	out, err := s.integ.OutList(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outbound": out})
}

// listHooks shows every discovered inbound webhook config.
func (s *Server) listHooks(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	slug := nb.Manifest.Slug
	if err := s.integ.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("hook ensure failed", "notebook", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	in, err := s.integ.InList(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inbound": in})
}

// reloadIntegrations rescans both config directories — the way to
// pick up an edited integration without a restart.
func (s *Server) reloadIntegrations(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	slug := nb.Manifest.Slug
	if err := s.integ.Reload(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
		s.log.Error("integration reload failed", "notebook", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	out, err1 := s.integ.OutList(slug)
	in, err2 := s.integ.InList(slug)
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reloaded": true,
		"outbound": out,
		"inbound":  in,
	})
}

type secretRequest struct {
	Secret string `json:"secret"`
}

// setIntegrationSecret stores the HMAC secret for an outbound config
// (empty secret removes it, leaving the config inert until one is
// set again). Secrets live in platform.db, never in the config file.
func (s *Server) setIntegrationSecret(w http.ResponseWriter, r *http.Request) {
	s.setSecretCommon(w, r, true)
}

// setHookSecret stores the HMAC secret for an inbound hook.
func (s *Server) setHookSecret(w http.ResponseWriter, r *http.Request) {
	s.setSecretCommon(w, r, false)
}

func (s *Server) setSecretCommon(w http.ResponseWriter, r *http.Request, outbound bool) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	var req secretRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	name := r.PathValue("name")
	var err error
	if outbound {
		err = s.integ.SetOutSecret(r.Context(), nb.Manifest.Slug, name, req.Secret)
	} else {
		err = s.integ.SetInSecret(r.Context(), nb.Manifest.Slug, name, req.Secret)
	}
	if err != nil {
		if errors.Is(err, integrations.ErrIntegrationNotFound) || errors.Is(err, integrations.ErrHookNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "secret store failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// handleWebhook is the inbound endpoint external services POST to.
// No forward-auth and no CSRF: the caller is a machine on the public
// side of the proxy, so the HMAC signature over the body is the
// entire authentication. The write runs the real write path with
// hook: attribution and the chain depth continued from the delivery
// header, which is what stops a service that echoes deliveries back
// at us from looping.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	name := r.PathValue("name")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
		return
	}
	sig := r.Header.Get("X-Noteroot-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256") // GitHub-compatible
	}
	depth := 0
	if d := r.Header.Get("X-Noteroot-Depth"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n >= 0 {
			depth = n
		}
	}
	path, err := s.integ.Handle(r.Context(), nb.Manifest.Slug, name, body, sig, depth)
	switch {
	case errors.Is(err, integrations.ErrHookNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown hook"})
	case errors.Is(err, integrations.ErrBadSignature):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
	case errors.Is(err, integrations.ErrDepthExceeded):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "event chain depth exceeded"})
	case err != nil:
		s.log.Error("hook write failed", "notebook", slug, "hook", name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
	}
}
