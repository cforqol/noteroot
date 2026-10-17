package plugins

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore is the note capability for tests: an in-memory map plus a
// write log so attribution and depth can be asserted.
type fakeStore struct {
	mu     sync.Mutex
	notes  map[string]string
	writes []WriteRequest
}

func newFakeStore() *fakeStore {
	return &fakeStore{notes: map[string]string{
		"a.md": "---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV00\ntitle: A\n---\n\nbody a\n",
	}}
}

func (f *fakeStore) Read(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.notes[path]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) Write(_ context.Context, wr WriteRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes[wr.Path] = wr.Content
	f.writes = append(f.writes, wr)
	return nil
}

func (f *fakeStore) lastWrite() (WriteRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return WriteRequest{}, false
	}
	return f.writes[len(f.writes)-1], true
}

func (f *fakeStore) get(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notes[path]
}

func (f *fakeStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// fixture lays out one plugin directory under a temp notebook and
// returns everything a Manager needs to load it.
type fixture struct {
	layout notebooks.Layout
	store  *fakeStore
	bus    *platform.Bus
	mgr    *Manager
	dir    string
}

func newFixture(t *testing.T, name, manifest, luaSrc string) *fixture {
	t.Helper()
	return newFixtureWithStore(t, name, manifest, luaSrc, newFakeStore())
}

func newFixtureWithStore(t *testing.T, name, manifest, luaSrc string, store NoteStore) *fixture {
	t.Helper()
	root := t.TempDir()
	layout := notebooks.NewLayout(root)
	dir := filepath.Join(layout.Plugins(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if luaSrc != "" {
		if err := os.WriteFile(filepath.Join(dir, DefaultEntry), []byte(luaSrc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bus := platform.NewBus("work", testLogger())
	mgr := NewManager(testLogger(), func(string) NoteStore { return store })
	t.Cleanup(func() { mgr.Close(); bus.Close() })
	f := &fixture{layout: layout, bus: bus, mgr: mgr, dir: dir}
	if fs, ok := store.(*fakeStore); ok {
		f.store = fs
	}
	return f
}

func (f *fixture) ensure(t *testing.T) {
	t.Helper()
	if err := f.mgr.Ensure("work", f.layout, f.bus); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
}

func (f *fixture) plugin(t *testing.T) *Plugin {
	t.Helper()
	f.mgr.mu.Lock()
	defer f.mgr.mu.Unlock()
	rt := f.mgr.nbs["work"]
	if rt == nil || len(rt.plugins) != 1 {
		t.Fatalf("want one loaded plugin, got %+v", rt)
	}
	return rt.plugins[0]
}

func (f *fixture) run(t *testing.T, cmd, args string) (any, error) {
	t.Helper()
	return f.mgr.RunCommand(context.Background(), "work", "test", cmd, []byte(args))
}

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", what)
}

func TestCommand_roundTrip(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
permissions = []
`, `
register_command("hello", function(args)
  return { msg = "hi " .. (args.who or "world"), n = 2 }
end)
`)
	f.ensure(t)

	got, err := f.run(t, "hello", `{"who":"you"}`)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["msg"] != "hi you" {
		t.Fatalf("result = %#v", got)
	}
	if m["n"] != float64(2) {
		t.Errorf("n = %#v, want 2", m["n"])
	}

	if _, err := f.run(t, "nope", ""); !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("unknown command err = %v", err)
	}
	_, err = f.mgr.RunCommand(context.Background(), "work", "ghost", "x", nil)
	if !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("unknown plugin err = %v", err)
	}
}

// The sandbox must expose no path to the host filesystem, process
// environment, package loading, or code loading — verified from
// inside the VM itself, not just from the Go side.
func TestSandbox_noHostAccess(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
`, `
local leaked = {}
local probes = { "os", "io", "package", "debug", "coroutine", "channel",
  "require", "dofile", "loadfile", "load", "loadstring",
  "setfenv", "getfenv", "newproxy", "module" }
for _, name in ipairs(probes) do
  if _G[name] ~= nil then
    table.insert(leaked, name .. "=" .. type(_G[name]))
  end
end
register_command("probe", function()
  return { leaked = table.concat(leaked, ",") }
end)
register_command("getenv", function()
  if os ~= nil and os.getenv ~= nil then
    return { v = tostring(os.getenv("HOME")) }
  end
  return { v = "blocked" }
end)
`)
	f.ensure(t)

	got, err := f.run(t, "probe", "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if m := got.(map[string]any)["leaked"]; m != "" {
		t.Errorf("sandbox leaked globals: %v", m)
	}
	got, err = f.run(t, "getenv", "")
	if err != nil {
		t.Fatalf("getenv: %v", err)
	}
	if m := got.(map[string]any)["v"]; m != "blocked" {
		t.Errorf("os.getenv reachable: %v", m)
	}
}

// A wedged plugin times out, tears down, and after repeated failures
// opens its breaker — then recovers on a successful probe.
func TestTimeout_breakerOpensAndRecovers(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
`, `
register_command("spin", function() while true do end end)
register_command("ok", function() return "fine" end)
`)
	f.ensure(t)
	p := f.plugin(t)
	p.cmdTimeout = 80 * time.Millisecond

	for i := 0; i < 3; i++ {
		if _, err := f.run(t, "spin", ""); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("spin %d err = %v, want deadline", i, err)
		}
	}
	// Breaker open: even the healthy command is refused.
	if _, err := f.run(t, "ok", ""); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("after 3 strikes err = %v, want ErrBreakerOpen", err)
	}
	if !p.br.open() {
		t.Fatal("breaker not open")
	}

	// Shorten the cooldown, wait it out, probe with a healthy
	// command: success closes the circuit.
	p.br.mu.Lock()
	p.br.cooldown = 5 * time.Millisecond
	p.br.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if _, err := f.run(t, "ok", ""); err != nil {
		t.Fatalf("probe after cooldown: %v", err)
	}
	if p.br.open() {
		t.Error("breaker still open after successful probe")
	}
	// The timeout tore the VM down and rebuilt it: commands work.
	if v, err := f.run(t, "ok", ""); err != nil || v != "fine" {
		t.Errorf("post-rebuild command = %v, %v", v, err)
	}
}

// blockingStore simulates a capability whose Go side does blocking
// I/O that honors its context (an HTTP NoteStore, a locked SQLite
// call): Read signals entry, then parks until its ctx dies.
type blockingStore struct {
	*fakeStore
	started chan struct{}
}

func newBlockingStore() *blockingStore {
	return &blockingStore{fakeStore: newFakeStore(), started: make(chan struct{}, 1)}
}

func (b *blockingStore) Read(ctx context.Context, path string) (string, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return "", ctx.Err()
}

// A blocking capability call must inherit the invocation's deadline:
// when the command timeout fires, the parked Go call is cancelled
// right away — it must not run detached on its own capabilityTimeout
// (5s) while the invocation mutex stays held.
func TestCapability_blockingCallInheritsInvocationDeadline(t *testing.T) {
	bs := newBlockingStore()
	f := newFixtureWithStore(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["note:read"]
`, `
register_command("peek", function()
  local n, err = note.read("a.md")
  return { found = n ~= nil, err = err }
end)
`, bs)
	f.ensure(t)
	p := f.plugin(t)
	p.cmdTimeout = 80 * time.Millisecond

	start := time.Now()
	_, err := f.run(t, "peek", "")
	elapsed := time.Since(start)

	select {
	case <-bs.started:
	default:
		t.Fatal("command never reached the blocking capability")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	// Generous CI margin: the point is that it returned long before
	// capabilityTimeout (5s) would have expired the detached context.
	if elapsed > 2*time.Second {
		t.Errorf("blocking capability held the invocation for %v, want ~cmdTimeout", elapsed)
	}
	// The timeout tore the VM down once; a healthy follow-up works.
	if p.br.open() {
		t.Error("breaker opened on a single timeout")
	}
}

// Close() must cancel an in-flight invocation blocked inside a
// capability instead of waiting out its timeout: stopCtx is cancelled
// first, the parked Go call returns, and p.mu is released.
func TestCapability_closeCancelsInFlightInvocation(t *testing.T) {
	bs := newBlockingStore()
	f := newFixtureWithStore(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["note:read"]
`, `
register_command("peek", function()
  local n, err = note.read("a.md")
  return { found = n ~= nil, err = err }
end)
`, bs)
	f.ensure(t)
	p := f.plugin(t)
	// A long timeout: without stopCtx cancellation Close would block
	// here for the full command timeout.
	p.cmdTimeout = 30 * time.Second

	done := make(chan error, 1)
	go func() {
		_, err := f.run(t, "peek", "")
		done <- err
	}()
	select {
	case <-bs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("command never reached the blocking capability")
	}

	start := time.Now()
	p.Close()
	closeElapsed := time.Since(start)
	if closeElapsed > 2*time.Second {
		t.Errorf("Close blocked for %v behind an in-flight capability", closeElapsed)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("command err = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight command did not return after Close")
	}
}

// Hooks receive HTTP-originated events, plugin writes carry the
// plugin's attribution and hop depth, and a plugin never sees the
// events it caused itself.
func TestHooks_receiveAttributeAndSelfSuppress(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["note:read", "note:write", "kv:write"]
`, `
on("note.write", function(ev)
  note.write("copy-" .. ev.path, "from hook: " .. ev.path)
  kv.set("last", ev.path)
end)
`)
	f.ensure(t)

	// HTTP-origin event reaches the hook; the hook's write is
	// attributed to the plugin and one hop deeper.
	f.bus.Publish(platform.Event{
		Type: "note.write", Path: "a.md", Origin: "http", Depth: 0, Actor: "alice",
	})
	f.bus.Flush()

	eventually(t, "hook wrote copy-a.md", func() bool {
		wr, ok := f.store.lastWrite()
		return ok && wr.Path == "copy-a.md"
	})
	wr, _ := f.store.lastWrite()
	if wr.Actor != "plugin:test" || wr.Origin != "plugin:test" {
		t.Errorf("write attribution = %s/%s, want plugin:test", wr.Actor, wr.Origin)
	}
	if wr.Depth != 1 {
		t.Errorf("write depth = %d, want 1 (0 + 1 hop)", wr.Depth)
	}
	if !strings.Contains(wr.Content, "from hook: a.md") {
		t.Errorf("write content = %q", wr.Content)
	}

	// Self-suppression: the plugin's own write event (origin
	// plugin:test) must not re-enter its hook — no copy-copy-a.md,
	// ever.
	f.store.mu.Lock()
	f.store.writes = nil
	f.store.mu.Unlock()
	f.bus.Publish(platform.Event{
		Type: "note.write", Path: "copy-a.md", Origin: "plugin:test", Depth: 1, Actor: "plugin:test",
	})
	f.bus.Flush()
	time.Sleep(150 * time.Millisecond)
	if n := f.store.writeCount(); n != 0 {
		t.Errorf("self-origin event triggered %d writes, want 0", n)
	}
}

// Permissions: read-only plugins cannot write; qualified grants only
// cover their subtree; capability denial is a Lua error the command
// sees, not a server failure.
func TestPermissions_enforcedAtCallTime(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["note:read:projects/*"]
`, `
register_command("peek", function()
  local n = note.read("projects/x.md")
  if n == nil then return { found = false } end
  return { found = true, title = n.title }
end)
register_command("peek-outside", function()
  -- capability denial raises a catchable Lua error
  local ok, n = pcall(function() return note.read("a.md") end)
  if ok then return { found = n ~= nil, err = nil } end
  return { found = false, err = tostring(n) }
end)
register_command("wipe", function()
  return note.write("projects/x.md", "clobbered")
end)
`)
	// The fixture store needs the qualified path present.
	f.store.mu.Lock()
	f.store.notes["projects/x.md"] = "---\ntitle: P\n---\n\nproj body\n"
	f.store.mu.Unlock()
	f.ensure(t)

	got, err := f.run(t, "peek", "")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if m := got.(map[string]any); m["found"] != true {
		t.Errorf("qualified read = %#v", got)
	}

	got, err = f.run(t, "peek-outside", "")
	if err != nil {
		t.Fatalf("peek-outside: %v", err)
	}
	m := got.(map[string]any)
	if m["found"] != false {
		t.Errorf("unqualified path was read: %#v", got)
	}
	if m["err"] == nil || !strings.Contains(m["err"].(string), "permission denied") {
		t.Errorf("err = %v, want permission denied", m["err"])
	}

	if _, err := f.run(t, "wipe", ""); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("write with read-only grant err = %v, want permission denied", err)
	}
	if f.store.writeCount() != 0 {
		t.Errorf("denied write still reached the store %d times", f.store.writeCount())
	}
}

// fm.parse/fm.build round-trip frontmatter as tables — the mechanism
// the frontmatter tagger (phase 5) builds on.
func TestCapabilities_frontmatterRoundTrip(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["note:read", "note:write"]
`, `
register_command("tag", function()
  local n = note.read("a.md")
  n.meta.tags = { "alpha", "beta" }
  note.write("a.md", n)
  return { title = n.title }
end)
`)
	f.ensure(t)

	got, err := f.run(t, "tag", "")
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if m := got.(map[string]any); m["title"] != "A" {
		t.Errorf("title = %#v", got)
	}
	wr, ok := f.store.lastWrite()
	if !ok {
		t.Fatal("no write reached the store")
	}
	for _, want := range []string{"tags:", "alpha", "beta", "title: A", "body a"} {
		if !strings.Contains(wr.Content, want) {
			t.Errorf("written note missing %q:\n%s", want, wr.Content)
		}
	}
	if !strings.HasPrefix(wr.Content, "---\n") {
		t.Errorf("frontmatter block lost:\n%s", wr.Content)
	}
}

// kv and panels are permission-gated and survive across commands
// (they live in platform.db, not in the VM).
func TestCapabilities_kvAndPanels(t *testing.T) {
	f := newFixture(t, "test", `
name = "test"
version = "0.1.0"
permissions = ["kv:read", "kv:write", "ui:write"]
`, `
register_command("remember", function(args)
  kv.set("who", args.who)
  return { saved = kv.get("who") }
end)
register_command("status", function()
  ui.set_panel("wc", { text = "42 words", level = "info", order = 1 })
  return true
end)
`)
	f.ensure(t)

	got, err := f.run(t, "remember", `{"who":"ada"}`)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if m := got.(map[string]any); m["saved"] != "ada" {
		t.Errorf("kv round trip = %#v", got)
	}
	// Value persisted outside the VM: a fresh read through kv works
	// after (same plugin instance) — and through the manager panels
	// API for ui.
	if _, err := f.run(t, "status", ""); err != nil {
		t.Fatalf("status: %v", err)
	}
	panels, err := f.mgr.Panels("work")
	if err != nil {
		t.Fatalf("Panels: %v", err)
	}
	raw, ok := panels["wc"]
	if !ok {
		t.Fatalf("panels = %#v, want wc", panels)
	}
	if !strings.Contains(string(raw), "42 words") {
		t.Errorf("panel json = %s", raw)
	}
}

func TestManager_listDisabledAndBroken(t *testing.T) {
	root := t.TempDir()
	layout := notebooks.NewLayout(root)
	// disabled
	dis := filepath.Join(layout.Plugins(), "off")
	_ = os.MkdirAll(dis, 0o755)
	_ = os.WriteFile(filepath.Join(dis, ManifestFile),
		[]byte("name = \"off\"\nversion = \"1.0.0\"\nenabled = false\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dis, DefaultEntry), []byte(""), 0o644)
	// broken manifest (bad permission spec)
	bad := filepath.Join(layout.Plugins(), "bad")
	_ = os.MkdirAll(bad, 0o755)
	_ = os.WriteFile(filepath.Join(bad, ManifestFile),
		[]byte("name = \"bad\"\nversion = \"1.0.0\"\npermissions = [\"note:exec\"]\n"), 0o644)
	// not a plugin: no manifest
	_ = os.MkdirAll(filepath.Join(layout.Plugins(), "drafts"), 0o755)

	store := newFakeStore()
	bus := platform.NewBus("work", testLogger())
	mgr := NewManager(testLogger(), func(string) NoteStore { return store })
	defer mgr.Close()
	if err := mgr.Ensure("work", layout, bus); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	infos, err := mgr.List("work")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]Info{}
	for _, in := range infos {
		byName[in.Name] = in
	}
	if len(byName) != 2 {
		t.Fatalf("list = %+v, want off+bad (drafts ignored)", infos)
	}
	if byName["off"].Enabled || byName["off"].Loaded {
		t.Errorf("disabled plugin = %+v", byName["off"])
	}
	if byName["bad"].Problem == "" {
		t.Errorf("broken plugin has no problem: %+v", byName["bad"])
	}
}
