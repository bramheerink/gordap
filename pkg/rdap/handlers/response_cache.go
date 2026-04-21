package handlers

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/cache"
)

// responseCacheMiddleware fronts the RDAP handlers with an already-
// rendered JSON cache keyed by (object, id, access-tier). Unlike the
// record-level cache, this stores the post-redaction response body —
// no PII ever sits in the cache's working set, regardless of the
// caller's tier.
//
// The middleware sits inside the auth layer (so it can read
// auth.Claims from the request context) and outside the gzip layer
// (so it operates on the canonical, non-compressed representation).
// Concurrent misses on the same key still rely on the record-cache's
// singleflight further down the stack.
func responseCacheMiddleware(rc *cache.ResponseCache) func(http.Handler) http.Handler {
	if rc == nil {
		return noopMW
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			// Respect RFC 7480 §4 Accept-negotiation even on cached
			// routes: an incompatible Accept must yield 406 from the
			// handler, not a stale 200 body.
			if !accepts(r) {
				next.ServeHTTP(w, r)
				return
			}
			object, id := classifyCacheable(r.URL.Path)
			if object == "" {
				next.ServeHTTP(w, r)
				return
			}
			tier := auth.FromContext(r.Context()).Level.String()

			if body, status, headers, ok := rc.Get(object, id, tier); ok {
				for k, v := range headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("X-Gordap-Cache", "HIT")
				w.WriteHeader(status)
				_, _ = w.Write(body)
				return
			}

			cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(cw, r)

			// Only cache clean responses — errors, redirects and
			// redacted-to-empty payloads are not worth a cache slot.
			if cw.status >= 200 && cw.status < 300 && cw.body.Len() > 0 {
				rc.Put(object, id, tier,
					cw.body.Bytes(), cw.status, snapshotHeaders(w.Header()))
			}
		})
	}
}

func noopMW(next http.Handler) http.Handler { return next }

// classifyCacheable returns (object class, identifier) for the RDAP
// paths worth caching. Search endpoints (/domains, /entities, …) and
// everything else return ("", "") so the middleware short-circuits.
func classifyCacheable(path string) (object, id string) {
	switch {
	case path == "/help":
		return "help", ""
	case strings.HasPrefix(path, "/domain/") && !strings.HasPrefix(path, "/domains"):
		return "domain", strings.TrimPrefix(path, "/domain/")
	case strings.HasPrefix(path, "/entity/") && !strings.HasPrefix(path, "/entities"):
		return "entity", strings.TrimPrefix(path, "/entity/")
	case strings.HasPrefix(path, "/nameserver/") && !strings.HasPrefix(path, "/nameservers"):
		return "nameserver", strings.TrimPrefix(path, "/nameserver/")
	case strings.HasPrefix(path, "/ip/"):
		return "ip", strings.TrimPrefix(path, "/ip/")
	default:
		return "", ""
	}
}

// snapshotHeaders copies the headers we care about into a plain map
// the ResponseCache can store. Headers that vary per-request
// (Date, traceparent) are explicitly not included.
func snapshotHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// captureWriter teed writes into a buffer so the response-cache
// middleware can store the body after the handler finishes. Writes
// still reach the real ResponseWriter immediately — the client sees
// the response as soon as possible, caching is a side-effect.
type captureWriter struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.body.Write(p)
	return c.ResponseWriter.Write(p)
}
