package httpapi

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noteroot/internal/config"
)

// installRepoPlugin copies a proof plugin from the repo's plugins/
// directory into the fixture notebook.
func installRepoPlugin(t *testing.T, srv *Server, name string) {
	t.Helper()
	src := filepath.Join("..", "..", "plugins", name)
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("fixture notebook missing")
	}
	dst := filepath.Join(nb.Layout.Plugins(), name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The three proof plugins over real HTTP: a human write triggers the
// tagger hook, the word-count panel lands in platform.db, and the
// theme command round-trips — the same path a user takes after
// copying the directories in and hitting reload.
func TestProofPlugins_endToEnd(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"projects/a.md": "ship the release\n",
	}))
	installRepoPlugin(t, srv, "tagger")
	installRepoPlugin(t, srv, "word-count")
	installRepoPlugin(t, srv, "theme")

	reload := doReq(t, srv, http.MethodPost, "/n/work/plugins/reload", "", nil)
	if reload.Code != http.StatusOK {
		t.Fatalf("reload = %d, body=%s", reload.Code, reload.Body)
	}
	for _, name := range []string{"tagger", "word-count", "theme"} {
		if !strings.Contains(reload.Body.String(), `"name":"`+name+`"`) ||
			!strings.Contains(reload.Body.String(), `"loaded":true`) {
			t.Errorf("reload missing loaded %s: %s", name, reload.Body)
		}
	}

	// Human write: tagger adds tags, word-count publishes a panel.
	put := doReq(t, srv, http.MethodPut, notePath("projects/a.md"), "ship the release\n", nil)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT = %d, body=%s", put.Code, put.Body)
	}
	// The tagger's frontmatter lands in the vault; the raw endpoint
	// returns it verbatim (the note view strips frontmatter). YAML
	// marshalling indents sequences, so check key and item apart.
	eventuallyHTTP(t, "tagger wrote tags into the vault", func() bool {
		n := doReq(t, srv, http.MethodGet, "/n/work/raw?path=projects/a.md", "", nil)
		return n.Code == http.StatusOK &&
			strings.Contains(n.Body.String(), "tags:") &&
			strings.Contains(n.Body.String(), "- project")
	})
	eventuallyHTTP(t, "word-count panel published", func() bool {
		p := doReq(t, srv, http.MethodGet, "/n/work/plugins/panels", "", nil)
		return p.Code == http.StatusOK && strings.Contains(p.Body.String(), "words")
	})
	eventuallyHTTP(t, "tagger write audited", func() bool {
		nb, _ := srv.reg.Get("work")
		entries, err := auditEntries(t, nb.Layout.PlatformDB())
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Action == "note.write" && e.Actor == "plugin:tagger" {
				return true
			}
		}
		return false
	})

	// Theme over HTTP.
	set := doReq(t, srv, http.MethodPost, "/n/work/plugins/theme/commands/set-theme",
		`{"theme":"dark"}`, map[string]string{"Content-Type": "application/json"})
	if set.Code != http.StatusOK {
		t.Fatalf("set-theme = %d, body=%s", set.Code, set.Body)
	}
	panels := doReq(t, srv, http.MethodGet, "/n/work/plugins/panels", "", nil)
	if !strings.Contains(panels.Body.String(), `"type":"theme"`) ||
		!strings.Contains(panels.Body.String(), `"name":"dark"`) {
		t.Errorf("theme panel = %s", panels.Body)
	}
}

func eventuallyHTTP(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", what)
}
