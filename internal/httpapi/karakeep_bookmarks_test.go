package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"noteroot/internal/config"
)

func TestKarakeepBookmarks_groupsByURLAcrossNotes(t *testing.T) {
	files := map[string]string{
		"a.md": "See https://example.com/x ([archived](https://karakeep.example/bookmarks/1)) for details.\n",
		"b.md": "Also https://example.com/x ([archived](https://karakeep.example/bookmarks/1)) here.\n" +
			"And https://other.example/y ([archived](https://karakeep.example/bookmarks/2)).\n",
		"c.md": "No archived links in this note at all.\n",
	}
	srv := newTestServer(t, config.AuthNone, notesFixture(t, files))
	rec := get(t, srv, "/n/work/integrations/karakeep/bookmarks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Bookmarks []struct {
			URL        string   `json:"url"`
			ArchiveURL string   `json:"archiveUrl"`
			Paths      []string `json:"paths"`
		} `json:"bookmarks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Bookmarks) != 2 {
		t.Fatalf("bookmarks = %+v, want 2", body.Bookmarks)
	}
	byURL := map[string][]string{}
	for _, b := range body.Bookmarks {
		byURL[b.URL] = b.Paths
	}
	paths1 := byURL["https://example.com/x"]
	if len(paths1) != 2 {
		t.Errorf("https://example.com/x paths = %v, want a.md and b.md", paths1)
	}
	paths2 := byURL["https://other.example/y"]
	if len(paths2) != 1 || paths2[0] != "b.md" {
		t.Errorf("https://other.example/y paths = %v, want [b.md]", paths2)
	}
}

func TestKarakeepBookmarks_emptyWhenNoneArchived(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "plain note, no links\n"}))
	rec := get(t, srv, "/n/work/integrations/karakeep/bookmarks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Bookmarks []any `json:"bookmarks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Bookmarks) != 0 {
		t.Errorf("bookmarks = %+v, want empty", body.Bookmarks)
	}
}

func TestKarakeepBookmarks_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := get(t, srv, "/n/nope/integrations/karakeep/bookmarks", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
