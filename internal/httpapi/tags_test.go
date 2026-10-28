package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"noteroot/internal/config"
)

func TestListTags(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md":     "---\ntags: [go, \"#web\"]\n---\nbody",
		"sub/b.md": "---\ntags:\n  - go\n---\nbody",
		"plain.md": "no frontmatter",
	}))
	rec := get(t, srv, "/n/work/tags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Tags map[string][]string `json:"tags"`
	}
	decodeJSON(t, rec, &resp)
	want := map[string][]string{"go": {"a.md", "sub/b.md"}, "web": {"a.md"}}
	if !reflect.DeepEqual(resp.Tags, want) {
		t.Fatalf("tags = %v, want %v", resp.Tags, want)
	}
	if rec := get(t, srv, "/n/nope/tags", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown notebook status = %d", rec.Code)
	}
}
