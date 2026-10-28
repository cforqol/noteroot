package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"noteroot/internal/config"
)

func csrfProbe(t *testing.T, publicOrigin string, method, target string, hdr map[string]string) int {
	t.Helper()
	next := CSRF(publicOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, target, nil)
	req.Host = "notes.example.com"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	return rec.Code
}

func TestCSRF_formContentTypesRejected(t *testing.T) {
	for _, ct := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=----x",
	} {
		if code := csrfProbe(t, "", "PUT", "/n/w/note?path=a.md",
			map[string]string{"Content-Type": ct}); code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", ct, code)
		}
	}
	for _, ct := range []string{"", "text/markdown", "application/json"} {
		if code := csrfProbe(t, "", "PUT", "/n/w/note?path=a.md",
			map[string]string{"Content-Type": ct}); code != http.StatusOK {
			t.Errorf("%q: status = %d, want 200", ct, code)
		}
	}
}

func TestCSRF_originEnforced(t *testing.T) {
	match := map[string]string{"Origin": "https://notes.example.com"}
	mismatch := map[string]string{"Origin": "https://evil.example.org"}
	if code := csrfProbe(t, "", "PUT", "/", match); code != http.StatusOK {
		t.Errorf("same-host origin: %d, want 200", code)
	}
	if code := csrfProbe(t, "", "PUT", "/", mismatch); code != http.StatusForbidden {
		t.Errorf("cross origin: %d, want 403", code)
	}
	if code := csrfProbe(t, "", "PUT", "/", map[string]string{"Origin": "null"}); code != http.StatusForbidden {
		t.Errorf("opaque origin: %d, want 403", code)
	}
	if code := csrfProbe(t, "", "PUT", "/", nil); code != http.StatusOK {
		t.Errorf("no origin (curl/API): %d, want 200", code)
	}
	if code := csrfProbe(t, "", "PUT", "/", map[string]string{"Referer": "https://evil.example.org/x"}); code != http.StatusForbidden {
		t.Errorf("cross referer: %d, want 403", code)
	}
	if code := csrfProbe(t, "", "PUT", "/", map[string]string{"Referer": "https://notes.example.com/x"}); code != http.StatusOK {
		t.Errorf("same-host referer: %d, want 200", code)
	}
}

func TestCSRF_configuredOrigin(t *testing.T) {
	pub := "https://notes.example.com"
	other := map[string]string{"Origin": "http://notes.example.com"}
	if code := csrfProbe(t, pub, "POST", "/", map[string]string{"Origin": pub}); code != http.StatusOK {
		t.Errorf("configured origin: %d, want 200", code)
	}
	if code := csrfProbe(t, pub, "POST", "/", other); code != http.StatusForbidden {
		t.Errorf("same host, wrong scheme: %d, want 403", code)
	}
}

func TestCSRF_mutatingRouteEnforces(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x"}))
	rec := put(t, srv, putNotePath("n.md"), []byte("x"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("form PUT: %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, notePath("n.md"), nil)
	req.Header.Set("Origin", "https://evil.example.org")
	getRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(getRec, req)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET with cross origin: %d, want 200 (reads are safe)", getRec.Code)
	}
}
