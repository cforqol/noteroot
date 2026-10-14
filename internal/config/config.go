package config

import (
	"fmt"
	"os"
	"strings"
)

type AuthMode string

const (
	AuthForward AuthMode = "forward"
	AuthNone    AuthMode = "none"
)

type Config struct {
	DataDir    string
	ListenAddr string
	LogLevel   string
	AuthMode   AuthMode
}

func Load() (Config, error) {
	c := Config{
		DataDir:    env("NOTEROOT_DATA_DIR", defaultDataDir()),
		ListenAddr: env("NOTEROOT_LISTEN", ":8080"),
		LogLevel:   env("NOTEROOT_LOG_LEVEL", "info"),
		AuthMode:   AuthMode(env("NOTEROOT_AUTH_MODE", string(AuthForward))),
	}
	if !strings.HasPrefix(c.DataDir, "/") {
		return c, fmt.Errorf("NOTEROOT_DATA_DIR must be absolute, got %q", c.DataDir)
	}
	switch c.AuthMode {
	case AuthForward, AuthNone:
	default:
		return c, fmt.Errorf("unknown NOTEROOT_AUTH_MODE %q", c.AuthMode)
	}
	return c, nil
}

func defaultDataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x + "/noteroot"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/share/noteroot"
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}
