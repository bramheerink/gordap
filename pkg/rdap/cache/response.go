package cache

// The ResponseCache in this file is the PII-safe cache layer.
//
// Ordering contract — CRITICAL, audited by code review.
//
// A response MUST have passed through the tier-aware redactor before
// any byte of it reaches ResponseCache.Put. The only sanctioned
// insertion path is via the response-cache HTTP middleware in
// pkg/rdap/handlers/response_cache.go, which arranges the ordering:
//
//	1. auth.Middleware decodes the bearer token and places the
//	   caller's AccessLevel on the request context.
//	2. responseCacheMiddleware computes the cache key
//	   (object, id, tier) and checks for a hit. On hit, serve. On
//	   miss, install a captureWriter and call next.
//	3. The matching HTTP handler renders the response:
//	      a. pulls the raw record from the DataSource;
//	      b. invokes mapper.Domain / mapper.Entity / …, which calls
//	         mapper.RedactContact with the caller's AccessLevel;
//	      c. JSON-encodes the redacted view into the captureWriter.
//	4. captureWriter tees the bytes to both the real ResponseWriter
//	   (the client sees the response immediately) AND its internal
//	   buffer (for cache storage).
//	5. Only after the handler returns and the status is 2xx does
//	   ResponseCache.Put get called.
//
// The invariant any contributor MUST preserve:
//
//	No code path may call ResponseCache.Put without first running
//	mapper.Redact on the source data for the access tier encoded in
//	the cache key.
//
// Because the cache key includes the tier, a stored body is already
// appropriate for its key — a privileged entry can hold privileged
// JSON, an anonymous entry can hold anonymous JSON, and the
// middleware never serves an entry across tiers. No crossover, no
// PII leak from a warm cache to a colder caller.

import (
	"container/list"
	"sync"
	"time"
)

// ResponseCache is a PII-safer sibling of the record-level cache above.
// It stores already-rendered JSON bodies keyed by
// (object-class, id, access-tier). Because the values are the final
// redacted wire responses, there is no PII in the cache's working set —
// a memory dump or stray goroutine leak can only reveal what was
// already public to callers at that tier.
//
// Trade-off versus the record cache:
//   - Higher memory use for sites serving many access tiers (3× for
//     Anonymous/Authenticated/Privileged fan-out).
//   - No hit across tiers (privileged miss doesn't warm anonymous).
//   - Zero PII exposure surface.
//
// Deployments at scale should run both: record cache as a thin buffer,
// response cache as the front line.
type ResponseCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time

	items map[key]*list.Element
	order *list.List
}

type key struct {
	object string
	id     string
	tier   string
}

type entry struct {
	k       key
	body    []byte
	status  int
	headers map[string]string
	expires time.Time
}

// NewResponseCache constructs a ResponseCache. A size of 0 disables the
// cache (every Get reports a miss and Put is a no-op); handy behind a
// feature flag.
func NewResponseCache(size int, ttl time.Duration) *ResponseCache {
	return &ResponseCache{
		capacity: size,
		ttl:      ttl,
		now:      time.Now,
		items:    map[key]*list.Element{},
		order:    list.New(),
	}
}

// Get returns the cached (body, status, headers, true) or (_, _, _, false).
func (r *ResponseCache) Get(object, id, tier string) ([]byte, int, map[string]string, bool) {
	if r == nil || r.capacity == 0 {
		return nil, 0, nil, false
	}
	k := key{object, id, tier}
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.items[k]
	if !ok {
		return nil, 0, nil, false
	}
	e := el.Value.(*entry)
	if r.now().After(e.expires) {
		r.order.Remove(el)
		delete(r.items, k)
		return nil, 0, nil, false
	}
	r.order.MoveToFront(el)
	return e.body, e.status, e.headers, true
}

// Put stores a rendered response. Headers are copied defensively so
// subsequent mutations on the caller side don't leak in.
func (r *ResponseCache) Put(object, id, tier string, body []byte, status int, headers map[string]string) {
	if r == nil || r.capacity == 0 {
		return
	}
	k := key{object, id, tier}
	h := make(map[string]string, len(headers))
	for kk, vv := range headers {
		h[kk] = vv
	}
	// Defensive body copy — otherwise the caller's buffer ownership
	// story becomes part of this cache's contract.
	buf := make([]byte, len(body))
	copy(buf, body)

	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.items[k]; ok {
		el.Value = &entry{k: k, body: buf, status: status, headers: h, expires: r.now().Add(r.ttl)}
		r.order.MoveToFront(el)
		return
	}
	el := r.order.PushFront(&entry{k: k, body: buf, status: status, headers: h, expires: r.now().Add(r.ttl)})
	r.items[k] = el
	for r.order.Len() > r.capacity {
		last := r.order.Back()
		delete(r.items, last.Value.(*entry).k)
		r.order.Remove(last)
	}
}

// Len reports the current entry count — useful for metrics.
func (r *ResponseCache) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order.Len()
}
