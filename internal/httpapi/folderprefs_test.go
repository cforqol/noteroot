package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func folderPrefsReq(t *testing.T, srv *Server, method, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/n/"+slug+"/folder-prefs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

type folderPref struct {
	Emoji string `json:"emoji"`
	Color string `json:"color"`
}

func getFolderPrefs(t *testing.T, srv *Server, slug string) map[string]folderPref {
	t.Helper()
	var resp struct {
		Prefs map[string]folderPref `json:"prefs"`
	}
	decodeJSON(t, folderPrefsReq(t, srv, http.MethodGet, slug, ""), &resp)
	return resp.Prefs
}

func TestListFolderPrefs_empty(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	prefs := getFolderPrefs(t, srv, "work")
	if len(prefs) != 0 {
		t.Errorf("prefs = %v, want empty", prefs)
	}
}

func TestListFolderPrefs_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	if rec := folderPrefsReq(t, srv, http.MethodGet, "nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSetFolderPref_storesAndClears(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"projects/a.md": "x"}))

	rec := folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"projects","emoji":" 💼 ","color":"#AABBCC"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	prefs := getFolderPrefs(t, srv, "work")
	if prefs["projects"] != (folderPref{Emoji: "💼", Color: "#aabbcc"}) {
		t.Errorf("prefs[projects] = %v", prefs["projects"])
	}

	// Clearing (both fields empty) removes the entry.
	rec = folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"projects"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if prefs := getFolderPrefs(t, srv, "work"); len(prefs) != 0 {
		t.Errorf("prefs after clear = %v, want empty", prefs)
	}
}

// A folder-prefs.json that is actually a directory (e.g. corrupted by
// something outside the app) makes the underlying file ops fail;
// both endpoints surface that as a 5xx rather than panicking.
func TestFolderPrefs_underlyingStoreError(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("notebook work not found")
	}
	if err := os.MkdirAll(nb.Layout.FolderPrefs(), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// statusFor's default for an unrecognized error is 400, the same
	// convention every other handler in this package follows.
	if rec := folderPrefsReq(t, srv, http.MethodGet, "work", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("GET status = %d, want 400", rec.Code)
	}
	if rec := folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"f","emoji":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT status = %d, want 400", rec.Code)
	}
}

func TestSetFolderPref_validation(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	// Invalid color is dropped rather than stored.
	folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"f","emoji":"X","color":"red; background:url(x)"}`)
	if got := getFolderPrefs(t, srv, "work")["f"]; got.Color != "" {
		t.Errorf("color = %q, want dropped", got.Color)
	}

	// Emoji is trimmed and capped at 16 runes.
	folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"f","emoji":"`+strings.Repeat("x", 40)+`"}`)
	if got := getFolderPrefs(t, srv, "work")["f"]; len([]rune(got.Emoji)) != 16 {
		t.Errorf("emoji length = %d, want 16", len([]rune(got.Emoji)))
	}

	if rec := folderPrefsReq(t, srv, http.MethodPut, "work", `{"emoji":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing path = %d, want 400", rec.Code)
	}
	if rec := folderPrefsReq(t, srv, http.MethodPut, "work", `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d, want 400", rec.Code)
	}
	if rec := folderPrefsReq(t, srv, http.MethodPut, "nope", `{"path":"f"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook = %d, want 404", rec.Code)
	}
}

// Integration: a folder's decoration follows a rename/move (and its
// children's), and is forgotten when the folder is deleted — the
// same guarantee the browser-side localStorage version gave, now
// enforced server-side so it survives across browsers too.
func TestFolderPrefs_followRenameAndDelete(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"old/one.md":     "one",
		"old/sub/two.md": "two",
		"keep/x.md":      "x",
	}))
	folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"old","emoji":"O"}`)
	folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"old/sub","emoji":"S"}`)
	folderPrefsReq(t, srv, http.MethodPut, "work", `{"path":"keep","emoji":"K"}`)

	if rec := folderReq(t, srv, http.MethodPatch, "work", "", `{"from":"old","to":"archive/old"}`); rec.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", rec.Code, rec.Body.String())
	}
	prefs := getFolderPrefs(t, srv, "work")
	if prefs["archive/old"].Emoji != "O" || prefs["archive/old/sub"].Emoji != "S" {
		t.Errorf("prefs after move = %v", prefs)
	}
	if _, stale := prefs["old"]; stale {
		t.Errorf("stale 'old' entry survived move: %v", prefs)
	}
	if prefs["keep"].Emoji != "K" {
		t.Errorf("unrelated folder's pref disturbed: %v", prefs)
	}

	if rec := folderReq(t, srv, http.MethodDelete, "work", "?path="+"archive%2Fold", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	prefs = getFolderPrefs(t, srv, "work")
	if _, gone := prefs["archive/old"]; gone {
		t.Errorf("deleted folder's pref survived: %v", prefs)
	}
	if _, gone := prefs["archive/old/sub"]; gone {
		t.Errorf("deleted folder's child pref survived: %v", prefs)
	}
	if prefs["keep"].Emoji != "K" {
		t.Errorf("unrelated folder's pref disturbed by delete: %v", prefs)
	}
}
