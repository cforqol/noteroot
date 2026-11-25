package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"noteroot/internal/notebooks"
)

// breakerTestMux wires NotebookBreaker in front of a handler whose
// status code (and call count) the test controls, routed through a
// real *http.ServeMux so r.PathValue("slug") is populated exactly as
// it is in the real server.
func breakerTestMux(b *notebooks.Breakers, code *int32, calls *int32) *http.ServeMux {
	mux := http.NewServeMux()
	h := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.WriteHeader(int(atomic.LoadInt32(code)))
	}
	mux.Handle("GET /n/{slug}/thing", NotebookBreaker(b)(http.HandlerFunc(h)))
	return mux
}

func doBreakerReq(mux *http.ServeMux, slug string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/"+slug+"/thing", nil))
	return rec
}

func TestNotebookBreaker_passesThroughUntilThreshold(t *testing.T) {
	b := notebooks.NewBreakers(3, time.Minute)
	var code, calls int32 = 500, 0
	mux := breakerTestMux(b, &code, &calls)

	for i := 0; i < 3; i++ {
		rec := doBreakerReq(mux, "work")
		if rec.Code != 500 {
			t.Fatalf("request %d: status = %d, want 500 (handler should still run)", i, rec.Code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("handler called %d times, want 3", got)
	}
}

func TestNotebookBreaker_opensAfterThresholdWithoutCallingHandler(t *testing.T) {
	b := notebooks.NewBreakers(3, time.Minute)
	var code, calls int32 = 500, 0
	mux := breakerTestMux(b, &code, &calls)

	for i := 0; i < 3; i++ {
		doBreakerReq(mux, "work")
	}
	rec := doBreakerReq(mux, "work")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 once breaker is open", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on breaker-open response")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("handler called %d times after breaker opened, want 3 (no further calls)", got)
	}
}

func TestNotebookBreaker_fourXXIsNeutral(t *testing.T) {
	b := notebooks.NewBreakers(2, time.Minute)
	var code, calls int32 = 404, 0
	mux := breakerTestMux(b, &code, &calls)

	// 4xx must neither trip the breaker nor reset it: a flood of
	// not-found/forbidden responses must not be usable to mask real
	// failures recorded elsewhere, nor to pad strikes toward a trip.
	for i := 0; i < 10; i++ {
		rec := doBreakerReq(mux, "work")
		if rec.Code != 404 {
			t.Fatalf("request %d: status = %d, want 404 passthrough", i, rec.Code)
		}
	}
	if st := b.State("work"); st.Strikes != 0 || st.Open {
		t.Errorf("state after only 4xx responses = %+v, want untouched", st)
	}
}

func TestNotebookBreaker_successClosesAfterFailures(t *testing.T) {
	b := notebooks.NewBreakers(3, time.Minute)
	var code, calls int32 = 500, 0
	mux := breakerTestMux(b, &code, &calls)

	doBreakerReq(mux, "work")
	doBreakerReq(mux, "work")
	atomic.StoreInt32(&code, 200)
	doBreakerReq(mux, "work")
	if st := b.State("work"); st.Strikes != 0 {
		t.Errorf("strikes after a success = %d, want 0", st.Strikes)
	}
}

func TestNotebookBreaker_isolatesNotebooks(t *testing.T) {
	b := notebooks.NewBreakers(2, time.Minute)
	var code, calls int32 = 500, 0
	mux := breakerTestMux(b, &code, &calls)

	doBreakerReq(mux, "bad")
	doBreakerReq(mux, "bad")
	if rec := doBreakerReq(mux, "bad"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bad notebook status = %d, want 503", rec.Code)
	}
	atomic.StoreInt32(&code, 200)
	if rec := doBreakerReq(mux, "good"); rec.Code != 200 {
		t.Fatalf("unrelated healthy notebook status = %d, want 200", rec.Code)
	}
}

func TestNotebookBreaker_halfOpenProbeAfterCooldown(t *testing.T) {
	b := notebooks.NewBreakers(1, 10*time.Millisecond)
	var code, calls int32 = 500, 0
	mux := breakerTestMux(b, &code, &calls)

	doBreakerReq(mux, "work") // trips it
	if rec := doBreakerReq(mux, "work"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while open", rec.Code)
	}
	time.Sleep(20 * time.Millisecond)
	atomic.StoreInt32(&code, 200)
	rec := doBreakerReq(mux, "work")
	if rec.Code != 200 {
		t.Fatalf("probe status = %d, want 200 (handler should run once cooldown elapses)", rec.Code)
	}
	if st := b.State("work"); st.Open {
		t.Error("breaker still open after a successful half-open probe")
	}
}

func TestNotebookBreaker_noSlugPassesThrough(t *testing.T) {
	b := notebooks.NewBreakers(1, time.Minute)
	mux := http.NewServeMux()
	mux.Handle("GET /no-slug", NotebookBreaker(b)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-slug", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
