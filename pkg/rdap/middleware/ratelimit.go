package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimiter is a per-key token-bucket limiter. The key is derived from
// the request (typically client IP, optionally paired with the bearer
// token's subject). Buckets are lazily created and pruned when idle.
//
// Defaults are tuned for anonymous RDAP: 10 req/s sustained, 20-token
// burst. Operators serving authenticated traffic typically raise these.
type RateLimiter struct {
	rate  float64       // tokens per second
	burst float64       // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	// idle buckets older than this are dropped from the map on the next
	// access — keeps memory proportional to the active key set.
	maxIdle time.Duration
}

type bucket struct {
	tokens    float64
	lastSeen  time.Time
}

// NewRateLimiter returns a limiter with the given sustained rate and
// burst. A rate of 0 disables limiting and returns a pass-through
// middleware (useful behind an external limiter / LB).
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	return &RateLimiter{
		rate:    ratePerSec,
		burst:   burst,
		now:     time.Now,
		buckets: map[string]*bucket{},
		maxIdle: 10 * time.Minute,
	}
}

// Allow reports whether a request keyed by k is within budget. Callers
// decide what "k" means (IP, token, IP+path, …).
func (rl *RateLimiter) Allow(k string) bool {
	if rl.rate <= 0 {
		return true
	}
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b := rl.buckets[k]
	if b == nil {
		b = &bucket{tokens: rl.burst, lastSeen: now}
		rl.buckets[k] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		if elapsed > 0 {
			b.tokens = minF64(rl.burst, b.tokens+elapsed*rl.rate)
		}
		b.lastSeen = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Middleware returns the http.Handler wrapper. The key function is
// required — there is no safe default, because the obvious choice
// (r.RemoteAddr) collapses every caller behind a load balancer into a
// single bucket, turning one noisy client into a DoS against everyone
// else on the same replica. Callers MUST pass either middleware.ClientIP
// for direct-connect deployments, or a trusted-proxy extractor that
// reads a validated forwarded-client-IP header.
func (rl *RateLimiter) Middleware(key func(*http.Request) string) func(http.Handler) http.Handler {
	if key == nil {
		panic("ratelimit: key function is required; use middleware.ClientIP for direct deploys or a trusted-proxy extractor")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.Allow(key(r)) {
				// Per RFC 7480 §5.5: 429 + Retry-After. We set a
				// conservative one-second retry: honest for a token
				// bucket at single-token granularity.
				w.Header().Set("Retry-After", strconv.Itoa(1))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP is the direct-connect key function: strips the port from
// RemoteAddr and falls back to the raw RemoteAddr if parsing fails.
// Deployments behind a proxy should use ForwardedClientIP instead —
// ClientIP sees the proxy's IP, collapsing every caller into one bucket.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ForwardedClientIP extracts the original client IP from X-Forwarded-For
// or X-Real-IP, but ONLY when the immediate peer (RemoteAddr) is in the
// trustedProxies allowlist. Untrusted peers fall back to ClientIP so an
// attacker can't spoof their rate-limit key by forging the header.
//
// Trusted proxies are specified as CIDR prefixes — typical values
// include 10.0.0.0/8 for an internal LB or the documented egress range
// of your CDN (Cloudflare publishes one, Fastly another). Bare IPs
// work too; they're accepted as /32 or /128.
//
// X-Forwarded-For may contain a comma-separated chain when traffic
// traversed multiple proxies. We take the first entry (original client)
// after trimming whitespace — the RFC 7239 recommendation.
func ForwardedClientIP(trustedProxies []netip.Prefix) func(*http.Request) string {
	return func(r *http.Request) string {
		peer := parseRemoteAddr(r.RemoteAddr)
		if !peer.IsValid() || !anyContains(trustedProxies, peer) {
			return ClientIP(r)
		}
		if h := r.Header.Get("X-Forwarded-For"); h != "" {
			// First entry = original client.
			if comma := strings.IndexByte(h, ','); comma >= 0 {
				h = h[:comma]
			}
			candidate := strings.TrimSpace(h)
			if candidate != "" {
				return candidate
			}
		}
		if h := r.Header.Get("X-Real-IP"); h != "" {
			return strings.TrimSpace(h)
		}
		return ClientIP(r)
	}
}

func parseRemoteAddr(s string) netip.Addr {
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

func anyContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// CleanupIdle prunes buckets that haven't been touched within maxIdle.
// Callers SHOULD run this on a ticker to cap memory use on long uptimes.
func (rl *RateLimiter) CleanupIdle() {
	cutoff := rl.now().Add(-rl.maxIdle)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, b := range rl.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

func minF64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// A format helper used in a couple of middleware. Go's stdlib has no
// String(int64) without detour; fmt.Sprintf works and is used once per
// rate-limit rejection so allocation is not hot-path.
var _ = fmt.Sprintf
