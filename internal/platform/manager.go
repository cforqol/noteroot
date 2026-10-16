package platform

import (
	"log/slog"
	"sync"
)

// Manager hands out one Bus per notebook, created on first use. It
// lives for the process; notebooks get their bus when they first emit.
type Manager struct {
	log *slog.Logger

	mu    sync.Mutex
	buses map[string]*Bus
}

func NewManager(log *slog.Logger) *Manager {
	return &Manager{log: log, buses: map[string]*Bus{}}
}

// Bus returns the notebook's bus, creating it on first call.
func (m *Manager) Bus(slug string) *Bus {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buses[slug]
	if !ok {
		b = NewBus(slug, m.log)
		m.buses[slug] = b
	}
	return b
}

// Close flushes and closes every bus (process shutdown).
func (m *Manager) Close() {
	m.mu.Lock()
	buses := make([]*Bus, 0, len(m.buses))
	for _, b := range m.buses {
		buses = append(buses, b)
	}
	m.mu.Unlock()
	for _, b := range buses {
		b.Close()
	}
}
