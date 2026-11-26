package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"noteroot/internal/folderprefs"
)

var folderPrefColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const maxFolderPrefEmojiRunes = 16

// listFolderPrefs handles GET /n/{slug}/folder-prefs — every folder's
// server-side emoji/color, keyed by folder path.
//
// Response (200):
//
//	{"prefs": {"projects": {"emoji": "💼", "color": "#aabbcc"}}}
func (s *Server) listFolderPrefs(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	prefs, err := folderprefs.Load(nb.Layout.FolderPrefs())
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prefs": prefs})
}

// setFolderPref handles PUT /n/{slug}/folder-prefs — set, or (with
// both fields empty) clear, one folder's emoji and name color.
//
// Request body:
//
//	{"path": "projects", "emoji": "💼", "color": "#aabbcc"}
//
// Response (200):
//
//	{"path": "projects", "emoji": "💼", "color": "#aabbcc"}
func (s *Server) setFolderPref(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		Path  string `json:"path"`
		Emoji string `json:"emoji"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}

	pref := cleanFolderPref(req.Emoji, req.Color)
	if _, err := folderprefs.Set(nb.Layout.FolderPrefs(), req.Path, pref); err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": req.Path, "emoji": pref.Emoji, "color": pref.Color})
}

// cleanFolderPref mirrors the browser-side validation the feature had
// before it moved server-side: an emoji is trimmed and capped at 16
// runes, a color must be a 6-digit hex code or it is dropped.
func cleanFolderPref(emoji, color string) folderprefs.Pref {
	e := strings.TrimSpace(emoji)
	if r := []rune(e); len(r) > maxFolderPrefEmojiRunes {
		e = string(r[:maxFolderPrefEmojiRunes])
	}
	c := strings.ToLower(color)
	if !folderPrefColorRe.MatchString(c) {
		c = ""
	}
	return folderprefs.Pref{Emoji: e, Color: c}
}
