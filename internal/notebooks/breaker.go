package notebooks

import (
	"sync"
	"time"
)

// Breaker defaults, used when config supplies a zero value.
const (
	DefaultBreakerThreshold = 5
	DefaultBreakerCooldown  = 30 * time.Second
)

// BreakerState is a read-only snapshot of one notebook's breaker, for
// health/diagnostics reporting. It never mutates the breaker it was
// taken from.
type BreakerState struct {
	Open      bool      `json:"open"`
	Strikes   int       `json:"strikes"`
	Trips     int       `json:"trips"`
	LastTrip  time.Time `json:"last_trip,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

// breaker is a consecutive-failure circuit: strikes accumulate, the
// circuit opens at the threshold, half-opens after a cooldown (one
// probe let through), and a successful probe closes it again. Same
// shape as the existing plugin runtime and integration delivery
// breakers; this one is exported because it is driven from the HTTP
// layer rather than owned entirely within one package.
type breaker struct {
	threshold int
	cooldown  time.Duration

	strikes   int
	isOpen    bool
	opened    time.Time
	trips     int
	lastError string
}

// isCurrentlyOpen reports the breaker's state without mutating it, so
// a diagnostics read never has the side effect of half-opening the
// circuit for the next real request.
func (b *breaker) isCurrentlyOpen() bool {
	return b.isOpen && time.Since(b.opened) < b.cooldown
}

// allow reports whether a request may proceed, transitioning an open
// breaker whose cooldown has elapsed into a half-open probe.
func (b *breaker) allow() bool {
	if !b.isOpen {
		return true
	}
	if time.Since(b.opened) >= b.cooldown {
		b.isOpen = false
		b.strikes = 0
		return true // half-open: let exactly one probe through
	}
	return false
}

func (b *breaker) fail(msg string) {
	b.strikes++
	b.lastError = msg
	if b.strikes >= b.threshold {
		b.isOpen = true
		b.opened = time.Now()
		b.trips++
	}
}

func (b *breaker) ok() {
	b.strikes = 0
	b.isOpen = false
}

func (b *breaker) snapshot() BreakerState {
	return BreakerState{
		Open:      b.isCurrentlyOpen(),
		Strikes:   b.strikes,
		Trips:     b.trips,
		LastTrip:  b.opened,
		LastError: b.lastError,
	}
}

// Breakers tracks one circuit breaker per notebook slug. A notebook
// that fails repeatedly at runtime (bad index, broken vault,
// unresponsive plugin) trips its own breaker and is isolated —
// requests against it fail fast instead of repeating the same slow
// failure — without touching any other notebook or the Registry on
// disk. Entries are created lazily on first use and are purely
// in-memory: a restart, or a rename/delete of the notebook, clears
// them along with the rest of the process's runtime state.
type Breakers struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	byS       map[string]*breaker
}

// NewBreakers builds a Breakers registry. A non-positive threshold or
// cooldown falls back to the package defaults.
func NewBreakers(threshold int, cooldown time.Duration) *Breakers {
	if threshold <= 0 {
		threshold = DefaultBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	return &Breakers{threshold: threshold, cooldown: cooldown, byS: map[string]*breaker{}}
}

// Cooldown is the configured cooldown, for callers that need to
// advertise a Retry-After.
func (bs *Breakers) Cooldown() time.Duration { return bs.cooldown }

func (bs *Breakers) get(slug string) *breaker {
	b, ok := bs.byS[slug]
	if !ok {
		b = &breaker{threshold: bs.threshold, cooldown: bs.cooldown}
		bs.byS[slug] = b
	}
	return b
}

// Allow reports whether a request against slug may proceed. False
// means the breaker is open and the caller must fail fast without
// attempting the operation.
func (bs *Breakers) Allow(slug string) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.get(slug).allow()
}

// Success records that a request against slug completed without a
// breaker-worthy failure, closing the circuit and clearing strikes.
func (bs *Breakers) Success(slug string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.get(slug).ok()
}

// Failure records one strike against slug's breaker. msg is kept as
// the breaker's LastError for diagnostics; the caller is responsible
// for logging with whatever request context it has.
func (bs *Breakers) Failure(slug, msg string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.get(slug).fail(msg)
}

// Reset forces slug's breaker closed, e.g. from an admin action.
func (bs *Breakers) Reset(slug string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if b, ok := bs.byS[slug]; ok {
		b.ok()
	}
}

// State returns a snapshot of slug's breaker. A slug with no recorded
// breaker yet (nothing has failed) reports the zero value: closed, no
// strikes.
func (bs *Breakers) State(slug string) BreakerState {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b, ok := bs.byS[slug]
	if !ok {
		return BreakerState{}
	}
	return b.snapshot()
}

// Snapshot returns every notebook with a recorded breaker (i.e. at
// least one strike), keyed by slug.
func (bs *Breakers) Snapshot() map[string]BreakerState {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	out := make(map[string]BreakerState, len(bs.byS))
	for slug, b := range bs.byS {
		out[slug] = b.snapshot()
	}
	return out
}
