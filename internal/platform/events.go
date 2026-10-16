package platform

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// Event is one thing that happened in a notebook. The bus coalesces
// bursts of identical (Type, Path) pairs into a single delivery with an
// elevated Count before subscribers ever see them.
type Event struct {
	Type   string // "note.write", "note.delete", "note.rename", …
	Slug   string
	Path   string         // primary subject (note path, "" for whole-notebook)
	Actor  string         // who caused it ("" = system)
	Origin string         // subsystem that caused it ("" = human/HTTP)
	Depth  int            // extension hops so far (loop guard)
	Count  int            // coalesced occurrences; set by the bus
	Data   map[string]any // optional structured detail
}

// Handler receives one delivered event. Handlers run async with a
// timeout; a stuck handler loses only itself, never the save that
// produced the event.
type Handler func(ctx context.Context, ev Event)

// Subscription registers a handler on a bus. Actor and Origin are
// suppression identities: a subscription never receives events it
// caused itself (loop suppression, README "coalescing and loop
// suppression"). ID must be unique per bus; empty is auto-generated.
type Subscription struct {
	ID      string
	Actor   string
	Origin  string
	Handler Handler
}

const (
	// DefaultCoalesceWindow collapses event bursts before delivery.
	DefaultCoalesceWindow = 500 * time.Millisecond
	// DefaultHandlerTimeout bounds one handler run.
	DefaultHandlerTimeout = 5 * time.Second
	// DefaultMaxDepth drops events past this many extension hops.
	DefaultMaxDepth = 5
)

// Bus is a per-notebook event bus: coalesced, actor/origin suppressed,
// depth-limited. Publish never blocks and never fails — delivery is
// timer-driven on a background goroutine, so no extension can block a
// save (the extensibility contract in README).
type Bus struct {
	slug string
	log  *slog.Logger

	window         time.Duration
	handlerTimeout time.Duration
	maxDepth       int

	mu        sync.Mutex
	subs      map[string]*Subscription
	pending   map[string]*Event
	order     []string // pending insertion order (delivery is FIFO)
	timer     *time.Timer
	nextID    int
	closed    bool
	wg        sync.WaitGroup
	deliverMu sync.Mutex // serializes whole flushes so batches keep order
}

// NewBus returns a bus for one notebook with default tuning.
func NewBus(slug string, log *slog.Logger) *Bus {
	return &Bus{
		slug:           slug,
		log:            log,
		window:         DefaultCoalesceWindow,
		handlerTimeout: DefaultHandlerTimeout,
		maxDepth:       DefaultMaxDepth,
		subs:           map[string]*Subscription{},
		pending:        map[string]*Event{},
	}
}

// Subscribe registers a handler and returns an unsubscribe func.
// Subscribing never races with delivery: handlers added mid-flush start
// with the next batch.
func (b *Bus) Subscribe(sub Subscription) (unsubscribe func()) {
	b.mu.Lock()
	if sub.ID == "" {
		b.nextID++
		sub.ID = "sub-" + strconv.Itoa(b.nextID)
	}
	s := sub
	b.subs[sub.ID] = &s
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, sub.ID)
		b.mu.Unlock()
	}
}

// Publish records an event for delivery after the coalesce window.
// It returns immediately; dropping happens only for depth overflow and
// closed buses (both logged at debug), never for load.
func (b *Bus) Publish(ev Event) {
	if ev.Slug == "" {
		ev.Slug = b.slug
	}
	if ev.Count < 1 {
		ev.Count = 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if ev.Depth > b.maxDepth {
		b.log.Debug("event dropped: depth limit",
			"notebook", b.slug, "type", ev.Type, "depth", ev.Depth)
		return
	}
	key := ev.Type + "\x00" + ev.Path
	if cur, ok := b.pending[key]; ok {
		cur.Count += ev.Count
		for k, v := range ev.Data {
			if cur.Data == nil {
				cur.Data = map[string]any{}
			}
			cur.Data[k] = v
		}
	} else {
		e := ev
		b.pending[key] = &e
		b.order = append(b.order, key)
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, b.Flush)
	}
}

// FollowOn returns ev as re-published by sub: one hop deeper and
// attributed to the subscription, so its own suppression rules apply
// and the depth limit eventually stops chains.
func (b *Bus) FollowOn(sub *Subscription, ev Event) Event {
	ev.Depth++
	if sub.Actor != "" {
		ev.Actor = sub.Actor
	}
	if sub.Origin != "" {
		ev.Origin = sub.Origin
	} else if sub.ID != "" && ev.Origin == "" {
		ev.Origin = sub.ID
	}
	if ev.Slug == "" {
		ev.Slug = b.slug
	}
	return ev
}

// Flush delivers all pending events now and waits for their handlers
// to finish (or time out). The coalesce timer calls it; tests and
// shutdown call it directly for determinism. Delivery per subscription
// is sequential in publish order, so handlers observe causality (write
// before delete). Handlers must not call Flush or Close themselves —
// they are already inside a flush.
func (b *Bus) Flush() {
	b.deliverMu.Lock()
	defer b.deliverMu.Unlock()

	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := make([]*Event, 0, len(b.order))
	for _, k := range b.order {
		batch = append(batch, b.pending[k])
	}
	b.pending = map[string]*Event{}
	b.order = nil
	subs := make([]*Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		b.wg.Add(1)
		go func(sub *Subscription) {
			defer b.wg.Done()
			for _, ev := range batch {
				if !deliverable(ev, sub) {
					continue
				}
				e := *ev
				ctx, cancel := context.WithTimeout(context.Background(), b.handlerTimeout)
				done := make(chan struct{})
				go func() {
					defer close(done)
					sub.Handler(ctx, e)
				}()
				select {
				case <-done:
				case <-ctx.Done():
					b.log.Warn("event handler timed out",
						"notebook", b.slug, "sub", sub.ID,
						"type", e.Type, "path", e.Path)
				}
				cancel()
			}
		}(sub)
	}
	b.wg.Wait()
}

// deliverable implements suppression: a subscription never sees events
// it caused (same actor or same origin), and depth is enforced at
// publish time.
func deliverable(ev *Event, sub *Subscription) bool {
	if sub.Actor != "" && ev.Actor == sub.Actor {
		return false
	}
	if sub.Origin != "" && ev.Origin == sub.Origin {
		return false
	}
	return true
}

// Close flushes remaining events and rejects further publishes.
// In-flight handlers are waited out (they are timeout-bounded).
func (b *Bus) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.Flush()
	b.wg.Wait()
}
