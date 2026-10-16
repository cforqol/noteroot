package httpapi

import (
	"context"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/frontmatter"
	"noteroot/internal/plugins"
	"noteroot/internal/state"
)

func TestPluginNoteStore_badIDMinted(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"seed.md": "seed\n"}))
	nb, _ := srv.reg.Get("work")
	st := &pluginNoteStore{s: srv, slug: "work"}

	err := st.Write(context.Background(), plugins.WriteRequest{
		Actor: "plugin:p", Origin: "plugin:p", Path: "n.md",
		Content: "---\nid: bogus\n---\nbody\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Read(context.Background(), "n.md")
	if err != nil {
		t.Fatal(err)
	}
	id := frontmatter.Parse([]byte(got)).ID
	if !frontmatter.ValidID(id) || id == "bogus" {
		t.Fatalf("bad id not re-minted, id=%q", id)
	}
	// The minted id is what the identity map recorded.
	db, _, err := state.Open(context.Background(), nb.Layout.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	uid, ok, err := db.UIDByPath(context.Background(), "n.md")
	if err != nil || !ok || uid != id {
		t.Errorf("identity = %q,%v,%v want %q,true,nil", uid, ok, err, id)
	}
}

func TestPluginNoteStore_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"seed.md": "seed\n"}))
	st := &pluginNoteStore{s: srv, slug: "missing"}
	if _, err := st.Read(context.Background(), "a.md"); err == nil {
		t.Error("read on unknown notebook must fail")
	}
	if err := st.Write(context.Background(), plugins.WriteRequest{Path: "a.md"}); err == nil {
		t.Error("write on unknown notebook must fail")
	}
}

func TestPluginNoteStore_writeErrorPassthrough(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"seed.md": "seed\n"}))
	st := &pluginNoteStore{s: srv, slug: "work"}
	if err := st.Write(context.Background(), plugins.WriteRequest{
		Path: "../evil.md", Content: "x",
	}); err == nil {
		t.Error("escaping write must fail")
	}
}

func TestPluginNoteStore_attribution(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"seed.md": "seed\n"}))
	nb, _ := srv.reg.Get("work")
	st := &pluginNoteStore{s: srv, slug: "work"}
	if err := st.Write(context.Background(), plugins.WriteRequest{
		Actor: "plugin:tagger", Origin: "plugin:tagger", Depth: 1,
		Path: "p.md", Content: "hello\n",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, err := auditEntries(t, nb.Layout.PlatformDB())
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Action == "note.write" && e.Actor == "plugin:tagger" && e.Resource == "work/p.md" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("audit never recorded the plugin write with its actor")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
