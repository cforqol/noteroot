package log

import (
	"testing"
)

func TestNew_validLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		l := New(level)
		if l == nil {
			t.Errorf("New(%q) returned nil", level)
		}
	}
}

func TestNew_invalidLevelFallsToInfo(t *testing.T) {
	l := New("garbage")
	if l == nil {
		t.Error("New(\"garbage\") returned nil, want non-nil logger")
	}
}
