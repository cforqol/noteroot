package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"noteroot/internal/config"
)

func deleteNote(t *testing.T, srv *Server, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func postEmpty(t *testing.T, srv *Server, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestDelete_roundtrip(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	etag := get(t, srv, notePath("n.md"), nil).Header().Get("ETag")
	rec := deleteNote(t, srv, putNotePath("n.md"), map[string]string{"If-Match": etag})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Trashed []string `json:"trashed"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Trashed) != 1 {
		t.Fatalf("trashed = %+v", body.Trashed)
	}
	if rec := get(t, srv, notePath("n.md"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleted note still readable: %d", rec.Code)
	}
	if rec := get(t, srv, "/n/work/search?q=v1", nil); rec.Code != http.StatusOK {
		t.Fatalf("search status = %d", rec.Code)
	} else {
		var sbody struct {
			Hits []any `json:"hits"`
		}
		_ = json.NewDecoder(rec.Body).Decode(&sbody)
		if len(sbody.Hits) != 0 {
			t.Errorf("deleted note still indexed: %+v", sbody.Hits)
		}
	}
}

func TestDelete_requiresBase(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	rec := deleteNote(t, srv, putNotePath("n.md"), nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Errorf("status = %d, want 428", rec.Code)
	}
	if raw := get(t, srv, "/n/work/raw?path=n.md", nil); raw.Code != http.StatusOK {
		t.Errorf("unconditional delete landed: %d", raw.Code)
	}
}

func TestDelete_staleBase(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	rec := deleteNote(t, srv, putNotePath("n.md"), map[string]string{"If-Match": `"deadbeef"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", rec.Code)
	}
}

func TestDelete_errors(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	etag := get(t, srv, notePath("n.md"), nil).Header().Get("ETag")
	hdr := map[string]string{"If-Match": etag}
	tests := []struct {
		name, target string
		want         int
	}{
		{"missing file", "/n/work/note?path=nope.md", http.StatusNotFound},
		{"missing path", "/n/work/note", http.StatusBadRequest},
		{"unknown notebook", "/n/nope/note?path=a.md", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := deleteNote(t, srv, tt.target, hdr); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestDelete_dirPrefix(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"d/a.md": "a\n", "d/b.md": "b\n", "keep.md": "k\n",
	}))
	rec := deleteNote(t, srv, "/n/work/note?path=d", map[string]string{"If-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Trashed []string `json:"trashed"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Trashed) != 2 {
		t.Fatalf("trashed = %+v, want 2", body.Trashed)
	}
	if raw := get(t, srv, "/n/work/raw?path=keep.md", nil); raw.Code != http.StatusOK {
		t.Errorf("sibling gone: %d", raw.Code)
	}
}

func TestTrash_listAndRestore(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	etag := get(t, srv, notePath("n.md"), nil).Header().Get("ETag")
	if rec := deleteNote(t, srv, putNotePath("n.md"), map[string]string{"If-Match": etag}); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec := get(t, srv, "/n/work/trash", nil)
	var list struct {
		Entries []struct {
			Name     string `json:"name"`
			OrigPath string `json:"origPath"`
		} `json:"entries"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&list)
	if len(list.Entries) != 1 || list.Entries[0].OrigPath != "n.md" {
		t.Fatalf("entries = %+v", list.Entries)
	}
	name := list.Entries[0].Name
	rec = postEmpty(t, srv, "/n/work/trash/restore?name="+name, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", rec.Code, rec.Body)
	}
	if raw := get(t, srv, "/n/work/raw?path=n.md", nil); raw.Body.String() != "v1\n" {
		t.Errorf("raw = %q", raw.Body)
	}
}

func TestTrash_restoreErrors(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	if rec := postEmpty(t, srv, "/n/work/trash/restore", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing name: %d, want 400", rec.Code)
	}
	if rec := postEmpty(t, srv, "/n/work/trash/restore?name=nope.md", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing entry: %d, want 404", rec.Code)
	}
	if rec := get(t, srv, "/n/nope/trash", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook trash: %d, want 404", rec.Code)
	}
}

func TestTrash_restoreOccupied(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	etag := get(t, srv, notePath("n.md"), nil).Header().Get("ETag")
	if rec := deleteNote(t, srv, putNotePath("n.md"), map[string]string{"If-Match": etag}); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec := get(t, srv, "/n/work/trash", nil)
	var list struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&list)
	if rec := put(t, srv, putNotePath("n.md"), []byte("v2\n"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("recreate status = %d", rec.Code)
	}
	if rec := postEmpty(t, srv, "/n/work/trash/restore?name="+list.Entries[0].Name, nil); rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestTrash_requiresAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, notesFixture(t, map[string]string{"n.md": "x"}))
	for path, method := range map[string]string{
		"/n/work/note?path=n.md":       "DELETE",
		"/n/work/trash":                "GET",
		"/n/work/trash/restore?name=x": "POST",
	} {
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", method, path, rec.Code)
		}
	}
}

func TestTrash_listFailure(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.WriteFile(nb.Layout.App(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := get(t, srv, "/n/work/trash", nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestTrash_restoreUnknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := postEmpty(t, srv, "/n/nope/trash/restore?name=x", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestTrash_restoreBadName(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	if rec := postEmpty(t, srv, "/n/work/trash/restore?name=a%2Fb", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_preconditionReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	reg := notesFixture(t, map[string]string{"n.md": "v1\n"})
	nb, _ := reg.Get("work")
	vaultDir := nb.Layout.Vault()
	if err := os.Chmod(vaultDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(vaultDir, 0o755) })
	srv := newTestServer(t, config.AuthNone, reg)
	rec := deleteNote(t, srv, putNotePath("n.md"), map[string]string{"If-Match": `"abc"`})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDelete_dirWithSha(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"d/a.md": "a\n"}))
	rec := deleteNote(t, srv, "/n/work/note?path=d", map[string]string{"If-Match": `"abc"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412: dirs have no content hash", rec.Code)
	}
}
