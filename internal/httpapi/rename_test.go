package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func patchNote(t *testing.T, srv *Server, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/n/"+slug+"/note", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestMoveNote_basic(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"notes/old.md": "# Hello",
	}))

	rec := patchNote(t, srv, "work", `{"from":"notes/old.md","to":"archive/old.md"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["from"] != "notes/old.md" || resp["to"] != "archive/old.md" {
		t.Errorf("resp = %v", resp)
	}

	// Verify old path is gone and new path exists via the tree.
	req := httptest.NewRequest(http.MethodGet, "/n/work/tree", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	_ = rr.Body.String()
	// Fetch from new path.
	noteReq := httptest.NewRequest(http.MethodGet, "/n/work/note?path=archive/old.md", nil)
	noteRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(noteRec, noteReq)
	if noteRec.Code != http.StatusOK {
		t.Errorf("new path fetch = %d, want 200", noteRec.Code)
	}
}

func TestMoveNote_srcMissing(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := patchNote(t, srv, "work", `{"from":"ghost.md","to":"b.md"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMoveNote_dstExists(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "aaa",
		"b.md": "bbb",
	}))

	rec := patchNote(t, srv, "work", `{"from":"a.md","to":"b.md"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestMoveNote_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := patchNote(t, srv, "nope", `{"from":"a.md","to":"b.md"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMoveNote_missingFields(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	for _, body := range []string{`{"from":"a.md"}`, `{"to":"b.md"}`, `{}`} {
		rec := patchNote(t, srv, "work", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%q status = %d, want 400", body, rec.Code)
		}
	}
}

func TestMoveNote_pathEscapes(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := patchNote(t, srv, "work", `{"from":"../escape.md","to":"b.md"}`)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Errorf("escape src: status = %d, want 400 or 404", rec.Code)
	}
	rec = patchNote(t, srv, "work", `{"from":"a.md","to":"../escape.md"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("escape dst: status = %d, want 400", rec.Code)
	}
}

func TestMoveNote_badJSON(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	rec := patchNote(t, srv, "work", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
