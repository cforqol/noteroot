package resources

// Unit tests for S3Store's HTTP behaviour (Put/Get/Has/Size/Delete)
// against an in-process fake S3 server, plus an independent SigV4
// verifier that re-derives the signature of every received request.
//
// The verifier intentionally re-implements the signing math in the test
// (not reusing Presign/signingKey) so a bug in Presign shows up as a
// signature mismatch instead of being self-consistently wrong.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake S3 ----------------------------------------------------------

type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte // key (after bucket) -> body
	failWith map[string]int    // method -> status code to fail with
	verify   func(*http.Request) error
	puts     int
	gets     int
	srv      *httptest.Server
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{
		objects:  map[string][]byte{},
		failWith: map[string]int{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions { // CORS for browser e2e reuse
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "PUT, GET, HEAD, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Path-style: /{bucket}/{key}
	p := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, ok := strings.Cut(p, "/")
	if !ok || bucket != f.bucket() || key == "" {
		http.Error(w, "no key", http.StatusNotFound)
		return
	}
	if f.verify != nil {
		if err := f.verify(r); err != nil {
			http.Error(w, "signature: "+err.Error(), http.StatusForbidden)
			return
		}
	}
	if st, bad := f.failWith[r.Method]; bad {
		http.Error(w, "injected failure", st)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.objects[key] = body
		f.puts++
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		obj, found := f.objects[key]
		if !found {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		f.gets++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(obj)
	case http.MethodHead:
		obj, found := f.objects[key]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj)))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "bad method", http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) bucket() string { return "testbucket" }

func (f *fakeS3) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeS3) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return b, ok
}

func newS3StoreFor(t *testing.T, f *fakeS3) *S3Store {
	t.Helper()
	s, err := NewS3Store(S3Config{
		Endpoint:  f.srv.URL,
		Bucket:    f.bucket(),
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "SECRETKEY",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- independent SigV4 verifier ---------------------------------------

// vtEncode encodes per RFC 3986 (SigV4 variant: space is %20).
func vtEncode(s string) string {
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

// vtCanonicalQuery rebuilds a canonical query string from the raw query
// (excluding X-Amz-Signature), sorted by key then value.
func vtCanonicalQuery(raw string) (string, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "", err
	}
	q.Del("X-Amz-Signature")
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, vtEncode(k)+"="+vtEncode(v))
		}
	}
	return strings.Join(parts, "&"), nil
}

func vtHMAC(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// verifySigV4 independently recomputes the SigV4 signature for a
// received presigned request and compares it to the query parameter.
// Returns an error describing the first mismatch.
func verifySigV4(r *http.Request, accessKey, secretKey string) error {
	q := r.URL.Query()
	if got := q.Get("X-Amz-Algorithm"); got != "AWS4-HMAC-SHA256" {
		return fmt.Errorf("algorithm = %q", got)
	}
	cred := q.Get("X-Amz-Credential")
	amzDate := q.Get("X-Amz-Date")
	if cred == "" || amzDate == "" {
		return errors.New("missing credential or date")
	}
	keyPart, scope, ok := strings.Cut(cred, "/")
	if !ok {
		return fmt.Errorf("bad credential %q", cred)
	}
	if keyPart != accessKey {
		return fmt.Errorf("access key = %q, want %q", keyPart, accessKey)
	}
	// scope: date/region/s3/aws4_request
	scopeParts := strings.Split(scope, "/")
	if len(scopeParts) != 4 || scopeParts[1] == "" || scopeParts[2] != "s3" || scopeParts[3] != "aws4_request" {
		return fmt.Errorf("bad scope %q", scope)
	}
	dateStamp := scopeParts[0]

	sig := q.Get("X-Amz-Signature")
	if sig == "" {
		return errors.New("no signature in query")
	}
	if q.Get("X-Amz-SignedHeaders") != "host" {
		return fmt.Errorf("signedHeaders = %q", q.Get("X-Amz-SignedHeaders"))
	}
	if q.Get("X-Amz-Expires") == "" {
		return errors.New("missing X-Amz-Expires")
	}

	canonQuery, err := vtCanonicalQuery(r.URL.RawQuery)
	if err != nil {
		return err
	}

	// Canonical request (AWS SigV4 layout: header block ends with \n,
	// then the join's separator yields the documented blank line).
	canonicalRequest := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		canonQuery,
		"host:" + r.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	sum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sum[:]),
	}, "\n")

	// Derive the signing key independently.
	kDate := vtHMAC([]byte("AWS4"+secretKey), dateStamp)
	kRegion := vtHMAC(kDate, scopeParts[1])
	kService := vtHMAC(kRegion, "s3")
	kSigning := vtHMAC(kService, "aws4_request")
	want := hex.EncodeToString(vtHMAC(kSigning, stringToSign))

	if sig != want {
		return fmt.Errorf("signature mismatch: got %s want %s", sig, want)
	}
	return nil
}

func requireVerified(t *testing.T, f *fakeS3, accessKey, secretKey string) {
	t.Helper()
	f.verify = func(r *http.Request) error {
		return verifySigV4(r, accessKey, secretKey)
	}
}

// --- Put --------------------------------------------------------------

func TestS3Put_storesObjectContentAddressed(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	content := []byte("hello object storage")
	sha, size, created, err := s.Put(strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}
	if sha != mustSHA(t, string(content)) {
		t.Errorf("sha = %q, want %q", sha, mustSHA(t, string(content)))
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if !created {
		t.Error("created = false, want true (S3 Put always uploads)")
	}
	got, found := f.get(sha)
	if !found {
		t.Fatal("object not stored in fake S3")
	}
	if string(got) != string(content) {
		t.Errorf("stored body = %q, want %q", got, content)
	}
	if f.puts != 1 {
		t.Errorf("puts = %d, want 1", f.puts)
	}
}

func TestS3Put_verifiesRequestSignature(t *testing.T) {
	f := newFakeS3(t)
	requireVerified(t, f, "AKIDEXAMPLE", "SECRETKEY")
	s := newS3StoreFor(t, f)

	if _, _, _, err := s.Put(strings.NewReader("signed body")); err != nil {
		t.Fatalf("put with verifying fake S3: %v", err)
	}
	if !f.has(mustSHA(t, "signed body")) {
		t.Error("object missing")
	}
}

func TestS3Put_wrongSecretRejected(t *testing.T) {
	f := newFakeS3(t)
	requireVerified(t, f, "AKIDEXAMPLE", "DIFFERENT-SECRET")
	s := newS3StoreFor(t, f)

	if _, _, _, err := s.Put(strings.NewReader("x")); err == nil {
		t.Fatal("want error when verifier uses a different secret")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want status 403", err)
	}
}

func TestS3Put_serverError(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)
	f.failWith["PUT"] = http.StatusInternalServerError

	_, _, _, err := s.Put(strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "put status 500") {
		t.Errorf("err = %v, want put status 500", err)
	}
}

func TestS3Put_readError(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	_, _, _, err := s.Put(errReader{})
	if err == nil || !strings.Contains(err.Error(), "s3: read") {
		t.Errorf("err = %v, want s3: read", err)
	}
	if f.puts != 0 {
		t.Error("no PUT should reach the server")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestS3PutBuffered_delegatesToPut(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	sha, size, created, err := s.PutBuffered(strings.NewReader("buffered"))
	if err != nil {
		t.Fatal(err)
	}
	if sha != mustSHA(t, "buffered") || size != 8 || !created {
		t.Errorf("sha=%q size=%d created=%v", sha, size, created)
	}
	if !f.has(sha) {
		t.Error("object missing after PutBuffered")
	}
}

// --- Get --------------------------------------------------------------

func TestS3Get_roundTrip(t *testing.T) {
	f := newFakeS3(t)
	requireVerified(t, f, "AKIDEXAMPLE", "SECRETKEY")
	s := newS3StoreFor(t, f)

	content := []byte("round trip payload")
	sha, _, _, err := s.Put(strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestS3Get_notFound(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	sha := mustSHA(t, "absent")
	rc, err := s.Get(sha)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if rc != nil {
		t.Error("reader should be nil")
	}
}

func TestS3Get_invalidSHA(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	if _, err := s.Get("not-a-sha"); err == nil {
		t.Error("want error for invalid sha")
	}
}

func TestS3Get_serverError(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)
	f.failWith["GET"] = http.StatusBadGateway

	if _, err := s.Get(mustSHA(t, "x")); err == nil || !strings.Contains(err.Error(), "get status 502") {
		t.Errorf("err = %v, want get status 502", err)
	}
}

// --- Has --------------------------------------------------------------

func TestS3Has_presence(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	absent := mustSHA(t, "absent")
	if s.Has(absent) {
		t.Error("Has(absent) = true")
	}
	sha, _, _, err := s.Put(strings.NewReader("present"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has(sha) {
		t.Error("Has(present) = false")
	}
	if s.Has("garbage") {
		t.Error("Has(garbage) = true, want false")
	}
}

func TestS3Has_serverErrorFalse(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)
	f.failWith["HEAD"] = http.StatusInternalServerError

	if s.Has(mustSHA(t, "x")) {
		t.Error("Has = true on 500, want false")
	}
}

// --- Size -------------------------------------------------------------

func TestS3Size_contentLength(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	sha, _, _, err := s.Put(strings.NewReader("12345"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Size(sha)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Errorf("Size = %d, want 5", got)
	}
}

func TestS3Size_notFound(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	if _, err := s.Size(mustSHA(t, "absent")); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestS3Size_invalidAndError(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	if _, err := s.Size("nope"); err == nil {
		t.Error("want error for invalid sha")
	}
	f.failWith["HEAD"] = http.StatusInternalServerError
	if _, err := s.Size(mustSHA(t, "x")); err == nil || !strings.Contains(err.Error(), "head status 500") {
		t.Errorf("err = %v, want head status 500", err)
	}
}

// --- Delete -----------------------------------------------------------

func TestS3Delete_removesObject(t *testing.T) {
	f := newFakeS3(t)
	requireVerified(t, f, "AKIDEXAMPLE", "SECRETKEY")
	s := newS3StoreFor(t, f)

	sha, _, _, err := s.Put(strings.NewReader("delete me"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(sha); err != nil {
		t.Fatal(err)
	}
	if f.has(sha) {
		t.Error("object still present after Delete")
	}
}

func TestS3Delete_missingTolerated(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	if err := s.Delete(mustSHA(t, "never-stored")); err != nil {
		t.Errorf("Delete missing object = %v, want nil", err)
	}
}

func TestS3Delete_invalidAndError(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	if err := s.Delete("bad"); err == nil {
		t.Error("want error for invalid sha")
	}
	f.failWith["DELETE"] = http.StatusInternalServerError
	if err := s.Delete(mustSHA(t, "x")); err == nil || !strings.Contains(err.Error(), "delete status 500") {
		t.Errorf("err = %v, want delete status 500", err)
	}
}

// --- Presign signature validity ---------------------------------------

func TestS3Presign_signatureVerifies(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodHead, http.MethodDelete} {
		raw, err := s.Presign(method, mustSHA(t, "k"), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		req := &http.Request{Method: method, URL: u, Host: u.Host}
		if err := verifySigV4(req, "AKIDEXAMPLE", "SECRETKEY"); err != nil {
			t.Errorf("%s presign: %v", method, err)
		}
	}
}

func TestS3Presign_tamperedQueryFails(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	raw, err := s.Presign(http.MethodGet, mustSHA(t, "k"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("X-Amz-Expires", "600") // attacker extends expiry
	u.RawQuery = q.Encode()

	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host}
	if err := verifySigV4(req, "AKIDEXAMPLE", "SECRETKEY"); err == nil {
		t.Error("tampered query verified — signature must fail")
	}
}

func TestS3Presign_specialCharsInKeyVerify(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)

	// Keys with characters requiring RFC 3986 encoding (e.g. spaces in
	// a future non-content-addressed layout).
	raw, err := s.Presign(http.MethodGet, "folder/my file (1).png", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host}
	if err := verifySigV4(req, "AKIDEXAMPLE", "SECRETKEY"); err != nil {
		t.Errorf("special-char key: %v", err)
	}
}

func TestS3Presign_sessionTokenCarriedAndSigned(t *testing.T) {
	f := newFakeS3(t)
	s, err := NewS3Store(S3Config{
		Endpoint:     f.srv.URL,
		Bucket:       f.bucket(),
		Region:       "us-east-1",
		AccessKey:    "AKIDEXAMPLE",
		SecretKey:    "SECRETKEY",
		PathStyle:    true,
		SessionToken: "FQoGZXIvYXdzEJr//////////wEXAMPLEsession",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Verifier checks the token is present in the query (it is covered
	// by the signature because it is rebuilt into the canonical query).
	f.verify = func(r *http.Request) error {
		if got := r.URL.Query().Get("X-Amz-Security-Token"); got != "FQoGZXIvYXdzEJr//////////wEXAMPLEsession" {
			return fmt.Errorf("security token = %q", got)
		}
		return verifySigV4(r, "AKIDEXAMPLE", "SECRETKEY")
	}

	sha, _, _, err := s.Put(strings.NewReader("temp creds"))
	if err != nil {
		t.Fatalf("put with session token: %v", err)
	}
	rc, err := s.Get(sha)
	if err != nil {
		t.Fatalf("get with session token: %v", err)
	}
	_ = rc.Close()
}

func TestS3Presign_clockSkewWithinWindow(t *testing.T) {
	// The signing date is baked into the signature; the fake server
	// verifies exact-match signatures so a fixed clock must produce a
	// verifiable URL (guards against time-zone slip in amzDate).
	f := newFakeS3(t)
	requireVerified(t, f, "AKIDEXAMPLE", "SECRETKEY")
	s := newS3StoreFor(t, f)
	fixed := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed.Local() } // local zone input

	raw, err := s.Presign(http.MethodGet, mustSHA(t, "k"), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "20260315T103000Z") {
		t.Errorf("amzDate not UTC-normalized: %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host}
	if err := verifySigV4(req, "AKIDEXAMPLE", "SECRETKEY"); err != nil {
		t.Errorf("fixed-clock presign: %v", err)
	}
}

// --- NewStore factory --------------------------------------------------

func TestNewStore_returnsLocalWhenS3Disabled(t *testing.T) {
	s, err := NewStore(t.TempDir(), S3Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Errorf("type = %T, want *LocalStore", s)
	}
}

func TestNewStore_returnsS3WhenEnabled(t *testing.T) {
	f := newFakeS3(t)
	s, err := NewStore(t.TempDir(), S3Config{
		Endpoint:  f.srv.URL,
		Bucket:    f.bucket(),
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "SECRETKEY",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*S3Store); !ok {
		t.Errorf("type = %T, want *S3Store", s)
	}
}

func TestNewStore_partialS3FallsToLocal(t *testing.T) {
	// Partial config is not Enabled() — must not error, must be local.
	s, err := NewStore(t.TempDir(), S3Config{Endpoint: "https://s3.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Errorf("type = %T, want *LocalStore", s)
	}
}

// --- helpers -----------------------------------------------------------

func mustSHA(t *testing.T, content string) string {
	t.Helper()
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// --- gap coverage: no-ops, network failures, bad config -----------------

func TestS3Touch_isNoopWithoutRequests(t *testing.T) {
	f := newFakeS3(t)
	s := newS3StoreFor(t, f)
	// Touch must neither panic nor hit the network.
	s.Touch(mustSHA(t, "touched"))
	s.Touch("") // invalid input tolerated by the no-op
	if f.puts != 0 {
		t.Error("Touch issued requests")
	}
}

// TestS3_networkUnreachableAllMethods: every Store method degrades
// gracefully (error / false) when the endpoint is down — no panics, no
// misleading success.
func TestS3_networkUnreachableAllMethods(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // immediately unreachable

	s, err := NewS3Store(S3Config{
		Endpoint:  dead.URL,
		Bucket:    "testbucket",
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "SECRETKEY",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Shorten the client timeout so the test does not hang.
	s.hc.Timeout = 2 * time.Second

	sha := mustSHA(t, "network")

	if _, _, _, err := s.Put(strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "s3: put") {
		t.Errorf("Put err = %v, want s3: put", err)
	}
	if _, err := s.Get(sha); err == nil || !strings.Contains(err.Error(), "s3: get") {
		t.Errorf("Get err = %v, want s3: get", err)
	}
	if s.Has(sha) {
		t.Error("Has = true on unreachable store")
	}
	if _, err := s.Size(sha); err == nil {
		t.Error("Size err = nil on unreachable store")
	}
	if err := s.Delete(sha); err == nil || !strings.Contains(err.Error(), "s3: delete") {
		t.Errorf("Delete err = %v, want s3: delete", err)
	}
}

// TestS3Presign_unparseableEndpoint: config validation only requires a
// non-empty endpoint, so objectURL's parse failure must surface as a
// Presign error rather than a malformed URL.
func TestS3Presign_unparseableEndpoint(t *testing.T) {
	s, err := NewS3Store(S3Config{
		Endpoint:  "[",
		Bucket:    "testbucket",
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "SECRETKEY",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Presign(http.MethodGet, mustSHA(t, "k"), time.Minute); err == nil {
		t.Error("want error for unparseable endpoint")
	}
}

func TestCanonicalQueryString_empty(t *testing.T) {
	if got := canonicalQueryString(url.Values{}); got != "" {
		t.Errorf("empty query = %q, want %q", got, "")
	}
}

func TestValidSHA(t *testing.T) {
	valid := mustSHA(t, "x")
	if !ValidSHA(valid) {
		t.Error("64 hex lowercase should be valid")
	}
	invalid := []string{
		"", "abc", strings.Repeat("A", 64), strings.Repeat("z", 64),
		strings.Repeat("a", 63), strings.Repeat("a", 65),
		"../../etc/passwd", strings.Repeat("a", 32) + "/" + strings.Repeat("b", 32),
		"0x" + strings.Repeat("a", 62),
	}
	for _, s := range invalid {
		if ValidSHA(s) {
			t.Errorf("ValidSHA(%q) = true, want false", s)
		}
	}
}

// TestS3_brokenEndpointPropagatesToAllMethods drives every code path
// that returns objectURL/Presign errors inside the Store methods
// (url.Parse rejects control characters, so Endpoint "\x00" is enabled
// by Enabled() but unusable).
func TestS3_brokenEndpointPropagatesToAllMethods(t *testing.T) {
	s, err := NewS3Store(S3Config{
		Endpoint:  "\x00",
		Bucket:    "testbucket",
		Region:    "us-east-1",
		AccessKey: "AKIDEXAMPLE",
		SecretKey: "SECRETKEY",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sha := mustSHA(t, "broken")

	if _, err := s.Presign(http.MethodGet, sha, time.Minute); err == nil {
		t.Error("Presign: want objectURL parse error")
	}
	if _, _, _, err := s.Put(strings.NewReader("x")); err == nil {
		t.Error("Put: want presign error inside putBytes")
	}
	if _, err := s.Get(sha); err == nil {
		t.Error("Get: want presign error")
	}
	if s.Has(sha) {
		t.Error("Has: want false on presign error")
	}
	if _, err := s.Size(sha); err == nil {
		t.Error("Size: want presign error")
	}
	if err := s.Delete(sha); err == nil {
		t.Error("Delete: want presign error")
	}
}
