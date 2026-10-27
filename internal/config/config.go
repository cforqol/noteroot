package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	// S3SSE is the server-side encryption mode: "" (none), "AES256", or "aws:kms".
	S3SSE string
	// S3SSEKeyID is the KMS key ID used when S3SSE is "aws:kms". Empty = AWS-managed key.
	S3SSEKeyID string

	// Karakeep integration. When KarakeepURL and KarakeepKey are both set,
	// the proxy endpoint is active and the editor archives pasted URLs automatically.
	KarakeepURL string
	KarakeepKey string

	// Slink integration. When SlinkURL and SlinkKey are both set, image pastes
	// and drops are uploaded to Slink instead of the local/S3 resource store.
	SlinkURL string
	SlinkKey string

	// BackupSchedule is the interval between automatic backups (e.g. "24h").
	// Empty or "0" disables scheduled backups.
	BackupSchedule string

	// Stage 8: housekeeping and limits.

	// QuotaBytes is the per-notebook storage limit in bytes. 0 = unlimited.
	QuotaBytes int64
	// GCInterval is how often the background GC sweep runs (e.g. "24h").
	GCInterval string
	// ReadTimeout is the maximum duration for reading the entire request, including the body.
	// Defaults to "10s" if not set.
	ReadTimeout string
	// WriteTimeout is the maximum duration for writing the response.
	// Defaults to "10s" if not set.
	WriteTimeout string
	// IdleTimeout is the maximum amount of time to wait for the next request when keep-alive is enabled.
	// Defaults to "60s" if not set.
	IdleTimeout string
	// MaxHeaderBytes is the maximum size of the request headers.
	// Defaults to 1 << 16 (65536) if not set.
	MaxHeaderBytes int
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

		S3Endpoint:   env("NOTEROOT_S3_ENDPOINT", ""),
		S3Bucket:     env("NOTEROOT_S3_BUCKET", ""),
		S3Region:     env("NOTEROOT_S3_REGION", ""),
		S3AccessKey:  env("NOTEROOT_S3_ACCESS_KEY", ""),
		S3SecretKey:  env("NOTEROOT_S3_SECRET_KEY", ""),
		S3PathStyle:  envBool("NOTEROOT_S3_PATH_STYLE", false),
		S3PresignTTL: env("NOTEROOT_S3_PRESIGN_TTL", ""),
		S3SSE:        env("NOTEROOT_S3_SSE", ""),
		S3SSEKeyID:   env("NOTEROOT_S3_SSE_KEY_ID", ""),

		KarakeepURL: env("NOTEROOT_KARAKEEP_URL", ""),
		KarakeepKey: env("NOTEROOT_KARAKEEP_KEY", ""),

		SlinkURL: env("NOTEROOT_SLINK_URL", ""),
		SlinkKey: env("NOTEROOT_SLINK_KEY", ""),

		BackupSchedule: env("NOTEROOT_BACKUP_SCHEDULE", ""),

		QuotaBytes: envInt64("NOTEROOT_QUOTA_BYTES", 0),
		GCInterval: env("NOTEROOT_GC_INTERVAL", "24h"),

		ReadTimeout:    env("NOTEROOT_READ_TIMEOUT", "10s"),
		WriteTimeout:   env("NOTEROOT_WRITE_TIMEOUT", "10s"),
		IdleTimeout:    env("NOTEROOT_IDLE_TIMEOUT", "60s"),
		MaxHeaderBytes: envInt("NOTEROOT_MAX_HEADER_BYTES", 1<<16),
	}
	if !strings.HasPrefix(c.DataDir, "/") {
		return c, fmt.Errorf("NOTEROOT_DATA_DIR must be absolute, got %q", c.DataDir)
	}
	switch c.AuthMode {
	case AuthForward, AuthNone:
	default:
		return c, fmt.Errorf("unknown NOTEROOT_AUTH_MODE %q", c.AuthMode)
	}
	if c.QuotaBytes < 0 {
		return c, fmt.Errorf("NOTEROOT_QUOTA_BYTES must be >= 0, got %d", c.QuotaBytes)
	}
	if c.GCInterval != "" {
		if _, err := time.ParseDuration(c.GCInterval); err != nil {
			return c, fmt.Errorf("bad NOTEROOT_GC_INTERVAL %q: %w", c.GCInterval, err)
		}
	}
	if c.S3PresignTTL != "" {
		d, err := time.ParseDuration(c.S3PresignTTL)
		if err != nil {
			return c, fmt.Errorf("bad NOTEROOT_S3_PRESIGN_TTL %q: %w", c.S3PresignTTL, err)
		}
		if d <= 0 {
			return c, fmt.Errorf("NOTEROOT_S3_PRESIGN_TTL must be positive, got %q", c.S3PresignTTL)
		}
	}
	switch c.S3SSE {
	case "", "AES256", "aws:kms":
	default:
		return c, fmt.Errorf("NOTEROOT_S3_SSE must be AES256 or aws:kms, got %q", c.S3SSE)
	}
	if c.BackupSchedule != "" && c.BackupSchedule != "0" {
		if _, err := time.ParseDuration(c.BackupSchedule); err != nil {
			return c, fmt.Errorf("bad NOTEROOT_BACKUP_SCHEDULE %q: %w", c.BackupSchedule, err)
		}
	}
	if (c.KarakeepURL == "") != (c.KarakeepKey == "") {
		return c, fmt.Errorf("NOTEROOT_KARAKEEP_URL and NOTEROOT_KARAKEEP_KEY must be set together")
	}
	if (c.SlinkURL == "") != (c.SlinkKey == "") {
		return c, fmt.Errorf("NOTEROOT_SLINK_URL and NOTEROOT_SLINK_KEY must be set together")
	}
	// S3 config must be all-or-nothing.
	s3Fields := map[string]string{
		"NOTEROOT_S3_ENDPOINT":   c.S3Endpoint,
		"NOTEROOT_S3_BUCKET":     c.S3Bucket,
		"NOTEROOT_S3_REGION":     c.S3Region,
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

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
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
