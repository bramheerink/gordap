package jwks

import (
	"sync"
	"time"
)

// replayCache tracks recently-seen JWT IDs (jti) to make a single
// stolen token unusable after its first successful verification.
//
// Scope — honest about limits:
//   - Single-replica: effective. A replayed token will be rejected.
//   - Multi-replica: best-effort. Each replica has its own cache;
//     an attacker who captures a token and replays against a
//     different replica within TTL will succeed. Deployments that
//     need cross-replica replay protection should point the RDAP
//     cluster at a shared store (Redis / Memcached) via a custom
//     Verifier wrapping jwks.Verifier.
//
// Tokens with no `jti` claim are NOT checked — the attack surface
// is identical to a world without this cache. Operators who require
// every token to carry a jti should reject un-jti'd tokens in their
// IdP config, not in the server.
type replayCache struct {
	mu    sync.Mutex
	seen  map[string]time.Time // jti → expiry time (original exp)
	prune time.Time            // next wholesale prune deadline
}

func newReplayCache() *replayCache {
	return &replayCache{seen: map[string]time.Time{}}
}

// check reports whether the jti has been seen before (replay) and
// records it when it hasn't. exp is the token's own expiry; entries
// are discarded once we pass that point plus leeway, because a token
// past exp is already rejected upstream by the main verifier.
func (r *replayCache) check(jti string, exp time.Time) (replay bool) {
	if jti == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if r.prune.IsZero() || now.After(r.prune) {
		for k, e := range r.seen {
			if now.After(e) {
				delete(r.seen, k)
			}
		}
		r.prune = now.Add(time.Minute)
	}

	if existing, ok := r.seen[jti]; ok && now.Before(existing) {
		return true
	}
	r.seen[jti] = exp
	return false
}
