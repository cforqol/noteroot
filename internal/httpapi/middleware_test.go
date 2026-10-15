package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"noteroot/internal/config"
)

func TestRequestID_setsHeader(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header not set")
	}
}

func TestRequestID_unique(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ids := make(map[string]bool)
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		id := rec.Header().Get("X-Request-Id")
		if id == "" {
			t.Fatal("empty request ID")
		}
		if ids[id] {
			t.Fatalf("duplicate request ID: %s", id)
		}
		ids[id] = true
	}
}

func TestRequestID_storesInContext(t *testing.T) {
	var ctxID string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(keyRequestID).(string); ok {
			ctxID = v
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if ctxID == "" {
		t.Error("request ID not stored in context")
	}
	if ctxID != rec.Header().Get("X-Request-Id") {
		t.Errorf("context ID %q != header %q", ctxID, rec.Header().Get("X-Request-Id"))
	}
}

func TestRecover_fromPanic(t *testing.T) {
	h := Recover(newTestLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestRecover_noOp(t *testing.T) {
	h := Recover(newTestLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLog_capturesExplicitStatus(t *testing.T) {
	h := Log(newTestLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
}

func TestLog_defaultStatus(t *testing.T) {
	h := Log(newTestLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no explicit WriteHeader — implicit 200
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuth_modeNone_noHeader(t *testing.T) {
	called := false
	h := Auth(config.AuthNone)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Error("handler not called in AuthNone mode")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuth_modeForward_missingHeader(t *testing.T) {
	h := Auth(config.AuthForward)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_modeForward_withHeader(t *testing.T) {
	var user string
	h := Auth(config.AuthForward)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ = r.Context().Value(keyUser).(string)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Auth-User", "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if user != "alice" {
		t.Errorf("user in context = %q, want alice", user)
	}
}

func TestChain_order(t *testing.T) {
	var order []int
	mw := func(n int) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, n)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		mw(1), mw(2), mw(3),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []int{1, 2, 3}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i, v := range order {
		if v != want[i] {
			t.Errorf("order[%d] = %d, want %d", i, v, want[i])
		}
	}
}

func TestSharedArrayBuffer_setsHeaders(t *testing.T) {
	h := SharedArrayBuffer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", rec.Header().Get("Cross-Origin-Opener-Policy"))
	}
	if rec.Header().Get("Cross-Origin-Embedder-Policy") != "credentialless" {
		t.Errorf("COEP = %q, want credentialless", rec.Header().Get("Cross-Origin-Embedder-Policy"))
	}
}

func TestSharedArrayBuffer_callsNext(t *testing.T) {
	called := false
	h := SharedArrayBuffer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("next handler not called")
	}
}
