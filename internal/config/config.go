package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type AuthMode string

const (
	AuthForward AuthMode = "forward"
	AuthNone    AuthMode = "none"
)

type Config struct {
	DataDir      string
	ListenAddr   string
	LogLevel     string
	AuthMode     AuthMode
	PublicOrigin string

	// Object storage (stage 7). When S3Endpoint is set, blobs are stored
	// in S3-compatible storage instead of the local filesystem.
	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string
	S3PathStyle bool
	// S3PresignTTL is how long presigned upload/read URLs live.
	// 0 = default (15 minutes).
	S3PresignTTL string

	// Stage 8: housekeeping and limits.

	// QuotaBytes is the per-notebook storage limit in bytes. 0 = unlimited.
	QuotaBytes int64
	// GCInterval is how often the background GC sweep runs (e.g. "24h").
	GCInterval string
}

// S3Enabled reports whether object storage is configured.
func (c Config) S3Enabled() bool {
	return c.S3Endpoint != "" && c.S3Bucket != "" && c.S3Region != "" &&
		c.S3AccessKey != "" && c.S3SecretKey != ""
}

func Load() (Config, error) {
	c := Config{
		DataDir:      env("NOTEROOT_DATA_DIR", defaultDataDir()),
		ListenAddr:   env("NOTEROOT_LISTEN", ":8080"),
		LogLevel:     env("NOTEROOT_LOG_LEVEL", "info"),
		AuthMode:     AuthMode(env("NOTEROOT_AUTH_MODE", string(AuthForward))),
		PublicOrigin: env("NOTEROOT_PUBLIC_ORIGIN", ""),

		S3Endpoint:  env("NOTEROOT_S3_ENDPOINT", ""),
		S3Bucket:    env("NOTEROOT_S3_BUCKET", ""),
		S3Region:    env("NOTEROOT_S3_REGION", ""),
		S3AccessKey: env("NOTEROOT_S3_ACCESS_KEY", ""),
		S3SecretKey: env("NOTEROOT_S3_SECRET_KEY", ""),
		S3PathStyle:  envBool("NOTEROOT_S3_PATH_STYLE", false),
		S3PresignTTL: env("NOTEROOT_S3_PRESIGN_TTL", ""),

		QuotaBytes: envInt64("NOTEROOT_QUOTA_BYTES", 0),
		GCInterval: env("NOTEROOT_GC_INTERVAL", "24h"),
	}
	if !strings.HasPrefix(c.DataDir, "/") {
		return c, fmt.Errorf("NOTEROOT_DATA_DIR must be absolute, got %q", c.DataDir)
	}
	switch c.AuthMode {
	case AuthForward, AuthNone:
	default:
		return c, fmt.Errorf("unknown NOTEROOT_AUTH_MODE %q", c.AuthMode)
	}
	// S3 config must be all-or-nothing.
	s3Fields := map[string]string{
		"NOTEROOT_S3_ENDPOINT":  c.S3Endpoint,
		"NOTEROOT_S3_BUCKET":    c.S3Bucket,
		"NOTEROOT_S3_REGION":    c.S3Region,
		"NOTEROOT_S3_ACCESS_KEY": c.S3AccessKey,
		"NOTEROOT_S3_SECRET_KEY": c.S3SecretKey,
	}
	set := 0
	for _, v := range s3Fields {
		if v != "" {
			set++
		}
	}
	if set > 0 && set != len(s3Fields) {
		return c, fmt.Errorf("NOTEROOT_S3_* must be set together (got %d of %d)", set, len(s3Fields))
	}
	return c, nil
}

func envInt64(k string, def int64) int64 {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
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
