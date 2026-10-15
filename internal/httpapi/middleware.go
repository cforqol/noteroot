package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"noteroot/internal/config"
)

type ctxKey string

const (
	keyRequestID ctxKey = "request_id"
	keyUser      ctxKey = "user"
)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "err", rec, "path", r.URL.Path)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

func Log(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("req", "method", r.Method, "path", r.URL.Path,
				"status", sw.code, "dur_ms", time.Since(start).Milliseconds())
		})
	}
}

// Auth trusts Traefik forward-auth, which strips client-supplied
// headers and sets X-Auth-User after successful authentication.
func Auth(mode config.AuthMode) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mode == config.AuthNone {
				next.ServeHTTP(w, r)
				return
			}
			user := r.Header.Get("X-Auth-User")
			if user == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyUser, user)))
		})
	}
}

func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// CSRF guards mutating routes against cross-site form and fetch attacks.
// Browsers cannot send these content types cross-origin without a CORS
// preflight, which the server never grants; top-level navigations and forms
// always carry an Origin or Referer that must match. Bare API clients send
// neither header and pass through.
func CSRF(publicOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ct := r.Header.Get("Content-Type"); ct != "" {
				media := ct
				if i := strings.IndexByte(ct, ';'); i >= 0 {
					media = ct[:i]
				}
				if media = strings.TrimSpace(media); media == "application/x-www-form-urlencoded" ||
					media == "multipart/form-data" {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden content type"})
					return
				}
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if !originAllowed(origin, publicOrigin, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin forbidden"})
					return
				}
			} else if ref := r.Header.Get("Referer"); ref != "" {
				if !originAllowed(ref, publicOrigin, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin forbidden"})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(value, publicOrigin, host string) bool {
	if publicOrigin != "" {
		return value == publicOrigin
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == host
}
