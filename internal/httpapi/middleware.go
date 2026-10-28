package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
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

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take first IP in chain
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				ip := strings.TrimSpace(xff[:i])
				if ip != "" {
					return ip
				}
				break
			}
		}
		ip := strings.TrimSpace(xff)
		if ip != "" {
			return ip
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		ip := strings.TrimSpace(xrip)
		if ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func Log(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(sw, r)
			user, _ := r.Context().Value(keyUser).(string)
			ip := clientIP(r)
			attrs := []any{"method", r.Method, "path", r.URL.Path,
				"status", sw.code, "dur_ms", time.Since(start).Milliseconds(),
				"ip", ip}
			if user != "" {
				attrs = append(attrs, "user", user)
			} else if r.Header.Get("X-Auth-User") != "" {
				attrs = append(attrs, "user", r.Header.Get("X-Auth-User"))
			}
			// Try to resolve hostname (best effort, short timeout not critical)
			if host, err := net.LookupAddr(ip); err == nil && len(host) > 0 {
				attrs = append(attrs, "hostname", strings.TrimSuffix(host[0], "."))
			}
			log.Info("req", attrs...)
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
	return csrfWithOpts(publicOrigin, false)
}

// CSRFAllowMultipart is like CSRF but permits multipart/form-data. File
// uploads (drag-and-drop) use multipart from same-origin fetch, which
// carries a matching Origin; the content-type block only guards against
// cross-origin HTML forms, which the Origin check already covers.
func CSRFAllowMultipart(publicOrigin string) func(http.Handler) http.Handler {
	return csrfWithOpts(publicOrigin, true)
}

func csrfWithOpts(publicOrigin string, allowMultipart bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ct := r.Header.Get("Content-Type"); ct != "" {
				media := ct
				if i := strings.IndexByte(ct, ';'); i >= 0 {
					media = ct[:i]
				}
				if media = strings.TrimSpace(media); media == "application/x-www-form-urlencoded" ||
					(!allowMultipart && media == "multipart/form-data") {
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

// SharedArrayBuffer enables SharedArrayBuffer for vim.wasm (Web Worker +
// Atomics blocking wait). Browsers require Cross-Origin-Opener-Policy and
// Cross-Origin-Embedder-Policy headers to expose SharedArrayBuffer.
func SharedArrayBuffer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "credentialless")
		next.ServeHTTP(w, r)
	})
}
