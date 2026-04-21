package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Gzip compresses the response body when the client advertises support
// and the body is worth compressing. RDAP JSON is highly redundant — a
// typical domain response shrinks by ~70%, which at 10k QPS is the
// difference between saturating a 1 Gbit uplink and not.
//
// The middleware refuses to compress already-compressed media, avoids
// compressing small responses (where the per-response overhead isn't
// recouped), and sets Vary: Accept-Encoding so caches key correctly.
func Gzip(minSize int) func(http.Handler) http.Handler {
	if minSize < 0 {
		minSize = 0
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Accept-Encoding")
			if !acceptsGzip(r) {
				next.ServeHTTP(w, r)
				return
			}

			gzw := newGzipWriter(w, minSize)
			defer gzw.Close()
			next.ServeHTTP(gzw, r)
		})
	}
}

func acceptsGzip(r *http.Request) bool {
	for _, v := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(v, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

// gzipWriter buffers writes until we've seen enough bytes to justify
// compression. If the total stays below minSize, the buffer is flushed
// plain. Once the threshold is crossed, writes stream through gzip.
type gzipWriter struct {
	w         http.ResponseWriter
	minSize   int
	buf       []byte
	status    int
	gz        *gzip.Writer
	committed bool
	plain     bool // threshold wasn't reached; pass-through mode
}

var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

func newGzipWriter(w http.ResponseWriter, minSize int) *gzipWriter {
	return &gzipWriter{w: w, minSize: minSize, status: http.StatusOK}
}

func (g *gzipWriter) Header() http.Header { return g.w.Header() }

func (g *gzipWriter) WriteHeader(code int) {
	g.status = code
	// Defer the actual WriteHeader until we've decided whether to
	// advertise Content-Encoding.
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if g.committed {
		if g.plain {
			return g.w.Write(p)
		}
		return g.gz.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < g.minSize {
		return len(p), nil
	}
	g.commit()
	return len(p), nil
}

func (g *gzipWriter) commit() {
	g.committed = true
	// Don't compress responses the upstream has marked as an image, or
	// where the caller has set a pre-compressed encoding. Best-effort.
	ct := g.w.Header().Get("Content-Type")
	if alreadyCompressible(ct) && len(g.buf) >= g.minSize {
		g.w.Header().Set("Content-Encoding", "gzip")
		g.w.Header().Del("Content-Length") // length changes post-compression
		g.w.WriteHeader(g.status)
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(g.w)
		g.gz = gz
		_, _ = g.gz.Write(g.buf)
		g.buf = nil
		return
	}
	// Fall back: write plainly.
	g.plain = true
	g.w.WriteHeader(g.status)
	_, _ = g.w.Write(g.buf)
	g.buf = nil
}

func (g *gzipWriter) Close() {
	if !g.committed {
		// Below threshold — write buffered body as-is.
		g.plain = true
		g.w.WriteHeader(g.status)
		_, _ = g.w.Write(g.buf)
		return
	}
	if g.gz != nil {
		_ = g.gz.Close()
		gzipPool.Put(g.gz)
	}
}

// alreadyCompressible returns true for text-ish payloads that benefit
// from gzip. We only know the Content-Type the upstream set — when empty
// (not set), default to "compress" because the handler has explicit
// control over the header.
func alreadyCompressible(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if ct == "" {
		return true
	}
	return strings.HasPrefix(ct, "text/") ||
		strings.HasPrefix(ct, "application/json") ||
		strings.HasPrefix(ct, "application/rdap+json") ||
		strings.HasPrefix(ct, "application/xml") ||
		strings.HasPrefix(ct, "application/javascript")
}
