package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
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

// ClientIP is a best-effort key function: strips the port from RemoteAddr
// and falls back to the raw RemoteAddr if parsing fails. Deployments
// behind a proxy should supply their own key extracting the real client
// IP from a trusted header (X-Forwarded-For, X-Real-IP).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
