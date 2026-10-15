package resources

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testS3Config() S3Config {
	return S3Config{
		Endpoint:  "https://s3.us-east-1.amazonaws.com",
		Bucket:    "mybucket",
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		PathStyle: false,
	}
}

func TestS3Config_enabled(t *testing.T) {
	c := testS3Config()
	if !c.Enabled() {
		t.Error("full config should be enabled")
	}
	c.Bucket = ""
	if c.Enabled() {
		t.Error("missing bucket should disable")
	}
	c = testS3Config()
	c.SecretKey = ""
	if c.Enabled() {
		t.Error("missing secret should disable")
	}
}

func TestNewS3Store_incompleteConfig(t *testing.T) {
	if _, err := NewS3Store(S3Config{Endpoint: "http://x"}); err == nil {
		t.Error("want error for incomplete config")
	}
}

func TestNewS3Store_stripsTrailingSlash(t *testing.T) {
	cfg := testS3Config()
	cfg.Endpoint = "http://minio:9000/"
	s, err := NewS3Store(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.Endpoint != "http://minio:9000" {
		t.Errorf("Endpoint = %q", s.cfg.Endpoint)
	}
}

func TestS3Store_objectURL_pathStyle(t *testing.T) {
	cfg := testS3Config()
	cfg.Endpoint = "http://minio:9000"
	cfg.PathStyle = true
	s, _ := NewS3Store(cfg)
	got, err := s.objectURL("abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://minio:9000/mybucket/abc123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestS3Store_objectURL_virtualHost(t *testing.T) {
	cfg := testS3Config()
	s, _ := NewS3Store(cfg)
	got, err := s.objectURL("abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://mybucket.s3.us-east-1.amazonaws.com/abc123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestS3Store_presign_structure(t *testing.T) {
	s, err := NewS3Store(testS3Config())
	if err != nil {
		t.Fatal(err)
	}
	presigned, err := s.Presign(http.MethodGet, "abcdef123456", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(presigned)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"X-Amz-Algorithm":  "AWS4-HMAC-SHA256",
		"X-Amz-Date":       q.Get("X-Amz-Date"),
		"X-Amz-Expires":    "900",
		"X-Amz-SignedHeaders": "host",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	cred := q.Get("X-Amz-Credential")
	if !strings.HasPrefix(cred, "AKIDEXAMPLE/") {
		t.Errorf("credential = %q", cred)
	}
	if !strings.HasSuffix(cred, "/s3/aws4_request") {
		t.Errorf("credential scope = %q", cred)
	}
	sig := q.Get("X-Amz-Signature")
	if len(sig) != 64 {
		t.Errorf("signature length = %d, want 64 hex chars", len(sig))
	}
}

func TestS3Store_presign_deterministic(t *testing.T) {
	cfg := testS3Config()
	s, _ := NewS3Store(cfg)
	// Freeze time for determinism.
	fixed := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	a, err := s.Presign(http.MethodGet, "sha", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Presign(http.MethodGet, "sha", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("non-deterministic presign:\n%s\n%s", a, b)
	}
}

func TestS3Store_presign_differentMethodsDiffer(t *testing.T) {
	cfg := testS3Config()
	s, _ := NewS3Store(cfg)
	fixed := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	get, _ := s.Presign(http.MethodGet, "sha", 15*time.Minute)
	put, _ := s.Presign(http.MethodPut, "sha", 15*time.Minute)
	if get == put {
		t.Error("GET and PUT presigned URLs should differ")
	}
}

func TestS3Store_presign_expiryCapped(t *testing.T) {
	s, _ := NewS3Store(testS3Config())
	// Request 30 days; should cap at 7 days = 604800 seconds.
	presigned, err := s.Presign(http.MethodGet, "sha", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(presigned)
	expires := u.Query().Get("X-Amz-Expires")
	if expires != "604800" {
		t.Errorf("X-Amz-Expires = %q, want 604800 (7 days)", expires)
	}
}

func TestS3Store_presign_zeroExpiryDefault(t *testing.T) {
	s, _ := NewS3Store(testS3Config())
	presigned, err := s.Presign(http.MethodGet, "sha", 0)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(presigned)
	if u.Query().Get("X-Amz-Expires") != "900" {
		t.Errorf("default expiry should be 900s, got %q", u.Query().Get("X-Amz-Expires"))
	}
}

func TestS3Store_presign_sessionToken(t *testing.T) {
	cfg := testS3Config()
	cfg.SessionToken = "tok123"
	s, _ := NewS3Store(cfg)
	presigned, err := s.Presign(http.MethodGet, "sha", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(presigned)
	if u.Query().Get("X-Amz-Security-Token") != "tok123" {
		t.Errorf("security token = %q", u.Query().Get("X-Amz-Security-Token"))
	}
}

func TestS3Store_invalidSHA(t *testing.T) {
	s, _ := NewS3Store(testS3Config())
	// Has with invalid sha returns false (no network call).
	if s.Has("notasha") {
		t.Error("Has with invalid sha should be false")
	}
}

func TestS3Store_touchAndGC_noop(t *testing.T) {
	s, _ := NewS3Store(testS3Config())
	s.Touch("anything") // no panic
	n, err := s.GC(time.Hour)
	if err != nil || n != 0 {
		t.Errorf("GC = (%d, %v), want (0, nil)", n, err)
	}
}

func TestS3Store_root(t *testing.T) {
	s, _ := NewS3Store(testS3Config())
	if s.Root() != "s3://mybucket" {
		t.Errorf("Root() = %q", s.Root())
	}
}

func TestAWSURIEncode(t *testing.T) {
	cases := map[string]string{
		"abc":     "abc",
		"a b":     "a%20b",
		"a/b":     "a%2Fb",
		"a+b":     "a%2Bb",
		"a=b":     "a%3Db",
		"a-b_c.d": "a-b_c.d",
		"~":       "~",
	}
	for in, want := range cases {
		if got := awsURIEncode(in); got != want {
			t.Errorf("awsURIEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalQueryString_sorted(t *testing.T) {
	q := url.Values{}
	q.Set("Zebra", "last")
	q.Add("apple", "first")
	q.Add("apple", "second")
	got := canonicalQueryString(q)
	want := "Zebra=last&apple=first&apple=second"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestS3Store_implementsStore(t *testing.T) {
	var _ Store = (*S3Store)(nil)
}

func TestS3Store_localStoreParity(t *testing.T) {
	// Both stores must satisfy the same interface.
	var _ Store = (*LocalStore)(nil)
	var _ Store = (*S3Store)(nil)
}
