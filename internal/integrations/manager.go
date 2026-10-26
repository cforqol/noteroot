package integrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
	"noteroot/internal/plugins"
)

// Secret namespaces in platform.db: the config files stay git-safe,
// the HMAC material never leaves the API surface.
const (
	secretNamespaceOut = "integrations.out"
	secretNamespaceIn  = "integrations.in"
)

// Manager owns every notebook's integration runtime, mirroring the
// plugin manager: discovery, load, one bus subscription that fans
// events out to outbound queues, secret rotation in place, reload,
// shutdown. No method can fail boot — integrations degrade.
type Manager struct {
	log      *slog.Logger
	newStore func(slug string) plugins.NoteStore

	mu     sync.Mutex
	nbs    map[string]*notebookRuntime
	closed bool
}

type notebookRuntime struct {
	slug     string
	db       *platform.DB
	foundOut []Found
	foundIn  []Found
	outs     []*Integration
	ins      map[string]*Hook
	unsub    func()
}

func (rt *notebookRuntime) close() {
	if rt.unsub != nil {
		rt.unsub()
	}
	for _, ig := range rt.outs {
		ig.close()
	}
	if rt.db != nil {
		_ = rt.db.Close()
	}
}

// NewManager wires a Manager whose note capability (used by inbound
// hooks) is provided by the caller.
func NewManager(log *slog.Logger, newStore func(slug string) plugins.NoteStore) *Manager {
	return &Manager{log: log, newStore: newStore, nbs: map[string]*notebookRuntime{}}
}

// Ensure loads a notebook's integrations once. Idempotent; call at
// boot and from management endpoints.
func (m *Manager) Ensure(slug string, layout notebooks.Layout, bus *platform.Bus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("integration manager closed")
	}
	if _, ok := m.nbs[slug]; ok {
		return nil
	}

	rt := &notebookRuntime{
		slug:     slug,
		foundOut: scanOut(layout.Integrations()),
		foundIn:  scanIn(layout.Hooks()),
		ins:      map[string]*Hook{},
	}
	if len(rt.foundOut) == 0 && len(rt.foundIn) == 0 {
		// Nothing configured: don't create platform.db.
		m.nbs[slug] = rt
		return nil
	}
	db, _, err := platform.Open(context.Background(), layout.PlatformDB())
	if err != nil {
		return fmt.Errorf("platform open: %w", err)
	}
	rt.db = db
	store := m.newStore(slug)

	for _, f := range rt.foundOut {
		if f.Problem != "" || f.Out == nil {
			if f.Problem != "" {
				m.log.Warn("integration invalid", "notebook", slug,
					"config", f.Name, "problem", f.Problem)
			}
			continue
		}
		if !f.Out.IsEnabled() {
			continue
		}
		ig := newIntegration(*f.Out, m.log)
		sec, ok, err := db.SecretGet(context.Background(), secretNamespaceOut, f.Name)
		if err != nil {
			m.log.Warn("integration secret read failed", "notebook", slug,
				"config", f.Name, "err", err)
			continue
		}
		if ok {
			ig.setSecret(sec)
		}
		// No secret is not a load failure: the integration is listed
		// (has_secret=false) and stays inert until one is set.
		rt.outs = append(rt.outs, ig)
	}

	for _, f := range rt.foundIn {
		if f.Problem != "" || f.In == nil {
			if f.Problem != "" {
				m.log.Warn("hook invalid", "notebook", slug,
					"config", f.Name, "problem", f.Problem)
			}
			continue
		}
		if !f.In.IsEnabled() {
			continue
		}
		h := newHook(*f.In, slug, store, m.log)
		sec, ok, err := db.SecretGet(context.Background(), secretNamespaceIn, f.Name)
		if err != nil {
			m.log.Warn("hook secret read failed", "notebook", slug,
				"config", f.Name, "err", err)
			continue
		}
		if ok {
			h.setSecret(sec)
		}
		rt.ins[f.Name] = h
	}

	if len(rt.outs) > 0 {
		// One subscription fans out to every outbound integration;
		// each enqueues without blocking, the bus handler never does
		// network I/O.
		outs := rt.outs
		rt.unsub = bus.Subscribe(platform.Subscription{
			ID: "integrations:" + slug,
			Handler: func(_ context.Context, ev platform.Event) {
				for _, ig := range outs {
					if ig.cfg.Matches(ev.Type) {
						ig.enqueue(ev)
					}
				}
			},
		})
		for _, ig := range rt.outs {
			go ig.eventLoop()
		}
	}

	m.nbs[slug] = rt
	return nil
}

// Reload tears a notebook's runtime down and rebuilds it from disk.
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

// OutInfo is the listing state of one outbound config.
type OutInfo struct {
	Name      string   `json:"name"`
	URL       string   `json:"url,omitempty"`
	Events    []string `json:"events,omitempty"`
	Enabled   bool     `json:"enabled"`
	HasSecret bool     `json:"has_secret"`
	Breaker   bool     `json:"breaker_open"`
	Problem   string   `json:"problem,omitempty"`
}

// InInfo is the listing state of one inbound config.
type InInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Enabled   bool   `json:"enabled"`
	HasSecret bool   `json:"has_secret"`
	Problem   string `json:"problem,omitempty"`
}

// OutList returns every discovered outbound config with live state.
func (m *Manager) OutList(slug string) ([]OutInfo, error) {
	m.mu.Lock()
	rt, ok := m.nbs[slug]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("notebook %q not loaded", slug)
	}
	loaded := map[string]*Integration{}
	for _, ig := range rt.outs {
		loaded[ig.cfg.Name] = ig
	}
	out := make([]OutInfo, 0, len(rt.foundOut))
	for _, f := range rt.foundOut {
		if f.Problem != "" {
			out = append(out, OutInfo{Name: f.Name, Problem: f.Problem})
			continue
		}
		c := f.Out
		ig, live := loaded[c.Name]
		info := OutInfo{
			Name:    c.Name,
			URL:     c.URL,
			Events:  append([]string{}, c.Events...),
			Enabled: c.IsEnabled() && live,
		}
		if live {
			info.HasSecret = ig.getSecret() != ""
			info.Breaker = ig.breakerOpen()
		}
		out = append(out, info)
	}
	return out, nil
}

// InList returns every discovered inbound config with live state.
func (m *Manager) InList(slug string) ([]InInfo, error) {
	m.mu.Lock()
	rt, ok := m.nbs[slug]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("notebook %q not loaded", slug)
	}
	out := make([]InInfo, 0, len(rt.foundIn))
	for _, f := range rt.foundIn {
		if f.Problem != "" {
			out = append(out, InInfo{Name: f.Name, Problem: f.Problem})
			continue
		}
		c := f.In
		info := InInfo{
			Name:    c.Name,
			Path:    c.Path,
			Mode:    c.Mode,
			Enabled: c.IsEnabled(),
		}
		if info.Mode == "" {
			info.Mode = "append"
		}
		if h, live := rt.ins[c.Name]; live {
			info.Enabled = true
			info.HasSecret = h.getSecret() != ""
		} else {
			info.Enabled = false
		}
		out = append(out, info)
	}
	return out, nil
}

// SetOutSecret stores the HMAC secret and updates the live instance
// in place (no reload needed for a rotation).
func (m *Manager) SetOutSecret(ctx context.Context, slug, name, secret string) error {
	m.mu.Lock()
	rt := m.nbs[slug]
	m.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("notebook %q not loaded", slug)
	}
	if rt.db == nil {
		return ErrIntegrationNotFound
	}
	found := false
	for _, f := range rt.foundOut {
		if f.Name == name && f.Problem == "" {
			found = true
			break
		}
	}
	if !found {
		return ErrIntegrationNotFound
	}
	if err := rt.db.SecretSet(ctx, secretNamespaceOut, name, secret); err != nil {
		return err
	}
	for _, ig := range rt.outs {
		if ig.cfg.Name == name {
			ig.setSecret(secret)
		}
	}
	return nil
}

// SetInSecret stores the HMAC secret for an inbound hook and updates
// the live instance in place.
func (m *Manager) SetInSecret(ctx context.Context, slug, name, secret string) error {
	m.mu.Lock()
	rt := m.nbs[slug]
	m.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("notebook %q not loaded", slug)
	}
	if rt.db == nil {
		return ErrHookNotFound
	}
	found := false
	for _, f := range rt.foundIn {
		if f.Name == name && f.Problem == "" {
			found = true
			break
		}
	}
	if !found {
		return ErrHookNotFound
	}
	if err := rt.db.SecretSet(ctx, secretNamespaceIn, name, secret); err != nil {
		return err
	}
	if h, ok := rt.ins[name]; ok {
		h.setSecret(secret)
	}
	return nil
}

// Handle verifies and applies one inbound webhook delivery.
func (m *Manager) Handle(ctx context.Context, slug, name string, body []byte, sig string, depth int) (string, error) {
	m.mu.Lock()
	rt := m.nbs[slug]
	m.mu.Unlock()
	if rt == nil {
		return "", ErrHookNotFound
	}
	h, ok := rt.ins[name]
	if !ok {
		return "", ErrHookNotFound
	}
	return h.Handle(ctx, body, sig, depth)
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
