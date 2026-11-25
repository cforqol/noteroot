package log

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Entry is one captured log record: just enough to show "what broke
// recently" in a diagnostics bundle, not a full structured-log replay.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Recent is a fixed-size, thread-safe ring buffer of the most recent
// Warn-or-above log records, independent of wherever the logger
// otherwise writes (stdout JSON in production). Oldest entries are
// overwritten once it fills, so memory use is bounded regardless of
// how long the process runs.
type Recent struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	filled  bool
}

// DefaultRecentCapacity is used when NewRecent is given a non-positive
// capacity.
const DefaultRecentCapacity = 200

// NewRecent allocates a ring buffer holding up to capacity entries.
func NewRecent(capacity int) *Recent {
	if capacity <= 0 {
		capacity = DefaultRecentCapacity
	}
	return &Recent{entries: make([]Entry, capacity)}
}

func (r *Recent) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = e
	r.next++
	if r.next == len(r.entries) {
		r.next = 0
		r.filled = true
	}
}

// Snapshot returns the captured entries, oldest first. The returned
// slice is a copy: safe to use after further log records arrive.
func (r *Recent) Snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.filled {
		out := make([]Entry, r.next)
		copy(out, r.entries[:r.next])
		return out
	}
	out := make([]Entry, len(r.entries))
	n := copy(out, r.entries[r.next:])
	copy(out[n:], r.entries[:r.next])
	return out
}

// Tee wraps an existing slog.Handler so every record still reaches it
// completely unchanged, while every Warn-or-above record is also
// captured into recent. A nil recent makes Tee a no-op passthrough.
func Tee(h slog.Handler, recent *Recent) slog.Handler {
	return &ringHandler{Handler: h, recent: recent}
}

type ringHandler struct {
	slog.Handler
	recent *Recent
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.recent != nil && r.Level >= slog.LevelWarn {
		h.recent.add(Entry{Time: r.Time, Level: r.Level.String(), Message: r.Message})
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs/WithGroup must keep teeing into the same Recent: slog
// calls these to derive the handler a .With(...)'d or grouped Logger
// actually logs through, and losing the tee there would silently stop
// capturing most real call sites (anything using a derived logger).
func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ringHandler{Handler: h.Handler.WithAttrs(attrs), recent: h.recent}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{Handler: h.Handler.WithGroup(name), recent: h.recent}
}
