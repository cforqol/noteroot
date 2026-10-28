package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

func TestNote_irregularTableKeepsEveryCell(t *testing.T) {
	// A body row wider than the header must keep its extra cells in the
	// HTML the read view renders.
	src := "| Name | Qty |\n|---|---|\n| apple | 3 | 4 |\n"
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": src}))
	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		HTML string `json:"html"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<table>", "<td>3</td>", "<td>4</td>", "<td>apple</td>"} {
		if !strings.Contains(body.HTML, want) {
			t.Errorf("html missing %q:\n%s", want, body.HTML)
		}
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
	renderNote = func([]byte, func(string) (string, bool)) ([]byte, error) {
		return nil, errors.New("render exploded")
	}
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
	_ = f.Close()

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
	_ = f.Close()

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

// Last-Modified alone would let browsers reuse a cached note heuristically
// (10% of its age) — right after a write that served the old text and
// sha, and the next save hit a spurious 412. Every note read and JSON
// answer must be revalidated before reuse.
func TestNoteReads_requireRevalidation(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "# Hi\n"}))
	for _, target := range []string{notePath("n.md"), "/n/work/raw?path=n.md", "/n/work/tree"} {
		rec := get(t, srv, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
			t.Errorf("%s: Cache-Control = %q, want %q", target, got, "private, no-cache")
		}
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

// TestNote_linksIntegration covers design §7 end to end: the note
// response carries backlinks/outgoing, and the rendered HTML resolves
// wikilinks against the same links rows the panels read.
func TestNote_linksIntegration(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"index.md":         "---\ntitle: Home\n---\nSee [[projects/plan|the plan]] and [[missing]].\n",
		"projects/plan.md": "---\ntitle: Plan\n---\nBack to [[Home]].\n",
	}))

	// The linking note: outgoing shows resolution + label, backlinks
	// arrive from the note that links here.
	rec := get(t, srv, notePath("index.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var indexResp struct {
		HTML      string `json:"html"`
		Backlinks []struct {
			Path  string `json:"path"`
			Title string `json:"title"`
		} `json:"backlinks"`
		Outgoing []struct {
			Target string `json:"target"`
			Path   string `json:"path"`
			Title  string `json:"title"`
			Label  string `json:"label"`
		} `json:"outgoing"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&indexResp); err != nil {
		t.Fatal(err)
	}
	if len(indexResp.Backlinks) != 1 ||
		indexResp.Backlinks[0].Path != "projects/plan.md" ||
		indexResp.Backlinks[0].Title != "Plan" {
		t.Errorf("backlinks = %+v, want [{projects/plan.md Plan}]", indexResp.Backlinks)
	}
	if len(indexResp.Outgoing) != 2 {
		t.Fatalf("outgoing = %+v, want 2 links", indexResp.Outgoing)
	}
	missing, planned := indexResp.Outgoing[0], indexResp.Outgoing[1]
	if missing.Target != "missing" || missing.Path != "" {
		t.Errorf("outgoing[0] = %+v, want unresolved 'missing'", missing)
	}
	if planned.Target != "projects/plan" || planned.Path != "projects/plan.md" || planned.Label != "the plan" {
		t.Errorf("outgoing[1] = %+v, want resolved with label", planned)
	}
	// Rendered HTML reads the same rows: live link with SPA data-path,
	// dead link as an inert span — never literal [[brackets]].
	if !strings.Contains(indexResp.HTML, `href="/n/work/note?path=projects%2Fplan.md"`) ||
		!strings.Contains(indexResp.HTML, `data-path="projects/plan.md"`) ||
		!strings.Contains(indexResp.HTML, `>the plan</a>`) {
		t.Errorf("html missing resolved wikilink: %s", indexResp.HTML)
	}
	if !strings.Contains(indexResp.HTML, `<span class="wikilink dead">missing</span>`) ||
		strings.Contains(indexResp.HTML, "[[missing]]") {
		t.Errorf("html missing dead span: %s", indexResp.HTML)
	}

	// The target note: incoming panel and the reciprocal link.
	rec = get(t, srv, notePath("projects/plan.md"), nil)
	var planResp struct {
		HTML      string `json:"html"`
		Backlinks []struct {
			Path  string `json:"path"`
			Title string `json:"title"`
		} `json:"backlinks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&planResp); err != nil {
		t.Fatal(err)
	}
	if len(planResp.Backlinks) != 1 || planResp.Backlinks[0].Path != "index.md" {
		t.Errorf("backlinks = %+v, want [{index.md}]", planResp.Backlinks)
	}
	if !strings.Contains(planResp.HTML, `href="/n/work/note?path=index.md"`) {
		t.Errorf("html missing [[Home]] resolution: %s", planResp.HTML)
	}
}

// TestNote_wikilinkHTMLStructure verifies the complete HTML structure
// for wikilinks: resolved links have proper href, class, and data-path;
// dead links are spans with appropriate styling hooks.
func TestNote_wikilinkHTMLStructure(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"source.md": "---\ntitle: Source\n---\nResolved: [[target|custom label]]\nDead: [[nowhere]]\n",
		"target.md": "---\ntitle: Target\n---\nContent.\n",
	}))

	rec := get(t, srv, notePath("source.md"), nil)
	var resp struct {
		HTML     string `json:"html"`
		Outgoing []struct {
			Target string `json:"target"`
			Path   string `json:"path"`
			Label  string `json:"label"`
		} `json:"outgoing"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// Verify resolved wikilink structure
	tests := []struct {
		name    string
		want    string
		missing string
	}{
		{"anchor tag", `<a href="/n/work/note?path=target.md"`, ""},
		{"wikilink class", `class="wikilink"`, ""},
		{"data-path attribute", `data-path="target.md"`, ""},
		{"custom label text", `>custom label</a>`, ""},
		{"no literal brackets", "", `[[target|custom label]]`},
		{"dead span class", `<span class="wikilink dead">nowhere</span>`, ""},
		{"no dead brackets", "", `[[nowhere]]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want != "" && !strings.Contains(resp.HTML, tt.want) {
				t.Errorf("HTML missing %q:\n%s", tt.want, resp.HTML)
			}
			if tt.missing != "" && strings.Contains(resp.HTML, tt.missing) {
				t.Errorf("HTML should not contain %q:\n%s", tt.missing, resp.HTML)
			}
		})
	}

	// Verify outgoing data matches HTML rendering (order may vary)
	if len(resp.Outgoing) != 2 {
		t.Fatalf("outgoing count = %d, want 2", len(resp.Outgoing))
	}

	var resolved, unresolved *struct {
		Target string `json:"target"`
		Path   string `json:"path"`
		Label  string `json:"label"`
	}
	for i := range resp.Outgoing {
		switch resp.Outgoing[i].Target {
		case "target":
			resolved = &resp.Outgoing[i]
		case "nowhere":
			unresolved = &resp.Outgoing[i]
		}
	}

	if resolved == nil || resolved.Path != "target.md" || resolved.Label != "custom label" {
		t.Errorf("resolved link = %+v, want {target target.md custom label}", resolved)
	}
	if unresolved == nil || unresolved.Path != "" {
		t.Errorf("unresolved link = %+v, want {nowhere  }", unresolved)
	}
}

// TestNote_backlinksOutgoingJSONShape verifies the exact JSON structure
// of backlinks and outgoing fields for UI consumption.
func TestNote_backlinksOutgoingJSONShape(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"alpha.md": "---\ntitle: Alpha\naliases:\n  - A\n---\n[[beta]]\n",
		"beta.md":  "---\ntitle: Beta\n---\n[[A|alias link]] and [[missing]].\n",
	}))

	rec := get(t, srv, notePath("beta.md"), nil)
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// Verify backlinks structure (incoming from alpha)
	backlinks, ok := resp["backlinks"].([]interface{})
	if !ok || len(backlinks) != 1 {
		t.Fatalf("backlinks = %v, want array with 1 item", resp["backlinks"])
	}
	bl := backlinks[0].(map[string]interface{})
	if bl["path"] != "alpha.md" || bl["title"] != "Alpha" {
		t.Errorf("backlinks[0] = %+v, want {path:alpha.md title:Alpha}", bl)
	}
	// Backlinks should not have target/label (omitempty)
	if _, has := bl["target"]; has {
		t.Errorf("backlinks should not include 'target' field")
	}

	// Verify outgoing structure (to alpha via alias, plus dead link)
	outgoing, ok := resp["outgoing"].([]interface{})
	if !ok || len(outgoing) != 2 {
		t.Fatalf("outgoing = %v, want array with 2 items", resp["outgoing"])
	}

	aliasLink := outgoing[0].(map[string]interface{})
	if aliasLink["target"] != "A" || aliasLink["path"] != "alpha.md" ||
		aliasLink["title"] != "Alpha" || aliasLink["label"] != "alias link" {
		t.Errorf("outgoing[0] = %+v, want alias resolution with label", aliasLink)
	}

	deadLink := outgoing[1].(map[string]interface{})
	if deadLink["target"] != "missing" || deadLink["path"] != "" || deadLink["title"] != "" {
		t.Errorf("outgoing[1] = %+v, want unresolved {target:missing path: title:}", deadLink)
	}
	// Empty label should be omitted (omitempty)
	if _, has := deadLink["label"]; has {
		t.Errorf("outgoing[1] should not include 'label' when empty")
	}
}

// TestNote_wikilinkSPAAttributes verifies data-path extraction for the
// SPA click handler (design §8: middle-click, cmd-click work via href).
func TestNote_wikilinkSPAAttributes(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"index.md":             "[[nested/deep]]",
		"nested/deep.md":       "---\ntitle: Deep\n---\nContent",
		"paths with spaces.md": "[[nested/deep]]",
	}))

	tests := []struct {
		notePath string
		wantHref string
		wantData string
	}{
		{
			"index.md",
			`href="/n/work/note?path=nested%2Fdeep.md"`,
			`data-path="nested/deep.md"`,
		},
		{
			"paths with spaces.md",
			`href="/n/work/note?path=nested%2Fdeep.md"`,
			`data-path="nested/deep.md"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.notePath, func(t *testing.T) {
			rec := get(t, srv, notePath(tt.notePath), nil)
			var resp struct{ HTML string }
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(resp.HTML, tt.wantHref) {
				t.Errorf("HTML missing encoded href %q:\n%s", tt.wantHref, resp.HTML)
			}
			if !strings.Contains(resp.HTML, tt.wantData) {
				t.Errorf("HTML missing data-path %q:\n%s", tt.wantData, resp.HTML)
			}

			// Verify it's a real anchor (middle-click and no-JS work)
			if !strings.Contains(resp.HTML, `<a href=`) {
				t.Errorf("resolved wikilink must be an anchor tag for href navigation")
			}
		})
	}
}

// TestNote_panelDataConsistency verifies that the outgoing/backlinks
// JSON data matches what the HTML renderer used (design §7: display
// and data never disagree).
func TestNote_panelDataConsistency(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "---\ntitle: A\n---\n[[b|link to B]]\n",
		"b.md": "---\ntitle: B\n---\n[[a]]\n",
	}))

	rec := get(t, srv, notePath("a.md"), nil)
	var resp struct {
		HTML     string `json:"html"`
		Outgoing []struct {
			Target string `json:"target"`
			Path   string `json:"path"`
			Label  string `json:"label"`
		} `json:"outgoing"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// The outgoing panel shows: target="b", path="b.md", label="link to B"
	if len(resp.Outgoing) != 1 {
		t.Fatalf("outgoing = %v, want 1 link", resp.Outgoing)
	}
	out := resp.Outgoing[0]

	// The HTML must use the same resolution: path b.md
	expectedHref := fmt.Sprintf(`href="/n/work/note?path=%s"`, url.QueryEscape(out.Path))
	if !strings.Contains(resp.HTML, expectedHref) {
		t.Errorf("HTML href disagrees with outgoing.path:\noutgoing: %+v\nHTML: %s",
			out, resp.HTML)
	}

	// The label in HTML must match outgoing.label
	expectedLabel := fmt.Sprintf(`>%s</a>`, out.Label)
	if !strings.Contains(resp.HTML, expectedLabel) {
		t.Errorf("HTML label disagrees with outgoing.label:\noutgoing: %+v\nHTML: %s",
			out, resp.HTML)
	}

	// The data-path must match
	expectedData := fmt.Sprintf(`data-path="%s"`, out.Path)
	if !strings.Contains(resp.HTML, expectedData) {
		t.Errorf("HTML data-path disagrees with outgoing.path:\noutgoing: %+v\nHTML: %s",
			out, resp.HTML)
	}
}

func TestNote_indexUnavailableStillRenders(t *testing.T) {
	reg := notesFixture(t, map[string]string{"n.md": "see [[x]] and more\n"})
	nb, _ := reg.Get("work")
	// Block the app dir so index.Open fails: opening the note must not.
	if err := os.WriteFile(nb.Layout.App(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	rec := get(t, srv, notePath("n.md"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (index trouble never blocks a note)", rec.Code)
	}
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["backlinks"]; ok {
		t.Error("backlinks must be omitted when the index is unavailable")
	}
	var html string
	_ = json.Unmarshal(resp["html"], &html)
	if !strings.Contains(html, `class="wikilink dead"`) {
		t.Errorf("html = %q, want dead-span wikilinks", html)
	}
}
