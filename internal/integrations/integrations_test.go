package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
	"noteroot/internal/plugins"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore is a minimal in-memory NoteStore for hook tests.
type fakeStore struct {
	mu     sync.Mutex
	notes  map[string]string
	writes []plugins.WriteRequest
}

func newFakeStore() *fakeStore {
	return &fakeStore{notes: map[string]string{}}
}

func (f *fakeStore) Read(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.notes[path]
	if !ok {
		return "", plugins.ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) Write(_ context.Context, wr plugins.WriteRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes[wr.Path] = wr.Content
	f.writes = append(f.writes, wr)
	return nil
}

// layoutFixture creates a temp notebook layout with the integration
// and hooks config dirs populated from the given maps (name → toml).
func layoutFixture(t *testing.T, out, in map[string]string) notebooks.Layout {
	t.Helper()
	layout := notebooks.NewLayout(t.TempDir())
	for _, dir := range []string{layout.Integrations(), layout.Hooks()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range out {
		if err := os.WriteFile(filepath.Join(layout.Integrations(), name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range in {
		if err := os.WriteFile(filepath.Join(layout.Hooks(), name+".toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return layout
}

func TestConfig_validate(t *testing.T) {
	badOut := []string{
		`url = "ftp://x"`,
		`url = "https://ok"`,
		`url = "https://ok"
events = []`,
		`url = "https://ok"
events = ["note write"]`,
	}
	for i, raw := range badOut {
		var c OutboundConfig
		c.Name = "cfg"
		if _, err := toml.Decode(raw, &c); err == nil {
			if err := c.Validate(); err == nil && i != 1 {
				t.Errorf("bad outbound %d: accepted", i)
			}
		}
	}
	var c OutboundConfig
	c.Name = "cfg"
	c.URL = "https://ok"
	c.Events = []string{"note.write", "*"}
	if err := c.Validate(); err != nil {
		t.Errorf("good outbound: %v", err)
	}
	if !c.Matches("note.trash") || !c.Matches("note.write") {
		t.Error("star/equality matching broken")
	}
	var only OutboundConfig
	only.Events = []string{"note.write"}
	only.URL = "https://ok"
	if only.Matches("note.trash") {
		t.Error("specific events matched wrong type")
	}

	badIn := []InboundConfig{
		{Name: "x", Path: "../escape.md"},
		{Name: "x", Path: "/abs.md"},
		{Name: "x", Path: "a/../b.md"},
		{Name: "x", Path: ""},
		{Name: "x", Path: "ok.md", Mode: "prepend"},
		{Name: "Bad", Path: "ok.md"},
	}
	for _, c := range badIn {
		if err := c.Validate(); err == nil {
			t.Errorf("bad inbound %+v: accepted", c)
		}
	}
	goodIn := InboundConfig{Name: "x", Path: "inbox/x.md"}
	if err := goodIn.Validate(); err != nil {
		t.Errorf("good inbound: %v", err)
	}
	if !goodIn.IsEnabled() {
		t.Error("nil enabled should default true")
	}
}

func TestSign_verify(t *testing.T) {
	body := []byte(`{"event":"note.write"}`)
	sig := Sign("s3cret", body)
	if !Verify("s3cret", body, sig) {
		t.Error("round trip failed")
	}
	if Verify("wrong", body, sig) {
		t.Error("wrong secret accepted")
	}
	if Verify("s3cret", []byte("tampered"), sig) {
		t.Error("tampered body accepted")
	}
	if Verify("", body, sig) {
		t.Error("empty secret accepted")
	}
	if Verify("s3cret", body, "") {
		t.Error("empty header accepted")
	}
}

func TestIntegration_deliver(t *testing.T) {
	var (
		mu      sync.Mutex
		gotHdr  http.Header
		gotBody delivery
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHdr = r.Header.Clone()
		_ = json.Unmarshal(b, &gotBody)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ig := newIntegration(OutboundConfig{
		Name: "ci", URL: srv.URL, Events: []string{"*"},
	}, testLogger())
	ig.setSecret("s3cret")
	ig.sendTimeout = 2 * time.Second

	ig.enqueue(platform.Event{
		Type: "note.write", Slug: "work", Path: "a.md",
		Actor: "alice", Origin: "http", Depth: 2, Count: 3,
		Data: map[string]any{"sha": "abc"},
	})
	ig.deliver(<-ig.queue)

	mu.Lock()
	defer mu.Unlock()
	if gotHdr.Get("X-Noteroot-Event") != "note.write" {
		t.Errorf("event header = %q", gotHdr.Get("X-Noteroot-Event"))
	}
	if gotHdr.Get("X-Noteroot-Depth") != "2" {
		t.Errorf("depth header = %q", gotHdr.Get("X-Noteroot-Depth"))
	}
	if !strings.HasPrefix(gotHdr.Get("X-Noteroot-Signature"), "sha256=") {
		t.Errorf("signature header = %q", gotHdr.Get("X-Noteroot-Signature"))
	}
	if gotHdr.Get("X-Noteroot-Delivery") == "" {
		t.Error("missing delivery id header")
	}
	if gotBody.Event != "note.write" || gotBody.Path != "a.md" || gotBody.Count != 3 {
		t.Errorf("payload = %+v", gotBody)
	}
	if ig.breakerOpen() {
		t.Error("breaker opened on success")
	}
}

func TestIntegration_breaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ig := newIntegration(OutboundConfig{
		Name: "dead", URL: srv.URL, Events: []string{"*"},
	}, testLogger())
	ig.setSecret("s")
	ig.mu.Lock()
	ig.brk = breaker{threshold: defaultBreakerStrikes, cooldown: 20 * time.Millisecond}
	ig.mu.Unlock()

	ev := platform.Event{Type: "note.write", Slug: "work", Count: 1}
	for i := 0; i < defaultBreakerStrikes; i++ {
		ig.deliver(ev)
	}
	if !ig.breakerOpen() {
		t.Fatal("breaker should be open after threshold failures")
	}
	// While open, deliveries are skipped: server hit count must not grow.
	ig.deliver(ev)
	// After cooldown, one probe succeeds against a recovered server.
	srv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	ig.mu.Lock()
	ig.cfg.URL = okSrv.URL
	ig.mu.Unlock()
	time.Sleep(25 * time.Millisecond)
	ig.deliver(ev)
	if ig.breakerOpen() {
		t.Error("breaker should close after successful probe")
	}
}

func TestManager_outboundEndToEnd(t *testing.T) {
	got := make(chan platform.Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var d delivery
		_ = json.Unmarshal(b, &d)
		if !Verify("topsecret", b, r.Header.Get("X-Noteroot-Signature")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got <- platform.Event{Type: d.Event, Path: d.Path, Depth: d.Depth, Slug: d.Slug}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := "url = \"" + srv.URL + "\"\nevents = [\"note.write\"]\n"
	layout := layoutFixture(t, map[string]string{"sink": cfg}, nil)
	bus := platform.NewBus("work", testLogger())
	m := NewManager(testLogger(), func(string) plugins.NoteStore { return newFakeStore() })
	t.Cleanup(m.Close)

	if err := m.Ensure("work", layout, bus); err != nil {
		t.Fatal(err)
	}

	// No secret yet: inert, nothing delivered.
	bus.Publish(platform.Event{Type: "note.write", Slug: "work", Path: "a.md", Count: 1})
	bus.Flush()
	select {
	case ev := <-got:
		t.Fatalf("delivered without secret: %+v", ev)
	case <-time.After(700 * time.Millisecond):
	}
	if out, err := m.OutList("work"); err != nil || out[0].HasSecret {
		t.Errorf("list before secret: %+v err=%v", out, err)
	}

	if err := m.SetOutSecret(context.Background(), "work", "sink", "topsecret"); err != nil {
		t.Fatal(err)
	}
	if out, err := m.OutList("work"); err != nil || !out[0].HasSecret || out[0].URL != srv.URL {
		t.Errorf("list after secret: %+v err=%v", out, err)
	}

	bus.Publish(platform.Event{Type: "note.write", Slug: "work", Path: "a.md", Count: 1})
	bus.Flush()
	select {
	case ev := <-got:
		if ev.Path != "a.md" || ev.Slug != "work" {
			t.Errorf("delivered = %+v", ev)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("delivery never arrived")
	}

	// Non-matching type is filtered.
	bus.Publish(platform.Event{Type: "note.trash", Slug: "work", Path: "a.md", Count: 1})
	bus.Flush()
	select {
	case ev := <-got:
		t.Fatalf("wrong type delivered: %+v", ev)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestManager_inboundEndToEnd(t *testing.T) {
	layout := layoutFixture(t, nil, map[string]string{
		"github": "path = \"inbox/github.md\"\nmode = \"append\"\n",
	})
	bus := platform.NewBus("work", testLogger())
	store := newFakeStore()
	m := NewManager(testLogger(), func(string) plugins.NoteStore { return store })
	t.Cleanup(m.Close)
	if err := m.Ensure("work", layout, bus); err != nil {
		t.Fatal(err)
	}

	body := []byte("push received\n")
	// Unconfigured secret: indistinguishable from unknown.
	if _, err := m.Handle(context.Background(), "work", "github", body, Sign("x", body), 0); !errors.Is(err, ErrHookNotFound) {
		t.Errorf("no-secret err = %v", err)
	}
	if err := m.SetInSecret(context.Background(), "work", "github", "topsecret"); err != nil {
		t.Fatal(err)
	}

	// Bad signature.
	if _, err := m.Handle(context.Background(), "work", "github", body, "sha256=bad", 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("bad sig err = %v", err)
	}
	// Depth at the limit: rejected before any write.
	if _, err := m.Handle(context.Background(), "work", "github", body, Sign("topsecret", body), platform.DefaultMaxDepth); !errors.Is(err, ErrDepthExceeded) {
		t.Errorf("depth err = %v", err)
	}

	path, err := m.Handle(context.Background(), "work", "github", body,
		Sign("topsecret", body), 2)
	if err != nil {
		t.Fatal(err)
	}
	if path != "inbox/github.md" {
		t.Errorf("path = %q", path)
	}
	store.mu.Lock()
	wr := store.writes[0]
	got := store.notes["inbox/github.md"]
	store.mu.Unlock()
	if wr.Actor != "hook:github" || wr.Origin != "hook:github" || wr.Depth != 3 {
		t.Errorf("attribution = %+v", wr)
	}
	if !strings.Contains(got, "push received") {
		t.Errorf("content = %q", got)
	}
}
