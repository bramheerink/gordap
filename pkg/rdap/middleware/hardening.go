package middleware

import (
	"context"
	"net/http"
	"time"
)

// MaxRequestBody caps the request body size. RDAP is GET-only in the
// server profile, so this is a belt-and-braces guard against rogue
// clients. Go's http.Server.ReadHeaderTimeout handles header floods.
func MaxRequestBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// RequestTimeout cancels the request context after the given duration.
// Any DB lookup / backend call rooted in that context aborts cleanly.
// Deployments should set this to a value noticeably below any upstream
// proxy timeout so we return 503 before the proxy 504s.
func RequestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SecurityHeaders applies a minimal hardening baseline. RDAP responses
// are JSON — not rendered — so the usual CSP/XFO headers would be noise,
// but the couple below matter:
//   - Strict-Transport-Security: 180d, preload-eligible — relevant when
//     the server terminates TLS directly. Harmless behind a proxy.
//   - Referrer-Policy: no-referrer — we never want the URL leaking.
//   - X-Content-Type-Options: nosniff — protects clients that do render.
func SecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Strict-Transport-Security", "max-age=15552000")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
}
