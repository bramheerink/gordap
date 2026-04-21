// Package middleware ships the production plumbing operators expect on
// top of the raw handlers: CORS, rate limiting, response compression,
// request-body size limits, and request timeouts. Each middleware is a
// plain func(http.Handler) http.Handler so they compose like every other
// Go middleware.
package middleware

import "net/http"

// CORS emits the wildcard Access-Control-* headers required by
// RFC 7480 §5.6 / ICANN TIG v2.2 §1.10 on every response, and handles the
// preflight OPTIONS request so browser-based RDAP clients work.
//
// RDAP is strictly public by design; the wildcard is intentional and
// matches what Verisign/ICANN/openrdap all ship.
func CORS() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Accept, Accept-Language")
			h.Set("Access-Control-Max-Age", "86400")
			h.Set("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
