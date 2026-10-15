package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func writeVaultFile(t *testing.T, vaultDir, rel, content string) {
	t.Helper()
	p := filepath.Join(vaultDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func searchPath(q string) string { return "/n/work/search?q=" + url.QueryEscape(q) }

func TestSearch_endpoint(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "---\ntitle: Alpha\n---\nthe quick brown fox\n",
		"b.md": "something else entirely\n",
	}))
	rec := get(t, srv, searchPath("quick"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Query string `json:"query"`
		Hits  []struct {
			Path    string `json:"path"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Query != "quick" {
		t.Errorf("query = %q, want quick", body.Query)
	}
	if len(body.Hits) != 1 || body.Hits[0].Path != "a.md" {
		t.Fatalf("hits = %+v, want [a.md]", body.Hits)
	}
	if body.Hits[0].Title != "Alpha" {
		t.Errorf("title = %q, want Alpha", body.Hits[0].Title)
	}
	if !strings.Contains(body.Hits[0].Snippet, "<b>quick</b>") {
		t.Errorf("snippet = %q, want <b>quick</b> highlight", body.Hits[0].Snippet)
	}
}

func TestSearch_seesNewNotesWithoutRestart(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "old content\n"})
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := get(t, srv, searchPath("newterm"), nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	nb, _ := reg.Get("work")
	writeVaultFile(t, nb.Layout.Vault(), "b.md", "brand newterm here\n")
	rec := get(t, srv, searchPath("newterm"), nil)
	var body struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Hits) != 1 || body.Hits[0].Path != "b.md" {
		t.Fatalf("hits = %+v, want [b.md]", body.Hits)
	}
}

func TestSearch_snippetSanitized(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"x.md": "<script>alert(1)</script> searchable marker text here\n",
	}))
	rec := get(t, srv, searchPath("marker"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("raw script tag leaked into search response: %s", rec.Body)
	}
}

func TestSearch_limit(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".md"] = "commonword filler\n"
	}
	srv := newTestServer(t, config.AuthNone, notesFixture(t, files))
	rec := get(t, srv, "/n/work/search?q=commonword&limit=2", nil)
	var body struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Hits) != 2 {
		t.Errorf("len(hits) = %d, want 2", len(body.Hits))
	}
}

func TestSearch_errors(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	tests := []struct {
		name, target string
		want         int
	}{
		{"missing query", "/n/work/search", http.StatusBadRequest},
		{"empty query", "/n/work/search?q=", http.StatusBadRequest},
		{"whitespace query", "/n/work/search?q=%20%20", http.StatusBadRequest},
		{"fts syntax error", "/n/work/search?q=%22unterminated", http.StatusBadRequest},
		{"unknown notebook", "/n/nope/search?q=x", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := get(t, srv, tt.target, nil); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestSearch_requiresAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, notesFixture(t, map[string]string{"a.md": "x"}))
	if rec := get(t, srv, searchPath("x"), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	rec := get(t, srv, searchPath("x"), map[string]string{"X-Auth-User": "alice"})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSearch_limitClamped(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "commonword\n"}))
	rec := get(t, srv, "/n/work/search?q=commonword&limit=99999", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Hits []any `json:"hits"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Hits) != 1 {
		t.Errorf("len(hits) = %d, want 1", len(body.Hits))
	}
}

func TestSearch_vaultGone(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.RemoveAll(nb.Layout.Vault()); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := get(t, srv, searchPath("x"), nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestSearch_indexUnwritable(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.WriteFile(nb.Layout.App(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := get(t, srv, searchPath("x"), nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
