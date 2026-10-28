package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func folderReq(t *testing.T, srv *Server, method, slug, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/n/"+slug+"/folder"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

type treeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"isDir"`
	Children []treeNode `json:"children"`
}

func findNode(n treeNode, p string) *treeNode {
	if n.Path == p {
		return &n
	}
	for _, c := range n.Children {
		if f := findNode(c, p); f != nil {
			return f
		}
	}
	return nil
}

func fetchTree(t *testing.T, srv *Server) treeNode {
	t.Helper()
	rec := get(t, srv, "/n/work/tree", nil)
	var root treeNode
	decodeJSON(t, rec, &root)
	return root
}

func TestCreateFolder(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":"projects/2026"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	n := findNode(fetchTree(t, srv), "projects/2026")
	if n == nil || !n.IsDir {
		t.Fatalf("empty folder missing from tree: %+v", n)
	}

	if rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":"projects/2026"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":"a.md"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("note-like name = %d, want 400", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty = %d, want 400", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":".secret"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("hidden = %d, want 400", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "work", "", `{"path":"../x"}`); rec.Code >= 500 || rec.Code < 400 {
		t.Errorf("escape = %d, want 4xx", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "work", "", `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d, want 400", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodPost, "nope", "", `{"path":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook = %d, want 404", rec.Code)
	}
}

func TestMoveFolder(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"old/one.md":     "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV\n---\nbody\n",
		"old/sub/two.md": "two",
		"taken/x.md":     "x",
		"loose.md":       "l",
	}))

	rec := folderReq(t, srv, http.MethodPatch, "work", "", `{"from":"old","to":"archive/old"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	tree := fetchTree(t, srv)
	if findNode(tree, "archive/old/sub/two.md") == nil || findNode(tree, "old") != nil {
		t.Errorf("tree not updated: %+v", tree)
	}
	if rec := get(t, srv, notePath("archive/old/one.md"), nil); rec.Code != http.StatusOK {
		t.Errorf("moved note fetch = %d", rec.Code)
	}

	cases := []struct {
		name, body string
		want       int
	}{
		{"missing src", `{"from":"ghost","to":"g"}`, http.StatusNotFound},
		{"dst exists", `{"from":"archive","to":"taken"}`, http.StatusConflict},
		{"into itself", `{"from":"archive","to":"archive/in"}`, http.StatusConflict},
		{"src is note", `{"from":"loose.md","to":"d"}`, http.StatusBadRequest},
		{"missing fields", `{"from":"archive"}`, http.StatusBadRequest},
		{"hidden dst", `{"from":"archive","to":".h"}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := folderReq(t, srv, http.MethodPatch, "work", "", c.body); rec.Code != c.want {
			t.Errorf("%s = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
	if rec := folderReq(t, srv, http.MethodPatch, "nope", "", `{"from":"a","to":"b"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook = %d", rec.Code)
	}
}

func TestDeleteFolder(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"proj/a.md":     "a",
		"proj/sub/b.md": "b",
		"keep.md":       "k",
	}))
	folderReq(t, srv, http.MethodPost, "work", "", `{"path":"empty"}`)

	rec := folderReq(t, srv, http.MethodDelete, "work", "?path="+url.QueryEscape("proj"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path    string   `json:"path"`
		Trashed []string `json:"trashed"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Trashed) != 2 {
		t.Errorf("trashed = %v, want 2 entries", resp.Trashed)
	}
	tree := fetchTree(t, srv)
	if findNode(tree, "proj") != nil {
		t.Error("folder still in tree after delete")
	}
	if findNode(tree, "keep.md") == nil {
		t.Error("unrelated note disturbed")
	}

	// Notes are restorable individually from the trash listing.
	var tr struct {
		Entries []struct {
			Name     string `json:"name"`
			OrigPath string `json:"origPath"`
		} `json:"entries"`
	}
	decodeJSON(t, get(t, srv, "/n/work/trash", nil), &tr)
	if len(tr.Entries) != 2 {
		t.Fatalf("trash entries = %+v", tr.Entries)
	}
	req := httptest.NewRequest(http.MethodPost, "/n/work/trash/restore?name="+url.QueryEscape(tr.Entries[0].Name), nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore = %d, %s", rr.Code, rr.Body.String())
	}
	if findNode(fetchTree(t, srv), tr.Entries[0].OrigPath) == nil {
		t.Error("restored note missing from tree")
	}

	// Empty folder: deleted, nothing trashed.
	rec = folderReq(t, srv, http.MethodDelete, "work", "?path=empty", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty delete = %d, %s", rec.Code, rec.Body.String())
	}
	if findNode(fetchTree(t, srv), "empty") != nil {
		t.Error("empty folder survived delete")
	}

	if rec := folderReq(t, srv, http.MethodDelete, "work", "?path=ghost", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodDelete, "work", "?path=keep.md", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("note via folder endpoint = %d, want 400", rec.Code)
	}
	if findNode(fetchTree(t, srv), "keep.md") == nil {
		t.Error("note was trashed through the folder endpoint")
	}
	if rec := folderReq(t, srv, http.MethodDelete, "work", "", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no path = %d, want 400", rec.Code)
	}
	if rec := folderReq(t, srv, http.MethodDelete, "nope", "?path=x", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook = %d", rec.Code)
	}
}
