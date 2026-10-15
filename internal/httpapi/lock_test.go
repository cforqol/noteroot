package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"noteroot/internal/config"
)

func lockReq(t *testing.T, srv *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestLock_roundtrip(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x"}))
	rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Holder    string `json:"holder"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Holder != "tab-1" || body.ExpiresAt <= time.Now().Unix() {
		t.Errorf("body = %+v", body)
	}
	if rec := lockReq(t, srv, http.MethodDelete, "/n/work/lock?path=n.md&holder=tab-1"); rec.Code != http.StatusOK {
		t.Errorf("release status = %d", rec.Code)
	}
}

func TestLock_conflict(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x"}))
	if rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-1"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-2")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Holder    string `json:"holder"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Holder != "tab-1" || body.ExpiresAt == 0 {
		t.Errorf("409 body = %+v, want current holder+expiry", body)
	}
	if rec := lockReq(t, srv, http.MethodDelete, "/n/work/lock?path=n.md&holder=tab-2"); rec.Code != http.StatusNotFound {
		t.Errorf("release by other: %d, want 404", rec.Code)
	}
}

func TestLock_refresh(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x"}))
	first := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-1&ttl=60")
	second := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-1&ttl=600")
	var b1, b2 struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	_ = json.NewDecoder(first.Body).Decode(&b1)
	_ = json.NewDecoder(second.Body).Decode(&b2)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || b2.ExpiresAt <= b1.ExpiresAt {
		t.Errorf("refresh did not extend: %v -> %v", b1, b2)
	}
}

func TestLock_ttlClamped(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x"}))
	rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=tab-1&ttl=99999")
	var body struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.ExpiresAt-time.Now().Unix() > 30*60 {
		t.Errorf("TTL not capped: %+v", body)
	}
}

func TestLock_errors(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "x", "d/y.md": "y"}))
	tests := []struct {
		name, method, target string
		want                 int
	}{
		{"missing note", http.MethodPut, "/n/work/lock?path=nope.md&holder=t", http.StatusNotFound},
		{"directory", http.MethodPut, "/n/work/lock?path=d&holder=t", http.StatusNotFound},
		{"missing holder", http.MethodPut, "/n/work/lock?path=n.md", http.StatusBadRequest},
		{"missing path", http.MethodPut, "/n/work/lock?holder=t", http.StatusBadRequest},
		{"release missing", http.MethodDelete, "/n/work/lock?path=nope.md&holder=t", http.StatusNotFound},
		{"release missing holder", http.MethodDelete, "/n/work/lock?path=n.md", http.StatusBadRequest},
		{"unknown notebook", http.MethodPut, "/n/nope/lock?path=a.md&holder=t", http.StatusNotFound},
		{"unknown notebook delete", http.MethodDelete, "/n/nope/lock?path=a.md&holder=t", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := lockReq(t, srv, tt.method, tt.target); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLock_requiresAuth_in_forward_mode(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, notesFixture(t, map[string]string{"n.md": "x"}))
	if rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=t"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLock_stateUnavailable(t *testing.T) {
	reg := notesFixture(t, map[string]string{"n.md": "x"})
	nb, _ := reg.Get("work")
	if err := os.WriteFile(nb.Layout.App(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := lockReq(t, srv, http.MethodPut, "/n/work/lock?path=n.md&holder=t"); rec.Code != http.StatusInternalServerError {
		t.Errorf("put status = %d, want 500", rec.Code)
	}
	if rec := lockReq(t, srv, http.MethodDelete, "/n/work/lock?path=n.md&holder=t"); rec.Code != http.StatusInternalServerError {
		t.Errorf("delete status = %d, want 500", rec.Code)
	}
}
