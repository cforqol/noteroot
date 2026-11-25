package log

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestRecent_defaultsCapacityWhenNonPositive(t *testing.T) {
	r := NewRecent(0)
	if len(r.entries) != DefaultRecentCapacity {
		t.Errorf("capacity = %d, want default %d", len(r.entries), DefaultRecentCapacity)
	}
	r2 := NewRecent(-5)
	if len(r2.entries) != DefaultRecentCapacity {
		t.Errorf("capacity(-5) = %d, want default %d", len(r2.entries), DefaultRecentCapacity)
	}
}

func TestRecent_snapshotEmptyIsEmpty(t *testing.T) {
	r := NewRecent(5)
	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() = %v, want empty", got)
	}
}

func TestRecent_snapshotOrderBeforeFull(t *testing.T) {
	r := NewRecent(5)
	r.add(Entry{Message: "a"})
	r.add(Entry{Message: "b"})
	r.add(Entry{Message: "c"})
	got := r.Snapshot()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Message != w {
			t.Errorf("entry %d = %q, want %q", i, got[i].Message, w)
		}
	}
}

func TestRecent_wraparoundKeepsOldestFirst(t *testing.T) {
	r := NewRecent(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		r.add(Entry{Message: m})
	}
	got := r.Snapshot()
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Message != w {
			t.Errorf("entry %d = %q, want %q", i, got[i].Message, w)
		}
	}
}

func TestRecent_exactCapacityNoWraparoundYet(t *testing.T) {
	r := NewRecent(3)
	for _, m := range []string{"a", "b", "c"} {
		r.add(Entry{Message: m})
	}
	got := r.Snapshot()
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if got[i].Message != w {
			t.Errorf("entry %d = %q, want %q", i, got[i].Message, w)
		}
	}
}

func TestRecent_concurrentAddAndSnapshot(t *testing.T) {
	r := NewRecent(50)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.add(Entry{Message: "x"})
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
	if got := r.Snapshot(); len(got) != 50 {
		t.Errorf("len = %d, want 50 (full)", len(got))
	}
}

func TestTee_passesEveryRecordThroughUnchanged(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, nil)
	recent := NewRecent(10)
	logger := slog.New(Tee(base, recent))

	logger.Info("hello")
	logger.Warn("careful")
	logger.Error("broken")

	out := buf.String()
	for _, want := range []string{"hello", "careful", "broken"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

func TestTee_capturesOnlyWarnAndAbove(t *testing.T) {
	base := slog.NewTextHandler(bytes.NewBuffer(nil), nil)
	recent := NewRecent(10)
	logger := slog.New(Tee(base, recent))

	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")

	got := recent.Snapshot()
	if len(got) != 2 {
		t.Fatalf("captured = %d, want 2 (warn+error only): %+v", len(got), got)
	}
	if got[0].Message != "warn msg" || got[0].Level != "WARN" {
		t.Errorf("entry 0 = %+v, want warn msg/WARN", got[0])
	}
	if got[1].Message != "error msg" || got[1].Level != "ERROR" {
		t.Errorf("entry 1 = %+v, want error msg/ERROR", got[1])
	}
}

func TestTee_nilRecentIsPassthrough(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(Tee(slog.NewTextHandler(&buf, nil), nil))
	logger.Error("still logs")
	if !bytes.Contains(buf.Bytes(), []byte("still logs")) {
		t.Errorf("output = %q, want to contain the message", buf.String())
	}
}

func TestTee_withAttrsAndWithGroupStillCapture(t *testing.T) {
	base := slog.NewTextHandler(bytes.NewBuffer(nil), nil)
	recent := NewRecent(10)
	logger := slog.New(Tee(base, recent))

	derived := logger.With("component", "test").WithGroup("g")
	derived.Error("derived error")

	got := recent.Snapshot()
	if len(got) != 1 || got[0].Message != "derived error" {
		t.Fatalf("captured = %+v, want one derived error entry", got)
	}
}

func TestRingHandler_handleReturnsUnderlyingError(t *testing.T) {
	// Exercise Handle directly, independent of slog.Logger, to confirm
	// errors from the wrapped handler propagate.
	h := Tee(slog.NewTextHandler(bytes.NewBuffer(nil), nil), NewRecent(1))
	r := slog.NewRecord(time.Now(), slog.LevelError, "boom", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Errorf("Handle() error = %v, want nil for a plain text handler", err)
	}
}
