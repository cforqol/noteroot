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

// --- S3 config (stage 7) ---

func s3EnvKeys() []string {
	return []string{
		"NOTEROOT_S3_ENDPOINT", "NOTEROOT_S3_BUCKET", "NOTEROOT_S3_REGION",
		"NOTEROOT_S3_ACCESS_KEY", "NOTEROOT_S3_SECRET_KEY",
		"NOTEROOT_S3_PATH_STYLE", "NOTEROOT_S3_PRESIGN_TTL",
	}
}

func TestLoad_s3DisabledByDefault(t *testing.T) {
	for _, k := range s3EnvKeys() {
		unsetenv(t, k)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3Enabled() {
		t.Error("S3 should be disabled by default")
	}
}

func TestLoad_s3AllSet(t *testing.T) {
	setenv(t, "NOTEROOT_S3_ENDPOINT", "https://s3.amazonaws.com")
	setenv(t, "NOTEROOT_S3_BUCKET", "mybucket")
	setenv(t, "NOTEROOT_S3_REGION", "us-east-1")
	setenv(t, "NOTEROOT_S3_ACCESS_KEY", "AKID")
	setenv(t, "NOTEROOT_S3_SECRET_KEY", "SECRET")
	setenv(t, "NOTEROOT_S3_PATH_STYLE", "true")
	setenv(t, "NOTEROOT_S3_PRESIGN_TTL", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.S3Enabled() {
		t.Error("S3 should be enabled")
	}
	if cfg.S3Endpoint != "https://s3.amazonaws.com" {
		t.Errorf("S3Endpoint = %q", cfg.S3Endpoint)
	}
	if !cfg.S3PathStyle {
		t.Error("S3PathStyle = false, want true")
	}
	if cfg.S3PresignTTL != "30m" {
		t.Errorf("S3PresignTTL = %q", cfg.S3PresignTTL)
	}
}

func TestLoad_s3PartialRejected(t *testing.T) {
	// Set only 2 of 5 required fields.
	setenv(t, "NOTEROOT_S3_ENDPOINT", "https://s3.amazonaws.com")
	setenv(t, "NOTEROOT_S3_BUCKET", "mybucket")
	unsetenv(t, "NOTEROOT_S3_REGION")
	unsetenv(t, "NOTEROOT_S3_ACCESS_KEY")
	unsetenv(t, "NOTEROOT_S3_SECRET_KEY")

	_, err := Load()
	if err == nil {
		t.Fatal("want error for partial S3 config")
	}
}

func TestS3Enabled_partialFalse(t *testing.T) {
	c := Config{S3Endpoint: "x", S3Bucket: "y"}
	if c.S3Enabled() {
		t.Error("partial config should not be enabled")
	}
	c = Config{
		S3Endpoint: "x", S3Bucket: "y", S3Region: "z",
		S3AccessKey: "a", S3SecretKey: "b",
	}
	if !c.S3Enabled() {
		t.Error("full config should be enabled")
	}
}

func TestLoad_stage8Defaults(t *testing.T) {
	for _, k := range []string{"NOTEROOT_QUOTA_BYTES", "NOTEROOT_GC_INTERVAL"} {
		unsetenv(t, k)
	}
	setenv(t, "NOTEROOT_DATA_DIR", "/tmp/stage8test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.QuotaBytes != 0 {
		t.Errorf("QuotaBytes default = %d, want 0", cfg.QuotaBytes)
	}
	if cfg.GCInterval != "24h" {
		t.Errorf("GCInterval default = %q, want 24h", cfg.GCInterval)
	}
}

func TestLoad_stage8Overrides(t *testing.T) {
	setenv(t, "NOTEROOT_DATA_DIR", "/tmp/stage8test")
	setenv(t, "NOTEROOT_QUOTA_BYTES", "1073741824")
	setenv(t, "NOTEROOT_GC_INTERVAL", "6h")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.QuotaBytes != 1073741824 {
		t.Errorf("QuotaBytes = %d, want 1073741824", cfg.QuotaBytes)
	}
	if cfg.GCInterval != "6h" {
		t.Errorf("GCInterval = %q, want 6h", cfg.GCInterval)
	}
}

func TestEnvInt64(t *testing.T) {
	setenv(t, "TEST_INT64", "99")
	if got := envInt64("TEST_INT64", 0); got != 99 {
		t.Errorf("envInt64 = %d, want 99", got)
	}
	setenv(t, "TEST_INT64", "notanumber")
	if got := envInt64("TEST_INT64", 42); got != 42 {
		t.Errorf("envInt64(bad) = %d, want default 42", got)
	}
	unsetenv(t, "TEST_INT64")
	if got := envInt64("TEST_INT64", 7); got != 7 {
		t.Error("missing env should use default")
	}
}

func TestEnvBool(t *testing.T) {
	cases := map[string]bool{
		"true": true, "1": true, "yes": true, "on": true,
		"false": false, "0": false, "no": false, "off": false,
	}
	for in, want := range cases {
		setenv(t, "TEST_BOOL", in)
		if got := envBool("TEST_BOOL", false); got != want {
			t.Errorf("envBool(%q) = %v, want %v", in, got, want)
		}
	}
	unsetenv(t, "TEST_BOOL")
	if got := envBool("TEST_BOOL", true); !got {
		t.Error("missing env should use default")
	}
}
