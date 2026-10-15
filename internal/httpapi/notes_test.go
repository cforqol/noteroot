package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
	"noteroot/internal/vault"
)

// notesFixture builds a registry with one notebook "work" whose vault
// contains the given files (vault-relative path -> content).
func notesFixture(t *testing.T, files map[string]string) *notebooks.Registry {
	t.Helper()
	dataDir := t.TempDir()
	nbDir := filepath.Join(dataDir, "notebooks", "work")
	writeTestManifest(t, nbDir, "work")
	for rel, content := range files {
		p := filepath.Join(nbDir, notebooks.VaultDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, problems := notebooks.Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	return reg
}

func get(t *testing.T, srv *Server, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func notePath(p string) string { return "/n/work/note?path=" + url.QueryEscape(p) }

func TestTree_endpoint(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x", "d/b.md": "y"}))
	rec := get(t, srv, "/n/work/tree", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Children []struct{ Name string } `json:"children"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Children) != 2 || body.Children[0].Name != "d" {
		t.Errorf("children = %+v", body.Children)
	}
}

func TestTree_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := get(t, srv, "/n/nope/tree", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestNote_rendersAndParsesFrontmatter(t *testing.T) {
	src := "---\ntitle: Hi\ntags: [x, y]\n---\n# Heading\n"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": src}))
	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Title string   `json:"title"`
		Tags  []string `json:"tags"`
		HTML  string   `json:"html"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Title != "Hi" || len(body.Tags) != 2 || !strings.Contains(body.HTML, "<h1") {
		t.Errorf("body = %+v", body)
	}
}

func TestNote_malformedFrontmatterStillOpens(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "---\n: : [\n---\nbody text"}))
	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["frontmatterError"] == nil {
		t.Error("expected frontmatterError")
	}
	if !strings.Contains(body["html"].(string), "body text") {
		t.Errorf("html = %v", body["html"])
	}
}

func TestNote_sanitizesHTML(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"x.md": "<script>alert(1)</script>ok"}))
	rec := get(t, srv, notePath("x.md"), nil)
	if strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("unsanitized script in response: %s", rec.Body)
	}
}

func TestNote_renderFailureFallsBackToEscapedPre(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "hi <b>there</b>"}))
	orig := renderNote
	renderNote = func([]byte) ([]byte, error) { return nil, errors.New("render exploded") }
	t.Cleanup(func() { renderNote = orig })

	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: render failure must not block the note", rec.Code)
	}
	var body struct {
		HTML        string `json:"html"`
		RenderError string `json:"renderError"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RenderError == "" {
		t.Error("expected renderError in response")
	}
	if !strings.HasPrefix(body.HTML, "<pre>") || strings.Contains(body.HTML, "<b>") {
		t.Errorf("html = %q, want escaped <pre> fallback", body.HTML)
	}
}

func TestNote_errorStatuses(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"d/n.md": "x"}))
	tests := []struct {
		name, target string
		want         int
	}{
		{"missing file", notePath("nope.md"), http.StatusNotFound},
		{"directory", notePath("d"), http.StatusNotFound},
		{"traversal", notePath("../../etc/passwd"), http.StatusBadRequest},
		{"absolute", notePath("/etc/passwd"), http.StatusBadRequest},
		{"no path", "/n/work/note", http.StatusBadRequest},
		{"unknown notebook", "/n/nope/note?path=a.md", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := get(t, srv, tt.target, nil); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestNote_tooLarge(t *testing.T) {
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	p := filepath.Join(nb.Layout.Vault(), "big.md")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(vault.MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := newTestServer(t, config.AuthNone, reg)
	rec := get(t, srv, notePath("big.md"), nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestTree_vaultGone_returns500(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.RemoveAll(nb.Layout.Vault()); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	rec := get(t, srv, "/n/work/tree", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestRaw_endpoint(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "# raw"}))
	rec := get(t, srv, "/n/work/raw?path=n.md", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "# raw" {
		t.Errorf("status=%d body=%q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec := get(t, srv, "/n/work/raw?path=../x", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("traversal status = %d, want 400", rec.Code)
	}
}

func TestRaw_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := get(t, srv, "/n/nope/raw?path=a.md", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRaw_tooLarge(t *testing.T) {
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	f, err := os.Create(filepath.Join(nb.Layout.Vault(), "big.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(vault.MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := newTestServer(t, config.AuthNone, reg)
	if rec := get(t, srv, "/n/work/raw?path=big.md", nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestNoteEndpoints_authModes(t *testing.T) {
	reg := notesFixture(t, map[string]string{"n.md": "x"})
	paths := []string{"/n/work/tree", notePath("n.md"), "/n/work/raw?path=n.md"}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			fwd := newTestServer(t, config.AuthForward, reg)
			if rec := get(t, fwd, p, nil); rec.Code != http.StatusUnauthorized {
				t.Errorf("forward/no header: %d, want 401", rec.Code)
			}
			if rec := get(t, fwd, p, map[string]string{"X-Auth-User": "alice"}); rec.Code != http.StatusOK {
				t.Errorf("forward/header: %d, want 200", rec.Code)
			}
			none := newTestServer(t, config.AuthNone, reg)
			if rec := get(t, none, p, nil); rec.Code != http.StatusOK {
				t.Errorf("none: %d, want 200", rec.Code)
			}
		})
	}
}

func TestNote_symlinkEscapeForbidden(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "s.md"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := notesFixture(t, map[string]string{"keep.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.Symlink(filepath.Join(outside, "s.md"), filepath.Join(nb.Layout.Vault(), "link.md")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	rec := get(t, srv, notePath("link.md"), nil)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("status=%d body=%s", rec.Code, rec.Body)
	}
}

// --- static web assets ---

func TestStaticAssets_unauthenticated(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, nil)
	for path, wantType := range map[string]string{
		"/":          "text/html",
		"/app.js":    "javascript",
		"/style.css": "text/css",
	} {
		rec := get(t, srv, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, wantType) {
			t.Errorf("%s: Content-Type = %q, want %q", path, ct, wantType)
		}
	}
}

func TestStaticAssets_doNotExposeSource(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := get(t, srv, "/embed.go", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/embed.go status = %d, want 404", rec.Code)
	}
}

func TestNote_etagAndLastModified(t *testing.T) {
	content := "# Hi\n"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": content}))
	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	sum := sha256.Sum256([]byte(content))
	if got, want := rec.Header().Get("ETag"), `"`+hex.EncodeToString(sum[:])+`"`; got != want {
		t.Errorf("ETag = %q, want %q", got, want)
	}
	lm := rec.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("missing Last-Modified")
	}
	if _, err := http.ParseTime(lm); err != nil {
		t.Errorf("Last-Modified = %q: %v", lm, err)
	}
}

func TestRaw_etagMatchesNote(t *testing.T) {
	content := "# Same\n"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": content}))
	noteRec := get(t, srv, notePath("n.md"), nil)
	rawRec := get(t, srv, "/n/work/raw?path=n.md", nil)
	if noteRec.Header().Get("ETag") == "" || rawRec.Header().Get("ETag") == "" {
		t.Fatal("both endpoints must set ETag")
	}
	if noteRec.Header().Get("ETag") != rawRec.Header().Get("ETag") {
		t.Errorf("ETag mismatch: note=%q raw=%q (must hash the same file bytes)",
			noteRec.Header().Get("ETag"), rawRec.Header().Get("ETag"))
	}
}

func TestNote_responseCarriesSha(t *testing.T) {
	content := "# Hi\n"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": content}))
	rec := get(t, srv, notePath("n.md"), nil)
	var body struct {
		Sha string `json:"sha"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	sum := sha256.Sum256([]byte(content))
	if want := hex.EncodeToString(sum[:]); body.Sha != want {
		t.Errorf("sha = %q, want %q", body.Sha, want)
	}
	if etag := rec.Header().Get("ETag"); etag != `"`+body.Sha+`"` {
		t.Errorf("sha %q does not match ETag %q", body.Sha, etag)
	}
}
