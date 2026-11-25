package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"noteroot/internal/notebooks"
)

// NotebookBreaker fails fast for a notebook whose breaker is open: a
// notebook that keeps failing at runtime (bad index, broken vault,
// unresponsive plugin) stops being retried request after request, and
// the failure never reaches the handler — so a slow or broken notebook
// cannot keep consuming latency and goroutines at everyone else's
// expense. Must run after routing (it reads the matched {slug} path
// value) and, for authenticated routes, after auth — an unauthorized
// caller must not learn anything about another notebook's health, and
// must not be able to add strikes to it.
//
// A response status >= 500 counts as a strike (this includes a
// plugin/integration breaker's own 503 — persistent 5xx from those
// subsystems is exactly the "unresponsive plugin" case this breaker
// exists to isolate); 2xx/3xx closes the circuit back up. A 4xx is
// deliberately neutral — it is the caller's fault, not a verdict on
// the notebook, and must not be a way for an unauthenticated or
// unauthorized caller to reset (or pad strikes toward tripping)
// another notebook's breaker from outside.
func NotebookBreaker(b *notebooks.Breakers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("slug")
			if slug == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !breakerGuard(b, slug, w) {
				return
			}
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(sw, r)
			recordBreakerResult(b, slug, r, sw.code)
		})
	}
}

// breakerGuard writes a 503 and returns false if slug's breaker is
// open; otherwise it returns true and the caller may proceed.
func breakerGuard(b *notebooks.Breakers, slug string, w http.ResponseWriter) bool {
	if b.Allow(slug) {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(b.Cooldown().Seconds())))
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "notebook temporarily unavailable",
	})
	return false
}

// recordBreakerResult classifies one response against slug's breaker:
// see the NotebookBreaker doc comment for the 5xx/4xx/2xx-3xx rules.
func recordBreakerResult(b *notebooks.Breakers, slug string, r *http.Request, code int) {
	switch {
	case code >= 500:
		b.Failure(slug, fmt.Sprintf("%s %s: %d", r.Method, r.URL.Path, code))
	case code < 400:
		b.Success(slug)
	}
}
