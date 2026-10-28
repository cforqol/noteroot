package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
)

// ErrPluginNotFound is returned when a notebook has no plugin with
// that name (loaded or not).
var ErrPluginNotFound = errors.New("plugin not found")

// Manager owns every notebook's plugin runtime: discovery, load, the
// bus subscription that fans events out to hooks, command dispatch,
// panels, reload, shutdown. One Manager per server; no method here
// can fail boot — plugins degrade, never the notebook.
type Manager struct {
	log      *slog.Logger
	newStore func(slug string) NoteStore

	mu     sync.Mutex
	nbs    map[string]*notebookRuntime
	closed bool
}

type notebookRuntime struct {
	slug    string
	plugins []*Plugin
	found   []Found
	db      *platform.DB
	unsub   func()
}

func (rt *notebookRuntime) close() {
	if rt.unsub != nil {
		rt.unsub()
	}
	for _, p := range rt.plugins {
		p.Close()
	}
	if rt.db != nil {
		_ = rt.db.Close()
	}
}

// NewManager wires a Manager whose note capability is provided by the
// caller (the HTTP layer owns write-path bookkeeping).
func NewManager(log *slog.Logger, newStore func(slug string) NoteStore) *Manager {
	return &Manager{log: log, newStore: newStore, nbs: map[string]*notebookRuntime{}}
}

// Ensure loads a notebook's plugins once: opens platform.db, scans
// the plugins directory, loads every enabled+valid plugin, starts
// their event loops, and subscribes one bus fan-out. Call it at boot
// for every notebook so hooks are live before the first HTTP request,
// and again from management endpoints (idempotent).
func (m *Manager) Ensure(slug string, layout notebooks.Layout, bus *platform.Bus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("plugin manager closed")
	}
	if _, ok := m.nbs[slug]; ok {
		return nil
	}

	rt := &notebookRuntime{slug: slug, found: Scan(layout.Plugins())}
	if len(rt.found) == 0 {
		// No plugin directories: don't even create platform.db.
		m.nbs[slug] = rt
		return nil
	}
	db, _, err := platform.Open(context.Background(), layout.PlatformDB())
	if err != nil {
		return fmt.Errorf("platform open: %w", err)
	}
	rt.db = db
	store := m.newStore(slug)

	for _, f := range rt.found {
		if f.Problem != "" {
			m.log.Warn("plugin invalid", "notebook", slug,
				"dir", f.Dir, "problem", f.Problem)
			continue
		}
		if !f.Manifest.IsEnabled() {
			continue
		}
		grants, err := f.Manifest.Grants()
		if err != nil { // impossible post-Scan; keep the notebook alive
			m.log.Warn("plugin grants", "notebook", slug, "err", err)
			continue
		}
		p := newPlugin(f, grants, slug, store, db, m.log)
		if err := p.Load(); err != nil {
			// Loaded with a recorded problem: listed, inert, retryable
			// via reload. Boot continues either way.
			m.log.Warn("plugin load failed", "notebook", slug,
				"plugin", f.Manifest.Name, "err", err)
		}
		rt.plugins = append(rt.plugins, p)
	}

	if len(rt.plugins) > 0 {
		// One subscription fans out to every plugin; each plugin does
		// its own self-suppression and enqueues without blocking. The
		// bus handler never touches a Lua VM.
		plugins := rt.plugins
		rt.unsub = bus.Subscribe(platform.Subscription{
			ID: "plugins:" + slug,
			Handler: func(_ context.Context, ev platform.Event) {
				for _, p := range plugins {
					p.Enqueue(ev)
				}
			},
		})
		for _, p := range plugins {
			go p.eventLoop()
		}
	}

	m.nbs[slug] = rt
	return nil
}

// Reload tears down a notebook's runtime (unsubscribe, close VMs and
// the platform db) and rebuilds it from disk. The way to pick up an
// edited plugin directory without a restart.
func (m *Manager) Reload(slug string, layout notebooks.Layout, bus *platform.Bus) error {
	m.mu.Lock()
	rt := m.nbs[slug]
	delete(m.nbs, slug)
	m.mu.Unlock()
	if rt != nil {
		rt.close()
	}
	return m.Ensure(slug, layout, bus)
}

// Remove tears a notebook's runtime down and forgets it, for notebooks
// being renamed or deleted. Ensure rebuilds it later if the notebook
// comes back.
func (m *Manager) Remove(slug string) {
	m.mu.Lock()
	rt := m.nbs[slug]
	delete(m.nbs, slug)
	m.mu.Unlock()
	if rt != nil {
		rt.close()
	}
}

// List returns every discovered plugin — valid, invalid, disabled —
// with its live state. Ensure first.
func (m *Manager) List(slug string) ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.nbs[slug]
	if !ok {
		return nil, fmt.Errorf("notebook %q not loaded", slug)
	}
	loaded := map[string]*Plugin{}
	for _, p := range rt.plugins {
		loaded[p.Meta.Name] = p
	}
	out := make([]Info, 0, len(rt.found))
	for _, f := range rt.found {
		if f.Problem != "" {
			out = append(out, Info{
				Name:    f.Manifest.Name,
				Enabled: f.Manifest.IsEnabled(),
				Problem: f.Problem,
			})
			continue
		}
		if p, ok := loaded[f.Manifest.Name]; ok {
			p.mu.Lock()
			out = append(out, p.info())
			p.mu.Unlock()
			continue
		}
		// Disabled: listed, never loaded.
		out = append(out, Info{
			Name:        f.Manifest.Name,
			Version:     f.Manifest.Version,
			Description: f.Manifest.Description,
			Permissions: append([]string{}, f.Manifest.Permissions...),
			Enabled:     false,
		})
	}
	return out, nil
}

// RunCommand dispatches a command to a loaded plugin. The caller
// (HTTP) is already authenticated: commands are human-triggered, so
// they carry no plugin permission check — the capabilities inside
// them do.
func (m *Manager) RunCommand(ctx context.Context, slug, plugin, cmd string, argsJSON []byte) (any, error) {
	m.mu.Lock()
	rt := m.nbs[slug]
	m.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("notebook %q not loaded", slug)
	}
	for _, p := range rt.plugins {
		if p.Meta.Name == plugin {
			return p.RunCommand(ctx, cmd, argsJSON)
		}
	}
	return nil, ErrPluginNotFound
}

// Panels returns the ui.panels namespace: panel name -> JSON spec as
// last written by ui.set_panel.
func (m *Manager) Panels(slug string) (map[string]json.RawMessage, error) {
	m.mu.Lock()
	rt := m.nbs[slug]
	m.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("notebook %q not loaded", slug)
	}
	if rt.db == nil {
		return map[string]json.RawMessage{}, nil
	}
	kv, err := rt.db.KVList(context.Background(), "ui.panels")
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(kv))
	for name, data := range kv {
		out[name] = json.RawMessage(data)
	}
	return out, nil
}

// Close stops every notebook's runtime. Safe to call twice.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	rts := make([]*notebookRuntime, 0, len(m.nbs))
	for _, rt := range m.nbs {
		rts = append(rts, rt)
	}
	m.nbs = map[string]*notebookRuntime{}
	m.mu.Unlock()
	for _, rt := range rts {
		rt.close()
	}
}
