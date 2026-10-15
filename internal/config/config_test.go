package config

import (
	"os"
	"testing"
)

func setenv(t *testing.T, key, val string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		}
	})
}

func TestLoad_defaults(t *testing.T) {
	for _, k := range []string{"NOTEROOT_DATA_DIR", "NOTEROOT_LISTEN", "NOTEROOT_LOG_LEVEL", "NOTEROOT_AUTH_MODE"} {
		unsetenv(t, k)
	}
	setenv(t, "XDG_DATA_HOME", "/tmp/xdg")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/tmp/xdg/noteroot" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/tmp/xdg/noteroot")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.AuthMode != AuthForward {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthForward)
	}
}

func TestLoad_overrides(t *testing.T) {
	setenv(t, "NOTEROOT_DATA_DIR", "/data/notes")
	setenv(t, "NOTEROOT_LISTEN", ":9090")
	setenv(t, "NOTEROOT_LOG_LEVEL", "debug")
	setenv(t, "NOTEROOT_AUTH_MODE", "none")
	setenv(t, "NOTEROOT_PUBLIC_ORIGIN", "https://notes.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/data/notes" {
		t.Errorf("DataDir = %q, want /data/notes", cfg.DataDir)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.ListenAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.AuthMode != AuthNone {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthNone)
	}
	if cfg.PublicOrigin != "https://notes.example.com" {
		t.Errorf("PublicOrigin = %q", cfg.PublicOrigin)
	}
}

func TestLoad_relativeDataDir(t *testing.T) {
	setenv(t, "NOTEROOT_DATA_DIR", "relative/path")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for relative NOTEROOT_DATA_DIR")
	}
}

func TestLoad_invalidAuthMode(t *testing.T) {
	setenv(t, "NOTEROOT_DATA_DIR", "/data/notes")
	setenv(t, "NOTEROOT_AUTH_MODE", "magic")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid NOTEROOT_AUTH_MODE")
	}
}

func TestLoad_emptyEnvFallsToDefault(t *testing.T) {
	setenv(t, "NOTEROOT_DATA_DIR", "/data/notes")
	setenv(t, "NOTEROOT_LISTEN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080 (empty env should fall back to default)", cfg.ListenAddr)
	}
}

func TestDefaultDataDir_XDG(t *testing.T) {
	setenv(t, "XDG_DATA_HOME", "/xdg")
	if got := defaultDataDir(); got != "/xdg/noteroot" {
		t.Errorf("got %q, want %q", got, "/xdg/noteroot")
	}
}

func TestDefaultDataDir_home(t *testing.T) {
	unsetenv(t, "XDG_DATA_HOME")
	home, _ := os.UserHomeDir()
	want := home + "/.local/share/noteroot"
	if got := defaultDataDir(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnv_present(t *testing.T) {
	setenv(t, "_NOTEROOT_TEST_KEY", "hello")
	if got := env("_NOTEROOT_TEST_KEY", "default"); got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestEnv_missing(t *testing.T) {
	unsetenv(t, "_NOTEROOT_TEST_KEY")
	if got := env("_NOTEROOT_TEST_KEY", "default"); got != "default" {
		t.Errorf("got %q, want default", got)
	}
}

func TestEnv_emptyUsesDefault(t *testing.T) {
	setenv(t, "_NOTEROOT_TEST_KEY", "")
	if got := env("_NOTEROOT_TEST_KEY", "default"); got != "default" {
		t.Errorf("got %q, want default (empty string should use default)", got)
	}
}
