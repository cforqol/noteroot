package notebooks

import (
	"sync"
	"testing"
	"time"
)

func TestBreakers_newDefaultsZeroValues(t *testing.T) {
	bs := NewBreakers(0, 0)
	if bs.threshold != DefaultBreakerThreshold {
		t.Errorf("threshold = %d, want default %d", bs.threshold, DefaultBreakerThreshold)
	}
	if bs.cooldown != DefaultBreakerCooldown {
		t.Errorf("cooldown = %v, want default %v", bs.cooldown, DefaultBreakerCooldown)
	}
	if bs.Cooldown() != DefaultBreakerCooldown {
		t.Errorf("Cooldown() = %v, want default %v", bs.Cooldown(), DefaultBreakerCooldown)
	}
}

func TestBreakers_closedUntilThreshold(t *testing.T) {
	bs := NewBreakers(3, time.Minute)
	for i := 0; i < 2; i++ {
		if !bs.Allow("a") {
			t.Fatalf("iteration %d: breaker open before threshold", i)
		}
		bs.Failure("a", "boom")
	}
	if !bs.Allow("a") {
		t.Fatal("breaker open after only 2 of 3 strikes")
	}
}

func TestBreakers_tripsAtThresholdAndBlocks(t *testing.T) {
	bs := NewBreakers(3, time.Minute)
	for i := 0; i < 3; i++ {
		bs.Failure("a", "boom")
	}
	if bs.Allow("a") {
		t.Fatal("breaker should be open after reaching threshold")
	}
	st := bs.State("a")
	if !st.Open {
		t.Error("State().Open = false, want true")
	}
	if st.Strikes != 3 {
		t.Errorf("Strikes = %d, want 3", st.Strikes)
	}
	if st.Trips != 1 {
		t.Errorf("Trips = %d, want 1", st.Trips)
	}
	if st.LastError != "boom" {
		t.Errorf("LastError = %q, want %q", st.LastError, "boom")
	}
}

func TestBreakers_successResetsStrikesBeforeTrip(t *testing.T) {
	bs := NewBreakers(3, time.Minute)
	bs.Failure("a", "boom")
	bs.Failure("a", "boom")
	bs.Success("a")
	if st := bs.State("a"); st.Strikes != 0 {
		t.Errorf("Strikes = %d, want 0 after Success", st.Strikes)
	}
	// Two more strikes should not trip it: Success reset the count.
	bs.Failure("a", "boom")
	bs.Failure("a", "boom")
	if !bs.Allow("a") {
		t.Fatal("breaker open after only 2 strikes post-reset")
	}
}

func TestBreakers_halfOpensAfterCooldownAndClosesOnSuccess(t *testing.T) {
	bs := NewBreakers(1, 10*time.Millisecond)
	bs.Failure("a", "boom")
	if bs.Allow("a") {
		t.Fatal("breaker should be open immediately after tripping")
	}
	time.Sleep(20 * time.Millisecond)
	if !bs.Allow("a") {
		t.Fatal("breaker should half-open and allow a probe after cooldown")
	}
	bs.Success("a")
	if st := bs.State("a"); st.Open {
		t.Error("breaker still reports open after a successful probe")
	}
}

func TestBreakers_halfOpenProbeFailureReopens(t *testing.T) {
	bs := NewBreakers(1, 10*time.Millisecond)
	bs.Failure("a", "boom")
	time.Sleep(20 * time.Millisecond)
	if !bs.Allow("a") {
		t.Fatal("expected half-open probe to be allowed")
	}
	bs.Failure("a", "boom again")
	if bs.Allow("a") {
		t.Fatal("breaker should reopen when the probe itself fails")
	}
}

func TestBreakers_reset(t *testing.T) {
	bs := NewBreakers(1, time.Minute)
	bs.Failure("a", "boom")
	if bs.Allow("a") {
		t.Fatal("breaker should be open")
	}
	bs.Reset("a")
	if !bs.Allow("a") {
		t.Fatal("breaker should be closed after Reset")
	}
	if st := bs.State("a"); st.Strikes != 0 || st.Open {
		t.Errorf("State after Reset = %+v, want zero strikes and closed", st)
	}
}

func TestBreakers_stateIsReadOnly(t *testing.T) {
	bs := NewBreakers(1, 5*time.Millisecond)
	bs.Failure("a", "boom")
	time.Sleep(10 * time.Millisecond)
	// Reading state after the cooldown has elapsed must not itself
	// half-open the breaker for the next real request.
	if st := bs.State("a"); st.Open {
		t.Error("State() reports open after cooldown elapsed, want closed (read-only)")
	}
	if !bs.Allow("a") {
		t.Fatal("Allow should still grant the half-open probe after a State() read")
	}
}

func TestBreakers_notebooksAreIndependent(t *testing.T) {
	bs := NewBreakers(1, time.Minute)
	bs.Failure("broken", "boom")
	if bs.Allow("broken") {
		t.Fatal("broken notebook's breaker should be open")
	}
	if !bs.Allow("healthy") {
		t.Fatal("an unrelated notebook must not be affected by another's breaker")
	}
}

func TestBreakers_unknownSlugStateIsZeroValue(t *testing.T) {
	bs := NewBreakers(1, time.Minute)
	st := bs.State("never-touched")
	if st.Open || st.Strikes != 0 || st.Trips != 0 {
		t.Errorf("State for untouched slug = %+v, want zero value", st)
	}
}

func TestBreakers_snapshotListsOnlyTouchedSlugs(t *testing.T) {
	bs := NewBreakers(5, time.Minute)
	bs.Failure("a", "boom")
	snap := bs.Snapshot()
	if _, ok := snap["a"]; !ok {
		t.Error("Snapshot missing slug with recorded strikes")
	}
	if _, ok := snap["never-touched"]; ok {
		t.Error("Snapshot should not include slugs with no recorded breaker")
	}
}

func TestBreakers_concurrentAccess(t *testing.T) {
	bs := NewBreakers(50, 5*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				bs.Allow("shared")
				bs.Failure("shared", "boom")
				bs.Success("shared")
				_ = bs.State("shared")
				_ = bs.Snapshot()
			}
		}()
	}
	wg.Wait()
}
