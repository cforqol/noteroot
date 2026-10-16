package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/platform"
)

// auditEntries reads the notebook's platform audit trail.
func auditEntries(t *testing.T, dbPath string) ([]platform.AuditEntry, error) {
	t.Helper()
	db, _, err := platform.Open(context.Background(), dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.AuditQuery(context.Background(), 50)
}

// writeTestPlugin drops a plugin directory into the fixture notebook.
// The server already Ensured at construction (empty), so tests call
// the reload endpoint afterwards — which is the documented way to pick
// up plugins without a restart.
func writeTestPlugin(t *testing.T, srv *Server, name, manifest, lua string) string {
	t.Helper()
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("fixture notebook missing")
	}
	dir := filepath.Join(nb.Layout.Plugins(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.lua"), []byte(lua), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPlugins_listReloadCommandPanels(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "---\ntitle: A\n---\n\nhello body\n",
	}))
	writeTestPlugin(t, srv, "stress", `
name = "stress"
version = "0.1.0"
permissions = ["note:read", "ui:write"]
`, `
register_command("peek", function()
  local n = note.read("a.md")
  return { title = (n and n.title) or "?" }
end)
register_command("status", function()
  ui.set_panel("wc", { text = "42 words", level = "info" })
  return true
end)
`)

	// Reload picks the plugin up.
	reload := doReq(t, srv, http.MethodPost, "/n/work/plugins/reload", "", nil)
	if reload.Code != http.StatusOK {
		t.Fatalf("reload = %d, body=%s", reload.Code, reload.Body)
	}
	if !strings.Contains(reload.Body.String(), `"stress"`) ||
		!strings.Contains(reload.Body.String(), `"reloaded":true`) {
		t.Errorf("reload body = %s", reload.Body)
	}

	// List shows it loaded with its commands.
	list := doReq(t, srv, http.MethodGet, "/n/work/plugins", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d", list.Code)
	}
	body := list.Body.String()
	for _, want := range []string{`"name":"stress"`, `"loaded":true`, `"peek"`, `"note:read"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %s: %s", want, body)
		}
	}

	// Command dispatch: capability reads through to the vault.
	cmd := doReq(t, srv, http.MethodPost, "/n/work/plugins/stress/commands/peek", "",
		map[string]string{"Content-Type": "application/json"})
	if cmd.Code != http.StatusOK {
		t.Fatalf("peek = %d, body=%s", cmd.Code, cmd.Body)
	}
	if !strings.Contains(cmd.Body.String(), `"title":"A"`) {
		t.Errorf("peek body = %s", cmd.Body)
	}

	// Panels land in platform.db and surface on the panels endpoint.
	if rec := doReq(t, srv, http.MethodPost, "/n/work/plugins/stress/commands/status", "",
		map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	panels := doReq(t, srv, http.MethodGet, "/n/work/plugins/panels", "", nil)
	if panels.Code != http.StatusOK {
		t.Fatalf("panels = %d", panels.Code)
	}
	if !strings.Contains(panels.Body.String(), "42 words") {
		t.Errorf("panels body = %s", panels.Body)
	}

	// Error mapping: unknown plugin 404, unknown command 400.
	if rec := doReq(t, srv, http.MethodPost, "/n/work/plugins/ghost/commands/x", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("ghost plugin = %d, want 404", rec.Code)
	}
	if rec := doReq(t, srv, http.MethodPost, "/n/work/plugins/stress/commands/nope", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown command = %d, want 400", rec.Code)
	}
}

// The whole phase-3 chain over HTTP: a mutation publishes an event, a
// plugin hook receives it, its write runs the real write path, and the
// audit trail attributes it to the plugin.
func TestPlugins_hookWritesNoteOverHTTP(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "original\n"}))
	nb, _ := srv.reg.Get("work")

	writeTestPlugin(t, srv, "mirror", `
name = "mirror"
version = "0.1.0"
permissions = ["note:write"]
`, `
on("note.write", function(ev)
  if ev.path == "a.md" then
    note.write("derived.md", "mirrored: " .. ev.path)
  end
end)
`)
	if rec := doReq(t, srv, http.MethodPost, "/n/work/plugins/reload", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("reload = %d, body=%s", rec.Code, rec.Body)
	}

	// Human write to a.md triggers the hook.
	put := doReq(t, srv, http.MethodPut, notePath("a.md"), "updated body\n", nil)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT = %d, body=%s", put.Code, put.Body)
	}

	// Hook delivery: bus window (500ms) + queue + VM — poll.
	deadline := time.Now().Add(4 * time.Second)
	for {
		got := doReq(t, srv, http.MethodGet, "/n/work/note?path=derived.md", "", nil)
		if got.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook never wrote derived.md, last status %d", got.Code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := doReq(t, srv, http.MethodGet, "/n/work/note?path=derived.md", "", nil)
	if !strings.Contains(got.Body.String(), "mirrored: a.md") {
		t.Errorf("derived.md body = %s", got.Body)
	}

	// The audit trail names the plugin, and the hook's write did not
	// loop (self-suppression + path guard).
	entries, err := auditEntries(t, nb.Layout.PlatformDB())
	if err != nil {
		t.Fatal(err)
	}
	pluginWrite := false
	for _, e := range entries {
		if e.Action == "note.write" && e.Actor == "plugin:mirror" {
			pluginWrite = true
		}
	}
	if !pluginWrite {
		t.Errorf("audit has no note.write by plugin:mirror: %+v", entries)
	}
	time.Sleep(700 * time.Millisecond) // > coalesce window: a loop would add writes
	if again := doReq(t, srv, http.MethodGet, "/n/work/note?path=derived.md", "", nil); again.Code != http.StatusOK {
		t.Errorf("derived.md disappeared: %d", again.Code)
	}
}
