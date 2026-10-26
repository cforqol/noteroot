package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"noteroot/internal/config"
)

// lockClient drives the content-lock endpoints with a persistent cookie
// jar, so unlock sessions survive across requests within one test.
type lockClient struct {
	t      *testing.T
	srv    *Server
	cookie string
}

func newLockClient(t *testing.T, files map[string]string) *lockClient {
	t.Helper()
	return &lockClient{t: t, srv: newTestServer(t, config.AuthNone, notesFixture(t, files))}
}

func (c *lockClient) do(method, target string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if c.cookie != "" {
		r.Header.Set("Cookie", c.cookie)
	}
	rec := httptest.NewRecorder()
	c.srv.http.Handler.ServeHTTP(rec, r)
	for _, ck := range rec.Result().Cookies() {
		if strings.HasPrefix(ck.Name, "nr_lock_") {
			if ck.MaxAge < 0 || ck.Value == "" {
				c.cookie = ""
			} else {
				c.cookie = ck.Name + "=" + ck.Value
			}
		}
	}
	return rec
}

func (c *lockClient) setLock(path, kind, password string) *httptest.ResponseRecorder {
	body := `{"path":"` + path + `","kind":"` + kind + `","password":"` + password + `"}`
	return c.do(http.MethodPut, "/n/work/contentlock", body, nil)
}

func (c *lockClient) unlock(path, password string) *httptest.ResponseRecorder {
	body := `{"path":"` + path + `","password":"` + password + `"}`
	return c.do(http.MethodPost, "/n/work/contentlock/unlock", body, nil)
}

func (c *lockClient) note(path string) *httptest.ResponseRecorder {
	return c.do(http.MethodGet, "/n/work/note?path="+url.QueryEscape(path), "", nil)
}

func TestContentLock_blocksThenUnlocks(t *testing.T) {
	c := newLockClient(t, map[string]string{"secret.md": "# hi\n", "open.md": "x\n"})

	if rec := c.setLock("secret.md", "note", "opensesame"); rec.Code != http.StatusOK {
		t.Fatalf("set lock status = %d: %s", rec.Code, rec.Body.String())
	}
	// Now locked -> 423.
	if rec := c.note("secret.md"); rec.Code != http.StatusLocked {
		t.Fatalf("locked read status = %d, want 423", rec.Code)
	}
	// Unrelated note still readable.
	if rec := c.note("open.md"); rec.Code != http.StatusOK {
		t.Fatalf("open note status = %d", rec.Code)
	}
	// Wrong password.
	if rec := c.unlock("secret.md", "nope"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", rec.Code)
	}
	// Right password.
	if rec := c.unlock("secret.md", "opensesame"); rec.Code != http.StatusOK {
		t.Fatalf("unlock status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := c.note("secret.md"); rec.Code != http.StatusOK {
		t.Fatalf("post-unlock read status = %d", rec.Code)
	}
}

func TestContentLock_folderCoversDescendants(t *testing.T) {
	c := newLockClient(t, map[string]string{"dir/a.md": "a\n", "dir/sub/b.md": "b\n", "top.md": "t\n"})
	if rec := c.setLock("dir", "folder", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set folder lock = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := c.note("dir/a.md"); rec.Code != http.StatusLocked {
		t.Fatalf("descendant read = %d, want 423", rec.Code)
	}
	if rec := c.note("dir/sub/b.md"); rec.Code != http.StatusLocked {
		t.Fatalf("deep descendant read = %d, want 423", rec.Code)
	}
	if rec := c.note("top.md"); rec.Code != http.StatusOK {
		t.Fatalf("outside read = %d", rec.Code)
	}
	// Unlocking the folder opens its whole subtree.
	if rec := c.unlock("dir", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("folder unlock = %d", rec.Code)
	}
	if rec := c.note("dir/sub/b.md"); rec.Code != http.StatusOK {
		t.Fatalf("post-unlock descendant = %d", rec.Code)
	}
}

func TestContentLock_notebookCoversAll(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "a\n"})
	if rec := c.do(http.MethodPut, "/n/work/contentlock",
		`{"path":"","kind":"notebook","password":"pw12"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("set notebook lock = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := c.note("a.md"); rec.Code != http.StatusLocked {
		t.Fatalf("note under notebook lock = %d", rec.Code)
	}
	// The tree exposes existence but no children.
	rec := c.do(http.MethodGet, "/n/work/tree", "", nil)
	var tree struct {
		Locked   bool `json:"locked"`
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	if !tree.Locked || len(tree.Children) != 0 {
		t.Errorf("tree = %+v", tree)
	}
}

func TestContentLock_treeMarksLockedFolder(t *testing.T) {
	c := newLockClient(t, map[string]string{"dir/a.md": "a\n", "top.md": "t\n"})
	if rec := c.setLock("dir", "folder", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	rec := c.do(http.MethodGet, "/n/work/tree", "", nil)
	var tree struct {
		Children []struct {
			Name     string `json:"name"`
			IsDir    bool   `json:"isDir"`
			Locked   bool   `json:"locked"`
			Children []struct {
				Name string `json:"name"`
			} `json:"children"`
		} `json:"children"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	for i := range tree.Children {
		if tree.Children[i].Name == "dir" {
			if !tree.Children[i].Locked {
				t.Error("locked folder not marked")
			}
			if len(tree.Children[i].Children) != 0 {
				t.Error("locked folder exposed children")
			}
		}
		if tree.Children[i].Name == "top.md" && tree.Children[i].Locked {
			t.Error("unlocked note marked locked")
		}
	}
}

func TestContentLock_searchExcludesLocked(t *testing.T) {
	c := newLockClient(t, map[string]string{"secret.md": "needle here\n", "open.md": "needle there\n"})
	if rec := c.setLock("secret.md", "note", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	rec := c.do(http.MethodGet, "/n/work/search?q=needle", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d", rec.Code)
	}
	var body struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, h := range body.Hits {
		if h.Path == "secret.md" {
			t.Error("locked note leaked into search results")
		}
	}
	found := false
	for _, h := range body.Hits {
		if h.Path == "open.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("unlocked note missing from search: %+v", body.Hits)
	}
}

func TestContentLock_remove(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "a\n"})
	if rec := c.setLock("a.md", "note", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	if rec := c.unlock("a.md", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("unlock = %d", rec.Code)
	}
	if rec := c.do(http.MethodDelete, "/n/work/contentlock?path=a.md&kind=note", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := c.note("a.md"); rec.Code != http.StatusOK {
		t.Fatalf("read after remove = %d", rec.Code)
	}
}

func TestContentLock_status(t *testing.T) {
	c := newLockClient(t, map[string]string{"dir/a.md": "a\n"})
	if rec := c.setLock("dir", "folder", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	rec := c.do(http.MethodGet, "/n/work/contentlock?path=dir/a.md", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Locked   bool     `json:"locked"`
		Unlocked bool     `json:"unlocked"`
		Locks    []string `json:"locks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Locked || body.Unlocked || len(body.Locks) != 1 || body.Locks[0] != "dir/" {
		t.Errorf("status = %+v", body)
	}
	if rec := c.unlock("dir", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("unlock = %d", rec.Code)
	}
	rec = c.do(http.MethodGet, "/n/work/contentlock?path=dir/a.md", "", nil)
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !body.Locked || !body.Unlocked {
		t.Errorf("after unlock status = %+v", body)
	}
}

func TestContentLock_shortPasswordRejected(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "a\n"})
	if rec := c.setLock("a.md", "note", "no"); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password status = %d, want 400", rec.Code)
	}
}

func TestContentLock_setRequiresUnlocked(t *testing.T) {
	c := newLockClient(t, map[string]string{"dir/a.md": "a\n"})
	if rec := c.setLock("dir", "folder", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	// A fresh client (no session) cannot re-key the locked folder.
	c2 := &lockClient{t: t, srv: c.srv}
	if rec := c2.setLock("dir/a.md", "note", "other1"); rec.Code != http.StatusLocked {
		t.Fatalf("rekey while locked status = %d, want 423", rec.Code)
	}
}

func TestContentLock_setMissingTarget(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "a\n"})
	if rec := c.setLock("ghost.md", "note", "pw12"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing target status = %d, want 404", rec.Code)
	}
}

func TestContentLock_rawBlocked(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "secret\n"})
	if rec := c.setLock("a.md", "note", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/n/work/raw?path=a.md", "", nil); rec.Code != http.StatusLocked {
		t.Fatalf("raw read status = %d, want 423", rec.Code)
	}
}

func TestContentLock_writeBlocked(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "orig\n"})
	if rec := c.setLock("a.md", "note", "pw12"); rec.Code != http.StatusOK {
		t.Fatalf("set = %d", rec.Code)
	}
	if rec := c.do(http.MethodPut, "/n/work/note?path=a.md", "new\n", nil); rec.Code != http.StatusLocked {
		t.Fatalf("write to locked note = %d, want 423", rec.Code)
	}
}

func TestContentLock_badKind(t *testing.T) {
	c := newLockClient(t, map[string]string{"a.md": "a\n"})
	if rec := c.setLock("a.md", "bogus", "pw12"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d, want 400", rec.Code)
	}
}
