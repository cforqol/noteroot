package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/frontmatter"
	"noteroot/internal/state"
)

func put(t *testing.T, srv *Server, target string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func putNotePath(p string) string { return "/n/work/note?path=" + p }

// wantMintedRaw asserts the stored note carries a fresh valid id in a
// frontmatter block plus the submitted body — design §3 mint-on-write.
// Returns the stored id.
func wantMintedRaw(t *testing.T, raw, wantBody string) string {
	t.Helper()
	doc := frontmatter.Parse([]byte(raw))
	if !frontmatter.ValidID(doc.ID) {
		t.Fatalf("raw = %q, want a fresh valid id (design §3)", raw)
	}
	if !strings.Contains(raw, wantBody) {
		t.Errorf("raw = %q, want it to contain %q", raw, wantBody)
	}
	return doc.ID
}

func TestPut_create(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	rec := put(t, srv, putNotePath("n.md"), []byte("# New\n"),
		map[string]string{"If-None-Match": "*", "Content-Type": "text/markdown"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Path string `json:"path"`
		Id   string `json:"id"`
		Sha  string `json:"sha"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Path != "n.md" || body.Sha == "" {
		t.Errorf("body = %+v", body)
	}
	raw := get(t, srv, "/n/work/raw?path=n.md", nil)
	if raw.Code != http.StatusOK {
		t.Fatalf("raw = %d %s", raw.Code, raw.Body)
	}
	storedID := wantMintedRaw(t, raw.Body.String(), "# New\n")
	if body.Id != storedID {
		t.Errorf("response id = %q, want stored id %q", body.Id, storedID)
	}
}

func TestPut_updateWithMatchingBase(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	etag := get(t, srv, notePath("n.md"), nil).Header().Get("ETag")
	rec := put(t, srv, putNotePath("n.md"), []byte("v2\n"), map[string]string{"If-Match": etag})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	wantMintedRaw(t, get(t, srv, "/n/work/raw?path=n.md", nil).Body.String(), "v2\n")
}

func TestPut_staleBaseConflict(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	rec := put(t, srv, putNotePath("n.md"), []byte("v2\n"), map[string]string{"If-Match": `"deadbeef"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", rec.Code)
	}
	var body struct {
		CurrentSha string `json:"currentSha"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.CurrentSha == "" {
		t.Error("expected currentSha in 412 response")
	}
	if raw := get(t, srv, "/n/work/raw?path=n.md", nil); raw.Body.String() != "v1\n" {
		t.Errorf("loser write landed: %q", raw.Body)
	}
}

func TestPut_forceWins(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	rec := put(t, srv, putNotePath("n.md")+"&force=1", []byte("v2\n"), map[string]string{"If-Match": `"deadbeef"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		ExternalChange bool `json:"externalChange"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !body.ExternalChange {
		t.Error("expected externalChange=true")
	}
	wantMintedRaw(t, get(t, srv, "/n/work/raw?path=n.md", nil).Body.String(), "v2\n")
}

func TestPut_preconditions(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n", "d/x.md": "x\n"}))
	tests := []struct {
		name, target string
		body         []byte
		hdr          map[string]string
		want         int
	}{
		{"create-only on existing", putNotePath("n.md"), []byte("x"), map[string]string{"If-None-Match": "*"}, http.StatusPreconditionFailed},
		{"update-only on missing", putNotePath("nope.md"), []byte("x"), map[string]string{"If-Match": "*"}, http.StatusPreconditionFailed},
		{"sha on directory", putNotePath("d"), []byte("x"), map[string]string{"If-Match": `"abc"`}, http.StatusPreconditionFailed},
		{"put directory", putNotePath("d"), []byte("x"), nil, http.StatusBadRequest},
		{"missing path", "/n/work/note", []byte("x"), nil, http.StatusBadRequest},
		{"traversal", putNotePath("../x"), []byte("x"), nil, http.StatusBadRequest},
		{"unknown notebook", "/n/nope/note?path=a.md", []byte("x"), nil, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := put(t, srv, tt.target, tt.body, tt.hdr); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestPut_tooLarge(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	big := bytes.Repeat([]byte("x"), 8<<20+1)
	if rec := put(t, srv, putNotePath("big.md"), big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestPut_unconditionalUpdate(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	if rec := put(t, srv, putNotePath("n.md"), []byte("v2\n"), nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestPut_indexReflectsWrite(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	if rec := put(t, srv, putNotePath("n.md"), []byte("uniquewriteword\n"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	rec := get(t, srv, "/n/work/search?q=uniquewriteword", nil)
	var body struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Hits) != 1 {
		t.Fatalf("hits = %+v, want [n.md]", body.Hits)
	}
}

func TestPut_requiresAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, notesFixture(t, map[string]string{"keep.md": "x"}))
	if rec := put(t, srv, putNotePath("n.md"), []byte("x"), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestPut_responseCarriesFrontmatterError(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	rec := put(t, srv, putNotePath("n.md"), []byte("---\n: : [\n---\nbody"), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["frontmatterError"] == nil {
		t.Error("expected frontmatterError, got none")
	}
}

func TestPut_bodyBeyondReaderLimit(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	big := bytes.Repeat([]byte("x"), 8<<20+2)
	if rec := put(t, srv, putNotePath("big.md"), big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestPut_indexFailureStillWrites(t *testing.T) {
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.WriteFile(nb.Layout.App(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := put(t, srv, putNotePath("n.md"), []byte("hello\n"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: index failure must not fail the write", rec.Code)
	}
	wantMintedRaw(t, get(t, srv, "/n/work/raw?path=n.md", nil).Body.String(), "hello\n")
}

func TestPut_preconditionReadError(t *testing.T) {
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
	rec := put(t, srv, putNotePath("n.md"), []byte("v2\n"), map[string]string{"If-Match": `"abc"`})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPut_preservesExistingValidID(t *testing.T) {
	const existing = "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	rec := put(t, srv, putNotePath("n.md"),
		[]byte("---\nid: "+existing+"\ntitle: Keep\n---\ncontent\n"), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Id string `json:"id"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Id != existing {
		t.Errorf("id = %q, want the existing %q (never rewritten)", body.Id, existing)
	}
	raw := get(t, srv, "/n/work/raw?path=n.md", nil).Body.String()
	if !strings.Contains(raw, "id: "+existing) {
		t.Errorf("raw = %q, want preserved id line", raw)
	}
}

// A YAML slip in another key used to blank the parsed id, so every save
// minted a fresh one and overwrote the note's durable identity.
func TestPut_keepsIDWhenYAMLBroken(t *testing.T) {
	const existing = "01ARZ3NDEKTSV4RRFFQ69G5FAV00"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	src := "---\nid: " + existing + "\ntitle: Meeting: notes\n---\ncontent\n"
	for i := 0; i < 2; i++ {
		hdr := map[string]string{}
		if i > 0 {
			hdr["If-Match"] = `"` + etagOf([]byte(src)) + `"`
		}
		rec := put(t, srv, putNotePath("n.md"), []byte(src), hdr)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("save %d: status = %d: %s", i, rec.Code, rec.Body)
		}
		var body struct {
			Id string `json:"id"`
		}
		_ = json.NewDecoder(rec.Body).Decode(&body)
		if body.Id != existing {
			t.Fatalf("save %d: id = %q, want %q kept", i, body.Id, existing)
		}
	}
	if raw := get(t, srv, "/n/work/raw?path=n.md", nil).Body.String(); raw != src {
		t.Errorf("raw = %q, want the note byte-for-byte", raw)
	}
}

func TestPut_reMintsMalformedID(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"keep.md": "x"}))
	rec := put(t, srv, putNotePath("n.md"),
		[]byte("---\nid: nope\n---\ncontent\n"), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Id string `json:"id"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !frontmatter.ValidID(body.Id) {
		t.Fatalf("id = %q, want a fresh valid id", body.Id)
	}
	raw := get(t, srv, "/n/work/raw?path=n.md", nil).Body.String()
	if !strings.Contains(raw, "id: "+body.Id) || strings.Contains(raw, "id: nope") {
		t.Errorf("raw = %q, want malformed id line replaced", raw)
	}
}

func TestPut_recordsIdentityMap(t *testing.T) {
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	srv := newTestServer(t, config.AuthNone, reg)
	rec := put(t, srv, putNotePath("n.md"), []byte("body\n"), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Id string `json:"id"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !frontmatter.ValidID(body.Id) {
		t.Fatalf("id = %q, want valid", body.Id)
	}
	nb, _ := reg.Get("work")
	db, _, err := state.Open(context.Background(), nb.Layout.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	uid, ok, err := db.UIDByPath(context.Background(), "n.md")
	if err != nil || !ok || uid != body.Id {
		t.Errorf("UIDByPath(n.md) = %q,%v,%v; want %q,true,nil", uid, ok, err, body.Id)
	}
}
