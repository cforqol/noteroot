package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/platform"
)

func issueToken(t *testing.T, srv *Server, body string) tokenCreated {
	t.Helper()
	rec := doReq(t, srv, http.MethodPost, "/n/work/tokens", body,
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /tokens status = %d, body=%s", rec.Code, rec.Body)
	}
	var out tokenCreated
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if out.Token == "" || out.ID == "" {
		t.Fatalf("create response incomplete: %+v", out)
	}
	return out
}

func TestTokens_issueUseRevoke(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "hello"}))

	tok := issueToken(t, srv, `{"name":"ci","scopes":"note:read note:write"}`)
	if len(tok.Scopes) != 2 {
		t.Errorf("scopes = %v", tok.Scopes)
	}

	bearer := map[string]string{"Authorization": "Bearer " + tok.Token}

	// Read works.
	got := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=a.md", "", bearer)
	if got.Code != http.StatusOK {
		t.Fatalf("GET note = %d, body=%s", got.Code, got.Body)
	}

	// Write works and is attributed to the token.
	put := doReq(t, srv, http.MethodPut, "/api/v1/n/work/note?path=a.md",
		"rewritten\n", bearer)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT note = %d, body=%s", put.Code, put.Body)
	}

	// No credential / garbage credential → 401 (same shape for both).
	for _, hdr := range []map[string]string{
		nil,
		{"Authorization": "Bearer wrong"},
		{"Authorization": "Basic dXNlcjpwYXNz"},
		// Forward-auth identity is not a credential on /api/v1.
		{"X-Auth-User": "alice"},
	} {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=a.md", "", hdr)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("headers %v → status %d, want 401", hdr, rec.Code)
		}
	}

	// Revocation kills it.
	rev := doReq(t, srv, http.MethodDelete, "/n/work/tokens?id="+tok.ID, "", nil)
	if rev.Code != http.StatusOK {
		t.Fatalf("revoke = %d, body=%s", rev.Code, rev.Body)
	}
	after := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=a.md", "", bearer)
	if after.Code != http.StatusUnauthorized {
		t.Errorf("revoked token status = %d, want 401", after.Code)
	}
}

func TestTokens_scopeEnforcement(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md":          "top",
		"projects/x.md": "in projects",
	}))

	// Read-only token cannot write.
	ro := issueToken(t, srv, `{"name":"ro","scopes":"note:read"}`)
	h := map[string]string{"Authorization": "Bearer " + ro.Token}
	put := doReq(t, srv, http.MethodPut, "/api/v1/n/work/note?path=a.md", "nope\n", h)
	if put.Code != http.StatusForbidden {
		t.Errorf("read-only PUT = %d, want 403", put.Code)
	}
	get := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=a.md", "", h)
	if get.Code != http.StatusOK {
		t.Errorf("read GET = %d, want 200", get.Code)
	}
	// Search is a different resource: not granted.
	srch := doReq(t, srv, http.MethodGet, "/api/v1/n/work/search?q=top", "", h)
	if srch.Code != http.StatusForbidden {
		t.Errorf("search with note:read = %d, want 403", srch.Code)
	}

	// Qualified token: subtree only.
	sub := issueToken(t, srv, `{"name":"proj","scopes":"note:read:projects/*"}`)
	hs := map[string]string{"Authorization": "Bearer " + sub.Token}
	in := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=projects/x.md", "", hs)
	if in.Code != http.StatusOK {
		t.Errorf("projects/ read = %d, want 200", in.Code)
	}
	out := doReq(t, srv, http.MethodGet, "/api/v1/n/work/note?path=a.md", "", hs)
	if out.Code != http.StatusForbidden {
		t.Errorf("outside-qualifier read = %d, want 403", out.Code)
	}
}

func TestTokens_apiWriteLandsInAuditAsToken(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, _ := srv.reg.Get("work")

	var actor string
	srv.plat.Bus("work").Subscribe(platform.Subscription{
		ID: "audit-check",
		Handler: func(_ context.Context, ev platform.Event) {
			if ev.Type == "note.write" {
				actor = ev.Actor
			}
		},
	})

	tok := issueToken(t, srv, `{"name":"writer","scopes":"*:*"}`)
	put := doReq(t, srv, http.MethodPut, "/api/v1/n/work/note?path=a.md", "via token\n",
		map[string]string{"Authorization": "Bearer " + tok.Token})
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT = %d, body=%s", put.Code, put.Body)
	}
	srv.plat.Bus("work").Flush()
	if actor != "token:writer" {
		t.Errorf("bus actor = %q, want token:writer", actor)
	}

	db, _, err := platform.Open(context.Background(), nb.Layout.PlatformDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	entries, err := db.AuditQuery(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "note.write" && e.Actor == "token:writer" {
			found = true
		}
		if strings.HasPrefix(e.Action, "token.") && e.Actor != "" {
			t.Errorf("token lifecycle actor = %q, action %q", e.Actor, e.Action)
		}
	}
	if !found {
		t.Errorf("no note.write by token:writer in audit: %+v", entries)
	}
}

func TestTokens_listAndValidation(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	issueToken(t, srv, `{"name":"listed","scopes":"note:read"}`)

	// List: metadata only — no hash, no secret, ever.
	list := doReq(t, srv, http.MethodGet, "/n/work/tokens", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d, body=%s", list.Code, list.Body)
	}
	body := list.Body.String()
	if strings.Contains(body, "hash") || strings.Contains(body, "nrk_") {
		t.Errorf("list leaks secret material: %s", body)
	}
	if !strings.Contains(body, "listed") || !strings.Contains(body, "note:read") {
		t.Errorf("list missing expected fields: %s", body)
	}

	// Validation.
	bad := []string{
		`{"name":"x","scopes":"note"}`,
		`{"name":"x","scopes":"note:exec"}`,
		`{"name":"","scopes":"note:read"}`,
		`{"name":"x","scopes":""}`,
		`{not json}`,
	}
	for _, b := range bad {
		rec := doReq(t, srv, http.MethodPost, "/n/work/tokens", b,
			map[string]string{"Content-Type": "application/json"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %s → %d, want 400", b, rec.Code)
		}
	}

	// Unknown-id revoke is 404; id-less revoke is 400.
	if rec := doReq(t, srv, http.MethodDelete, "/n/work/tokens?id=nope", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown = %d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodDelete, "/n/work/tokens", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("revoke without id = %d, want 400", rec.Code)
	}
}
