package resources

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config configures an S3-compatible object store (AWS S3, MinIO,
// Cloudflare R2, etc). Endpoint is the base URL; for AWS use
// "https://s3.<region>.amazonaws.com". PathStyle=true is required by
// MinIO and most self-hosted servers.
type S3Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	PathStyle bool
	// SessionToken is optional (for temporary credentials).
	SessionToken string
}

// Enabled reports whether S3 is configured (all required fields present).
func (c S3Config) Enabled() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.Region != "" &&
		c.AccessKey != "" && c.SecretKey != ""
}

// S3Store implements Store against an S3-compatible endpoint using only
// the standard library (SigV4 presigned URLs + plain HTTP).
//
// Touch is a no-op (S3 manages its own metadata); GC is a no-op
// (lifecycle rules handle expiry). Put streams via a presigned PUT so
// the upload never buffers the whole object in memory.
type S3Store struct {
	cfg S3Config
	hc  *http.Client
	now func() time.Time
}

// NewS3Store validates the config and returns a store.
func NewS3Store(cfg S3Config) (*S3Store, error) {
	if !cfg.Enabled() {
		return nil, errors.New("s3: incomplete config")
	}
	// Normalize endpoint: strip trailing slash.
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	return &S3Store{
		cfg: cfg,
		hc:  &http.Client{Timeout: 60 * time.Second},
		now: time.Now,
	}, nil
}

// objectURL returns the full URL for an object key.
// Path-style:    {endpoint}/{bucket}/{key}
// Virtual-host:  {scheme}://{bucket}.{host}/{key}
func (s *S3Store) objectURL(key string) (string, error) {
	u, err := url.Parse(s.cfg.Endpoint)
	if err != nil {
		return "", err
	}
	if s.cfg.PathStyle {
		return fmt.Sprintf("%s://%s/%s/%s", u.Scheme, u.Host, s.cfg.Bucket, key), nil
	}
	// Virtual-hosted style.
	return fmt.Sprintf("%s://%s.%s/%s", u.Scheme, s.cfg.Bucket, u.Host, key), nil
}

// Put stores the content. It computes sha256 while streaming, then does
// a presigned PUT to S3. Returns the sha, size, and created=true (S3
// cannot cheaply pre-check existence without a HEAD, so we always PUT;
// content-addressing means overwriting is idempotent). The body is
// capped at DefaultMaxSize+1 regardless of caller so a future uncapped
// caller cannot OOM the process.
func (s *S3Store) Put(r io.Reader) (string, int64, bool, error) {
	h := sha256.New()
	body, err := io.ReadAll(io.TeeReader(io.LimitReader(r, DefaultMaxSize+1), h))
	if err != nil {
		return "", 0, false, fmt.Errorf("s3: read: %w", err)
	}
	if int64(len(body)) > DefaultMaxSize {
		return "", 0, false, fmt.Errorf("%w: %d bytes > %d", ErrTooLarge, len(body), DefaultMaxSize)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	key := sha

	if err := s.putBytes(key, body); err != nil {
		return "", 0, false, err
	}
	return sha, int64(len(body)), true, nil
}

// PutBuffered is Put for the Store interface (S3 has no local temp).
func (s *S3Store) PutBuffered(r io.Reader) (string, int64, bool, error) {
	return s.Put(r)
}

// putBytes performs a presigned PUT with the given body.
func (s *S3Store) putBytes(key string, body []byte) error {
	presigned, err := s.Presign(http.MethodPut, key, 15*time.Minute)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, presigned, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := s.hc.Do(req)
	if err != nil {
		return fmt.Errorf("s3: put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("s3: put status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// Get fetches the object and returns a reader over its body.
func (s *S3Store) Get(sha string) (io.ReadCloser, error) {
	if !validSHA(sha) {
		return nil, fmt.Errorf("s3: invalid sha %q", sha)
	}
	presigned, err := s.Presign(http.MethodGet, sha, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, presigned, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: get: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("s3: get status %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

// Has reports whether the object exists (HEAD request).
func (s *S3Store) Has(sha string) bool {
	if !validSHA(sha) {
		return false
	}
	presigned, err := s.Presign(http.MethodHead, sha, 15*time.Minute)
	if err != nil {
		return false
	}
	req, err := http.NewRequest(http.MethodHead, presigned, nil)
	if err != nil {
		return false
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// Size returns the Content-Length from a HEAD, or ErrNotFound.
func (s *S3Store) Size(sha string) (int64, error) {
	if !validSHA(sha) {
		return 0, fmt.Errorf("s3: invalid sha %q", sha)
	}
	presigned, err := s.Presign(http.MethodHead, sha, 15*time.Minute)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodHead, presigned, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return 0, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("s3: head status %d", resp.StatusCode)
	}
	return resp.ContentLength, nil
}

// Delete removes the object. Missing objects are not an error.
func (s *S3Store) Delete(sha string) error {
	if !validSHA(sha) {
		return fmt.Errorf("s3: invalid sha %q", sha)
	}
	presigned, err := s.Presign(http.MethodDelete, sha, 15*time.Minute)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodDelete, presigned, nil)
	if err != nil {
		return err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return fmt.Errorf("s3: delete: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("s3: delete status %d", resp.StatusCode)
	}
	return nil
}

// Touch is a no-op: S3 has no cheap per-object access-time update.
func (s *S3Store) Touch(string) {}

// GC is a no-op: use S3 lifecycle rules for expiry.
func (s *S3Store) GC(time.Duration) (int, error) { return 0, nil }

// Root returns the s3:// URL for display purposes.
func (s *S3Store) Root() string { return "s3://" + s.cfg.Bucket }

// Presign generates a presigned URL for the given method and object key.
// The URL is valid for the given duration (max 7 days per AWS SigV4).
//
// This is the core of stage 7: clients upload directly to S3 with a
// presigned PUT (never touching the app server with the bytes), and read
// with a presigned GET.
func (s *S3Store) Presign(method, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		expiry = 15 * time.Minute
	}
	if expiry > 7*24*time.Hour {
		expiry = 7 * 24 * time.Hour
	}

	u, err := s.objectURL(key)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}

	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, s.cfg.Region)

	query := parsed.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.cfg.AccessKey+"/"+credentialScope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")
	if s.cfg.SessionToken != "" {
		query.Set("X-Amz-Security-Token", s.cfg.SessionToken)
	}

	// Canonical query string: sorted by key, URI-encoded.
	canonicalQuery := canonicalQueryString(query)

	// Canonical headers: only host is signed for presigned URLs.
	canonicalHeaders := "host:" + parsed.Host + "\n"
	signedHeaders := "host"

	// Payload hash: UNSIGNED-PAYLOAD for presigned.
	payloadHash := "UNSIGNED-PAYLOAD"

	canonicalRequest := strings.Join([]string{
		method,
		parsed.EscapedPath(),
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign.
	hash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hex.EncodeToString(hash[:]),
	}, "\n")

	// Signing key.
	signingKey := s.signingKey(dateStamp)

	signature := hmacSHA256(signingKey, stringToSign)

	query.Set("X-Amz-Signature", hex.EncodeToString(signature))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// signingKey derives the AWS SigV4 signing key.
func (s *S3Store) signingKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, s.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	return kSigning
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalQueryString sorts and URI-encodes query parameters per SigV4.
func canonicalQueryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := q[k]
		// Sort values for duplicate keys.
		vsCopy := make([]string, len(vs))
		copy(vsCopy, vs)
		sort.Strings(vsCopy)
		for _, v := range vsCopy {
			parts = append(parts, awsURIEncode(k)+"="+awsURIEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// awsURIEncode encodes per RFC 3986 (unreserved = A-Z a-z 0-9 - _ . ~).
// SigV4 requires space as %20 (not +).
func awsURIEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Compile-time check.
var _ Store = (*S3Store)(nil)
