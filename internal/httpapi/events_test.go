package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/platform"
)

func doReq(t *testing.T, srv *Server, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// TestEmit_mutationsReachAuditAndBus is the phase-1 substrate contract:
// every successful mutation lands in platform.db's audit trail and on
// the notebook's event bus, and dry runs land nowhere.
func TestEmit_mutationsReachAuditAndBus(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("fixture notebook missing")
	}

	type rec struct{ typ, path string }
	delivered := make(chan rec, 16)
	srv.plat.Bus("work").Subscribe(platform.Subscription{
		ID: "test-sub",
		Handler: func(_ context.Context, ev platform.Event) {
			delivered <- rec{ev.Type, ev.Path}
		},
	})

	// 1. PUT a.md → note.write
	put := doReq(t, srv, http.MethodPut, notePath("a.md"), "---\ntitle: A\n---\n\nhi\n", nil)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, body=%s", put.Code, put.Body)
	}

	// 2. PATCH a.md → b.md → note.rename
	patch := doReq(t, srv, http.MethodPatch, "/n/work/note",
		`{"from":"a.md","to":"b.md"}`, map[string]string{"Content-Type": "application/json"})
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", patch.Code, patch.Body)
	}

	// 3. DELETE b.md → note.delete (If-Match required)
	del := doReq(t, srv, http.MethodDelete, notePath("b.md"), "", map[string]string{"If-Match": "*"})
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body=%s", del.Code, del.Body)
	}

	// Deliver synchronously.
	srv.plat.Bus("work").Flush()

	want := []rec{
		{"note.write", "a.md"},
		{"note.rename", "b.md"},
		{"note.delete", "b.md"},
	}
	for _, w := range want {
		select {
		case got := <-delivered:
			if got != w {
				t.Errorf("event = {%s %s}, want {%s %s}", got.typ, got.path, w.typ, w.path)
			}
		default:
			t.Fatalf("missing event {%s %s}", w.typ, w.path)
		}
	}

	// Audit trail mirrors the events (newest first).
	db, _, err := platform.Open(context.Background(), nb.Layout.PlatformDB())
	if err != nil {
		t.Fatalf("platform open: %v", err)
	}
	defer func() { _ = db.Close() }()
	entries, err := db.AuditQuery(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("audit len = %d, want 3: %+v", len(entries), entries)
	}
	if entries[0].Action != "note.delete" || entries[1].Action != "note.rename" || entries[2].Action != "note.write" {
		t.Errorf("audit order = %s,%s,%s; want delete,rename,write",
			entries[0].Action, entries[1].Action, entries[2].Action)
	}
	if entries[2].Resource != "work/a.md" {
		t.Errorf("resource = %q, want work/a.md", entries[2].Resource)
	}
}

// TestEmit_dryRunImportEmitsNothing: classification without writes must
// not touch audit or the bus.
func TestEmit_dryRunImportEmitsNothing(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"seed.md": "s"}))
	nb, _ := srv.reg.Get("work")

	var n int
	srv.plat.Bus("work").Subscribe(platform.Subscription{
		ID:      "counter",
		Handler: func(context.Context, platform.Event) { n++ },
	})

	// Build a minimal one-file tar.gz in memory via the export path:
	// export first, then re-import it as dry run.
	export := doReq(t, srv, http.MethodGet, "/n/work/export", "", nil)
	if export.Code != http.StatusOK {
		t.Fatalf("export status = %d", export.Code)
	}
	imp := doReq(t, srv, http.MethodPost, "/n/work/import?dry_run=true",
		export.Body.String(), map[string]string{"Content-Type": "application/gzip"})
	if imp.Code != http.StatusOK {
		t.Fatalf("import status = %d, body=%s", imp.Code, imp.Body)
	}
	var body struct {
		DryRun bool `json:"dryRun"`
	}
	if err := json.NewDecoder(imp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.DryRun {
		t.Fatal("response dryRun = false")
	}

	srv.plat.Bus("work").Flush()
	if n != 0 {
		t.Errorf("bus delivered %d events, want 0", n)
	}
	db, _, err := platform.Open(context.Background(), nb.Layout.PlatformDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	entries, err := db.AuditQuery(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("audit has %d entries, want 0", len(entries))
	}
}
