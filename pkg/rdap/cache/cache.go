// Package cache wraps any DataSource with an in-memory, TTL-bounded LRU
// plus singleflight de-duplication. At 10k+ QPS with the typical skewed
// RDAP read pattern (a small set of popular names drives most traffic),
// this pushes the DB hit rate down 5-10× without introducing external
// infrastructure. Callers who need a shared cache across replicas can
// layer Redis/Memcached as a second decorator.
//
// IMPORTANT — PII and cache safety.
//
// Cached entries are the RAW datasource records: full contact detail
// (names, emails, postal addresses, extras JSONB). Redaction happens in
// the mapper, on the way out. This is fast, but it means PII is present
// in the process's working set for the entire TTL. Two consequences:
//
//  1. Any code path that serialises a cached value without going through
//     mapper.Redact / mapper.Domain WILL leak PII. The cache exposes the
//     datasource.DataSource interface and nothing else; keep it that way.
//
//  2. Memory dumps / core files from a caching process contain PII.
//     Operators who care about this should disable core dumps in prod
//     and consider response-cache instead of record-cache (see
//     PERFORMANCE.md for the L2 caching recipe).
//
// A safer long-term design is a *response* cache keyed by
// (object, id, access_level). That change is tracked in the roadmap;
// for now the invariant above is audited by code review.
package cache

import (
	"container/list"
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// Config controls cache behaviour. Zero values are rejected by New; the
// caller must pick deliberate numbers.
type Config struct {
	Size    int           // max entries per object class
	TTL     time.Duration // time a cached hit stays valid
	NegTTL  time.Duration // time a NotFound stays cached (typical: shorter than TTL)
	NowFunc func() time.Time

	// StaleTTL enables stale-while-revalidate: after TTL expires, the
	// cache returns the stale entry for up to StaleTTL while a
	// background goroutine refreshes the key via singleflight. Without
	// SWR, a popular key causes a latency cliff on TTL expiry — one
	// request waits for the DB, everyone else piles up in singleflight.
	// Set to 0 to keep the old hard-TTL behaviour.
	StaleTTL time.Duration
}

// DataSource is a caching wrapper. The zero value is not usable.
type DataSource struct {
	inner  datasource.DataSource
	cfg    Config
	now    func() time.Time

	domains    *lru
	entities   *lru
	nameserver *lru
	networks   *lru

	sf singleflight
}

// New wraps inner with a cache. Panics if cfg is not usable — a
// misconfigured cache is a programming error, not a runtime condition.
func New(inner datasource.DataSource, cfg Config) *DataSource {
	if cfg.Size <= 0 || cfg.TTL <= 0 {
		panic("cache: Config.Size and Config.TTL must be positive")
	}
	if cfg.NegTTL <= 0 {
		cfg.NegTTL = cfg.TTL / 2
	}
	now := cfg.NowFunc
	if now == nil {
		now = time.Now
	}
	return &DataSource{
		inner:      inner,
		cfg:        cfg,
		now:        now,
		domains:    newLRU(cfg.Size),
		entities:   newLRU(cfg.Size),
		nameserver: newLRU(cfg.Size),
		networks:   newLRU(cfg.Size),
	}
}

type cached[V any] struct {
	val     *V
	err     error
	expires time.Time
}

func (d *DataSource) ttlFor(err error) time.Duration {
	if err != nil {
		return d.cfg.NegTTL
	}
	return d.cfg.TTL
}

func getOrLoad[V any](ctx context.Context, d *DataSource, bucket *lru, key string,
	fetch func(ctx context.Context, k string) (*V, error)) (*V, error) {
	now := d.now()
	if raw, ok := bucket.get(key); ok {
		c := raw.(cached[V])
		// Fresh window: serve directly.
		if now.Before(c.expires) {
			return c.val, c.err
		}
		// Stale-while-revalidate window: serve the stale value and
		// asynchronously refresh. Only one refresh per key is
		// triggered, thanks to singleflight.
		if d.cfg.StaleTTL > 0 && now.Before(c.expires.Add(d.cfg.StaleTTL)) {
			go refreshAsync(d, bucket, key, fetch)
			return c.val, c.err
		}
		bucket.remove(key)
	}
	// Singleflight collapses concurrent misses on the same key into one
	// backend call. Without it, a cold popular key can multiply load
	// by the number of in-flight callers.
	v, err, _ := d.sf.do(key, func() (any, error) {
		val, err := fetch(context.Background(), key)
		return val, err
	})
	var out *V
	if v != nil {
		out = v.(*V)
	}
	bucket.put(key, cached[V]{val: out, err: err, expires: d.now().Add(d.ttlFor(err))})
	return out, err
}

// refreshAsync is the async arm of stale-while-revalidate. Uses a
// detached context (independent of the triggering request) because
// the request has already returned by the time this runs. Generic
// package-level function because Go doesn't allow generic methods on
// non-generic types.
func refreshAsync[V any](d *DataSource, bucket *lru, key string, fetch func(ctx context.Context, k string) (*V, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err, _ := d.sf.do(key, func() (any, error) {
		val, err := fetch(ctx, key)
		return val, err
	})
	var out *V
	if v != nil {
		out = v.(*V)
	}
	bucket.put(key, cached[V]{val: out, err: err, expires: d.now().Add(d.ttlFor(err))})
}

func (d *DataSource) GetDomain(ctx context.Context, name string) (*datasource.Domain, error) {
	return getOrLoad[datasource.Domain](ctx, d, d.domains, name, d.inner.GetDomain)
}

func (d *DataSource) GetEntity(ctx context.Context, handle string) (*datasource.Contact, error) {
	return getOrLoad[datasource.Contact](ctx, d, d.entities, handle, d.inner.GetEntity)
}

func (d *DataSource) GetNameserver(ctx context.Context, name string) (*datasource.Nameserver, error) {
	return getOrLoad[datasource.Nameserver](ctx, d, d.nameserver, name, d.inner.GetNameserver)
}

func (d *DataSource) GetIPNetwork(ctx context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	key := ip.String()
	return getOrLoad[datasource.IPNetwork](ctx, d, d.networks, key,
		func(ctx context.Context, _ string) (*datasource.IPNetwork, error) {
			return d.inner.GetIPNetwork(ctx, ip)
		})
}

// Stats is the observable state of the cache. Atomic reads — safe to call
// concurrently. Useful for /debug/vars or a Prometheus exporter.
type Stats struct {
	Domains    int
	Entities   int
	Nameserver int
	Networks   int
}

func (d *DataSource) Stats() Stats {
	return Stats{
		Domains:    d.domains.len(),
		Entities:   d.entities.len(),
		Nameserver: d.nameserver.len(),
		Networks:   d.networks.len(),
	}
}

// -----------------------------------------------------------------------
// LRU — deliberately not a third-party dependency.
// -----------------------------------------------------------------------

type lru struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type lruEntry struct {
	key string
	val any
}

func newLRU(capacity int) *lru {
	return &lru{capacity: capacity, items: map[string]*list.Element{}, order: list.New()}
}

func (l *lru) get(key string) (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.order.MoveToFront(el)
		return el.Value.(*lruEntry).val, true
	}
	return nil, false
}

func (l *lru) put(key string, val any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		el.Value.(*lruEntry).val = val
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&lruEntry{key: key, val: val})
	l.items[key] = el
	for l.order.Len() > l.capacity {
		last := l.order.Back()
		delete(l.items, last.Value.(*lruEntry).key)
		l.order.Remove(last)
	}
}

func (l *lru) remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.order.Remove(el)
		delete(l.items, key)
	}
}

func (l *lru) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// -----------------------------------------------------------------------
// Singleflight — collapses concurrent calls with the same key into one.
// Stripped-down port of golang.org/x/sync/singleflight; kept here to
// avoid pulling in the whole module for one function.
// -----------------------------------------------------------------------

type singleflight struct {
	mu    sync.Mutex
	calls map[string]*sfCall
}

type sfCall struct {
	wg  sync.WaitGroup
	val any
	err error
}

func (sf *singleflight) do(key string, fn func() (any, error)) (any, error, bool) {
	sf.mu.Lock()
	if sf.calls == nil {
		sf.calls = map[string]*sfCall{}
	}
	if c, ok := sf.calls[key]; ok {
		sf.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err, true
	}
	c := &sfCall{}
	c.wg.Add(1)
	sf.calls[key] = c
	sf.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	sf.mu.Lock()
	delete(sf.calls, key)
	sf.mu.Unlock()
	return c.val, c.err, false
}
