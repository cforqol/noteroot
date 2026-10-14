package httpapi

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, authMode config.AuthMode, reg *notebooks.Registry) *Server {
	t.Helper()
	if reg == nil {
		var problems []error
		reg, problems = notebooks.Open(t.TempDir())
		if len(problems) != 0 {
			t.Fatalf("notebooks.Open problems: %v", problems)
		}
	}
	cfg := config.Config{
		DataDir:    filepath.Join(t.TempDir(), "data"),
		ListenAddr: ":0",
		LogLevel:   "info",
		AuthMode:   authMode,
	}
	return New(cfg, newTestLogger(), reg)
}
