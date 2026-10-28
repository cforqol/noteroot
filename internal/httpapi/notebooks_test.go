package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func notebookReq(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func listNotebookSlugs(t *testing.T, srv *Server) []string {
	t.Helper()
	rec := notebookReq(t, srv, http.MethodGet, "/api/v1/notebooks", "")
	var resp struct {
		Notebooks []struct {
			Slug string `json:"slug"`
		} `json:"notebooks"`
	}
	decodeJSON(t, rec, &resp)
	out := make([]string, 0, len(resp.Notebooks))
	for _, nb := range resp.Notebooks {
		out = append(out, nb.Slug)
	}
	return out
}

func TestCreateNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"welcome.md": "hi"}))

	rec := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"fresh","name":"Fresh"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Profile string `json:"profile"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Slug != "fresh" || resp.Name != "Fresh" || resp.Profile != "plaintext" {
		t.Errorf("response = %+v", resp)
	}

	// The notebook is live: listed, and its tree answers.
	found := false
	for _, s := range listNotebookSlugs(t, srv) {
		if s == "fresh" {
			found = true
		}
	}
	if !found {
		t.Error("fresh notebook missing from the list")
	}
	if rec := notebookReq(t, srv, http.MethodGet, "/n/fresh/tree", ""); rec.Code != http.StatusOK {
		t.Errorf("tree of new notebook = %d", rec.Code)
	}

	// The vault got a git repo (best-effort; git is installed where tests run).
	nb, ok := srv.reg.Get("fresh")
	if !ok {
		t.Fatal("fresh missing from registry")
	}
	if _, err := os.Stat(filepath.Join(nb.Layout.Vault(), ".git")); err != nil {
		t.Errorf("vault git repo missing: %v", err)
	}

	cases := []struct {
		name, body string
		want       int
	}{
		{"duplicate", `{"slug":"work"}`, http.StatusConflict},
		{"duplicate on disk", `{"slug":"fresh"}`, http.StatusConflict},
		{"bad slug", `{"slug":"Bad_Slug"}`, http.StatusBadRequest},
		{"empty slug", `{"slug":""}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", c.body); rec.Code != c.want {
			t.Errorf("%s = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestRenameNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"welcome.md": "hi"}))

	rec := notebookReq(t, srv, http.MethodPatch, "/api/v1/notebooks/work", `{"slug":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Slug != "renamed" {
		t.Errorf("slug = %q, want renamed", resp.Slug)
	}

	// The old slug is gone, the new one serves the notes.
	if rec := notebookReq(t, srv, http.MethodGet, "/n/work/note?path=welcome.md", ""); rec.Code != http.StatusNotFound {
		t.Errorf("old slug still answers = %d", rec.Code)
	}
	if rec := notebookReq(t, srv, http.MethodGet, "/n/renamed/note?path=welcome.md", ""); rec.Code != http.StatusOK {
		t.Errorf("new slug note = %d", rec.Code)
	}
	nb, ok := srv.reg.Get("renamed")
	if !ok {
		t.Fatal("renamed missing from registry")
	}
	if _, err := os.Stat(nb.Layout.Vault()); err != nil {
		t.Errorf("renamed vault missing: %v", err)
	}
	if _, err := os.Stat(nb.Layout.Root()); err != nil {
		t.Errorf("renamed dir missing: %v", err)
	}

	// A second notebook gives the collision case something to hit.
	if rec := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"other"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create other = %d", rec.Code)
	}
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"missing", "/api/v1/notebooks/ghost", `{"slug":"x"}`, http.StatusNotFound},
		{"bad slug", "/api/v1/notebooks/renamed", `{"slug":"Bad_Slug"}`, http.StatusBadRequest},
		{"collision", "/api/v1/notebooks/renamed", `{"slug":"other"}`, http.StatusConflict},
		{"bad json", "/api/v1/notebooks/renamed", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := notebookReq(t, srv, http.MethodPatch, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}

	// A failed rename leaves the notebook where it was.
	if rec := notebookReq(t, srv, http.MethodGet, "/n/renamed/tree", ""); rec.Code != http.StatusOK {
		t.Error("notebook unusable after a failed rename")
	}
}

func TestDeleteNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"welcome.md": "hi"}))

	rec := notebookReq(t, srv, http.MethodDelete, "/api/v1/notebooks/work", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted  string `json:"deleted"`
		Archived string `json:"archived"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Deleted != "work" {
		t.Errorf("deleted = %q, want work", resp.Deleted)
	}
	fi, err := os.Stat(resp.Archived)
	if err != nil || !fi.IsDir() {
		t.Fatalf("archive path %q not a directory: %v", resp.Archived, err)
	}
	if _, err := os.Stat(filepath.Join(resp.Archived, notebooksVaultDir, "welcome.md")); err != nil {
		t.Errorf("note missing from archive: %v", err)
	}

	// Gone from the API surface.
	if rec := notebookReq(t, srv, http.MethodGet, "/n/work/tree", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted notebook still serves = %d", rec.Code)
	}
	if slugs := listNotebookSlugs(t, srv); len(slugs) != 0 {
		t.Errorf("slugs = %v, want empty", slugs)
	}

	if rec := notebookReq(t, srv, http.MethodDelete, "/api/v1/notebooks/ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

// notebooksVaultDir mirrors notebooks.VaultDir without importing the
// package just for a string in a test.
const notebooksVaultDir = "vault"

func TestNotebookCSRF(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	// A form content type is rejected; JSON and empty pass.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notebooks",
		strings.NewReader("slug=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("form post = %d, want 403", rec.Code)
	}

	// No body at all on DELETE still goes through.
	if rec := notebookReq(t, srv, http.MethodDelete, "/api/v1/notebooks/work", ""); rec.Code != http.StatusOK {
		t.Errorf("delete without content-type = %d", rec.Code)
	}
}

func TestNotebookErrorShapes(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"Bad_Slug"}`)
	var errResp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, rec, &errResp)
	if errResp.Error == "" {
		t.Error("400 response has no error message")
	}

	rec = notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"work"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d", rec.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Errorf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if msg, _ := raw["error"].(string); msg == "" {
		t.Error("409 response has no error message")
	}
}
