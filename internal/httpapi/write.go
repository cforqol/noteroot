package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"noteroot/internal/frontmatter"
	"noteroot/internal/git"
	"noteroot/internal/vault"
)

func (s *Server) putNote(w http.ResponseWriter, r *http.Request) {
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
	v := vault.Open(nb.Layout.Vault())

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vault.MaxReadBytes+1))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file too large"})
		return
	}

	base, currentSha, ok := s.preconditions(w, v, r, rel)
	if !ok {
		return
	}
	force := isForce(r)

	actor, _ := r.Context().Value(keyUser).(string)
	res, err := v.Write(rel, body, vault.WriteOpts{BaseSha: base, Actor: actor})
	external := false
	if errors.Is(err, vault.ErrConflict) && force {
		res, err = v.Write(rel, body, vault.WriteOpts{Actor: actor})
		external = err == nil
	}
	if err != nil {
		if errors.Is(err, vault.ErrConflict) {
			writeJSON(w, http.StatusPreconditionFailed, map[string]string{
				"error":      "precondition failed",
				"currentSha": currentSha,
			})
			return
		}
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}

	s.reindex(r, nb, v)
	s.markGitDirty(nb, git.Op{Action: "write", Path: rel, Actor: actor})

	resp := map[string]any{"path": rel, "sha": res.Sha, "mtime": res.ModTime.Unix()}
	if external {
		resp["externalChange"] = true
	}
	if doc := frontmatter.Parse(body); doc.Err != nil {
		resp["frontmatterError"] = doc.Err.Error()
	}
	if res.Created {
		writeJSON(w, http.StatusCreated, resp)
	} else {
		writeJSON(w, http.StatusOK, resp)
	}
}

// preconditions maps If-Match / If-None-Match to a vault base sha. It reports
// the current sha for 412 responses. ok=false means it already responded.
// Directories count as existing but have no content hash to match.
func (s *Server) preconditions(w http.ResponseWriter, v *vault.Vault, r *http.Request, rel string) (base, currentSha string, ok bool) {
	match := parseETag(r.Header.Get("If-Match"))
	createOnly := r.Header.Get("If-None-Match") == "*"
	if match == "" && !createOnly {
		return "", "", true
	}
	cur, err := v.Read(rel)
	isDir := false
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrNotFound):
			cur = nil
		case errors.Is(err, vault.ErrNotFile):
			cur, isDir = nil, true
		default:
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
			return "", "", false
		}
	}
	if cur != nil {
		currentSha = etagOf(cur)
	}
	switch {
	case createOnly && (cur != nil || isDir):
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "already exists", "currentSha": currentSha,
		})
		return "", "", false
	case match == "*" && cur == nil && !isDir:
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "not found"})
		return "", "", false
	case match != "" && match != "*" && isDir:
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "precondition failed"})
		return "", "", false
	case match != "" && match != "*":
		base = match
	}
	return base, currentSha, true
}

func parseETag(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "W/")
	return strings.Trim(h, `"`)
}

func isForce(r *http.Request) bool {
	switch strings.ToLower(r.URL.Query().Get("force")) {
	case "1", "true":
		return true
	}
	return false
}
