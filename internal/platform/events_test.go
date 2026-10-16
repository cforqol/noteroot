package platform

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testBus() *Bus {
	return NewBus("work", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBus_coalescesBursts(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	var got []Event
	b.Subscribe(Subscription{
		ID: "a",
		Handler: func(_ context.Context, ev Event) {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		},
	})

	for i := 0; i < 5; i++ {
		b.Publish(Event{Type: "note.write", Path: "a.md", Actor: "alice"})
	}
	b.Publish(Event{Type: "note.write", Path: "b.md", Actor: "alice"})
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("delivered %d events, want 2 (one per path)", len(got))
	}
	byPath := map[string]Event{}
	for _, ev := range got {
		byPath[ev.Path] = ev
	}
	if c := byPath["a.md"].Count; c != 5 {
		t.Errorf("Count for a.md = %d, want 5", c)
	}
	if c := byPath["b.md"].Count; c != 1 {
		t.Errorf("Count for b.md = %d, want 1", c)
	}
}

func TestBus_actorAndOriginSuppression(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	var actorGot, originGot, plainGot int
	b.Subscribe(Subscription{
		ID: "plugin", Actor: "plugin:x", Origin: "plugin:x",
		Handler: func(_ context.Context, _ Event) {
			mu.Lock()
			actorGot++
			mu.Unlock()
		},
	})
	b.Subscribe(Subscription{
		ID: "other",
		Handler: func(_ context.Context, _ Event) {
			mu.Lock()
			plainGot++
			mu.Unlock()
		},
	})
	b.Subscribe(Subscription{
		ID: "webhook", Origin: "integration:a",
		Handler: func(_ context.Context, _ Event) {
			mu.Lock()
			originGot++
			mu.Unlock()
		},
	})

	// User event: delivered to everyone.
	b.Publish(Event{Type: "note.write", Path: "1.md", Actor: "alice", Origin: "http"})
	// Event caused by plugin:x: suppressed only for its own subscription.
	b.Publish(Event{Type: "note.write", Path: "2.md", Actor: "plugin:x", Origin: "plugin:x"})
	// Event caused by integration:a: suppressed only for its own.
	b.Publish(Event{Type: "note.write", Path: "3.md", Actor: "bot", Origin: "integration:a"})
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if actorGot != 2 {
		t.Errorf("plugin subscription got %d, want 2 (its own suppressed, other two delivered)", actorGot)
	}
	if originGot != 2 {
		t.Errorf("webhook subscription got %d, want 2 (its own suppressed, other two delivered)", originGot)
	}
	if plainGot != 3 {
		t.Errorf("plain subscription got %d, want 3", plainGot)
	}
}

func TestBus_depthLimitDrops(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	var got []Event
	b.Subscribe(Subscription{
		ID: "deep",
		Handler: func(_ context.Context, ev Event) {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		},
	})

	b.Publish(Event{Type: "chain", Path: "x", Depth: DefaultMaxDepth})
	b.Publish(Event{Type: "chain", Path: "y", Depth: DefaultMaxDepth + 1})
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Path != "x" {
		t.Fatalf("delivered = %+v, want only depth==max event x", got)
	}
}

func TestBus_followOnAttribution(t *testing.T) {
	b := testBus()
	sub := &Subscription{ID: "plug", Actor: "plugin:p", Origin: "plugin:p"}
	ev := b.FollowOn(sub, Event{Type: "note.write", Path: "a.md", Actor: "alice", Origin: "http", Depth: 0})

	if ev.Depth != 1 {
		t.Errorf("Depth = %d, want 1", ev.Depth)
	}
	if ev.Actor != "plugin:p" || ev.Origin != "plugin:p" {
		t.Errorf("attribution = (%q, %q), want (plugin:p, plugin:p)", ev.Actor, ev.Origin)
	}
	// The follow-on is suppressed back to its own origin… but delivered
	// to a different subscription.
	var mu sync.Mutex
	selfGot, otherGot := 0, 0
	b.Subscribe(Subscription{ID: "s1", Origin: "plugin:p",
		Handler: func(context.Context, Event) { mu.Lock(); selfGot++; mu.Unlock() }})
	b.Subscribe(Subscription{ID: "s2",
		Handler: func(context.Context, Event) { mu.Lock(); otherGot++; mu.Unlock() }})
	b.Publish(ev)
	b.Flush()
	mu.Lock()
	defer mu.Unlock()
	if selfGot != 0 {
		t.Errorf("originating subscription got %d, want 0", selfGot)
	}
	if otherGot != 1 {
		t.Errorf("other subscription got %d, want 1", otherGot)
	}
}

func TestBus_deliveryIsFIFO(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	var got []string
	b.Subscribe(Subscription{
		ID: "ord",
		Handler: func(_ context.Context, ev Event) {
			mu.Lock()
			got = append(got, ev.Path)
			mu.Unlock()
		},
	})
	for _, p := range []string{"1.md", "2.md", "3.md"} {
		b.Publish(Event{Type: "note.write", Path: p})
	}
	b.Flush()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] != "1.md" || got[1] != "2.md" || got[2] != "3.md" {
		t.Fatalf("order = %v, want [1.md 2.md 3.md]", got)
	}
}

func TestBus_unsubscribeStopsDelivery(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	n := 0
	unsub := b.Subscribe(Subscription{
		ID:      "x",
		Handler: func(context.Context, Event) { mu.Lock(); n++; mu.Unlock() },
	})
	b.Publish(Event{Type: "t", Path: "p"})
	b.Flush()
	unsub()
	b.Publish(Event{Type: "t2", Path: "q"})
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
}

func TestBus_slowHandlerTimesOutAndFlushReturns(t *testing.T) {
	b := testBus()
	b.handlerTimeout = 50 * time.Millisecond
	released := make(chan struct{})
	b.Subscribe(Subscription{
		ID: "blocker",
		Handler: func(ctx context.Context, _ Event) {
			select {
			case <-released:
			case <-time.After(3 * time.Second): // leak guard: never hit
			}
		},
	})

	start := time.Now()
	b.Publish(Event{Type: "t", Path: "p"})
	b.Flush() // must return within the handler timeout, not 3s
	elapsed := time.Since(start)
	close(released)

	if elapsed > time.Second {
		t.Errorf("Flush took %v, want < 1s (timeout enforced)", elapsed)
	}
}

func TestBus_closeRejectsPublish(t *testing.T) {
	b := testBus()
	var mu sync.Mutex
	n := 0
	b.Subscribe(Subscription{
		ID:      "x",
		Handler: func(context.Context, Event) { mu.Lock(); n++; mu.Unlock() },
	})
	b.Publish(Event{Type: "t", Path: "p"})
	b.Close()
	b.Publish(Event{Type: "t", Path: "q"})
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("handler ran %d times, want 1 (pre-close event only)", n)
	}
}

func TestManager_perSlugBuses(t *testing.T) {
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a1 := m.Bus("work")
	a2 := m.Bus("work")
	other := m.Bus("home")
	if a1 != a2 {
		t.Error("Bus(\"work\") returned different instances")
	}
	if a1 == other {
		t.Error("different slugs share one bus")
	}
	m.Close()
}
