// Package metrics is a tiny optional seam for observing the DataSource.
// The core ships an expvar-backed Observer (exposed at /debug/vars by
// anyone who imports expvar) so operators get something without adding
// dependencies. Prometheus users write a ~10-line adapter against the
// Observer interface — see PERFORMANCE.md for the canonical snippet.
package metrics

import (
	"context"
	"expvar"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// Observer is the hook surface. Implementations MUST be safe for
// concurrent use; the DataSource wrapper calls Observed from many
// goroutines.
type Observer interface {
	Observed(ctx context.Context, op string, d time.Duration, err error)
}

// WrapDataSource decorates any DataSource so every method call emits an
// observation. Returns the original DataSource unchanged when o is nil,
// so enabling metrics is a zero-cost conditional in main.
func WrapDataSource(ds datasource.DataSource, o Observer) datasource.DataSource {
	if o == nil {
		return ds
	}
	return &observedDS{inner: ds, o: o}
}

type observedDS struct {
	inner datasource.DataSource
	o     Observer
}

func (d *observedDS) GetDomain(ctx context.Context, name string) (*datasource.Domain, error) {
	start := time.Now()
	v, err := d.inner.GetDomain(ctx, name)
	d.o.Observed(ctx, "domain", time.Since(start), err)
	return v, err
}

func (d *observedDS) GetEntity(ctx context.Context, handle string) (*datasource.Contact, error) {
	start := time.Now()
	v, err := d.inner.GetEntity(ctx, handle)
	d.o.Observed(ctx, "entity", time.Since(start), err)
	return v, err
}

func (d *observedDS) GetNameserver(ctx context.Context, name string) (*datasource.Nameserver, error) {
	start := time.Now()
	v, err := d.inner.GetNameserver(ctx, name)
	d.o.Observed(ctx, "nameserver", time.Since(start), err)
	return v, err
}

func (d *observedDS) GetIPNetwork(ctx context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	start := time.Now()
	v, err := d.inner.GetIPNetwork(ctx, ip)
	d.o.Observed(ctx, "ip", time.Since(start), err)
	return v, err
}

// ExpvarObserver is an Observer that publishes counters via the stdlib
// expvar package. Import `net/http/expvar` (or mount /debug/vars) to
// expose the counters as JSON. Sufficient for smoke tests and small
// deployments; serious operators attach a Prometheus adapter instead.
type ExpvarObserver struct {
	total   *expvar.Map // per-op hit counters
	errors  *expvar.Map // per-op error counters
	latency *expvar.Map // per-op accumulated latency (ns)
}

// NewExpvarObserver registers counters under the given namespace. Each
// namespace may be constructed once per process; re-registering panics.
func NewExpvarObserver(namespace string) *ExpvarObserver {
	pkg := expvar.NewMap(namespace)
	o := &ExpvarObserver{
		total:   new(expvar.Map).Init(),
		errors:  new(expvar.Map).Init(),
		latency: new(expvar.Map).Init(),
	}
	pkg.Set("total", o.total)
	pkg.Set("errors", o.errors)
	pkg.Set("latency_ns", o.latency)
	return o
}

func (o *ExpvarObserver) Observed(_ context.Context, op string, d time.Duration, err error) {
	o.total.Add(op, 1)
	o.latency.Add(op, d.Nanoseconds())
	if err != nil {
		o.errors.Add(op, 1)
	}
}

// --- Cache observer -----------------------------------------------------

// CacheObserver is a lightweight hit/miss counter for a cache layer.
// The cache package does not import this — it would create a cycle —
// but callers can attach it by wrapping the cached DataSource manually.
// Useful alongside WrapDataSource to compute hit-ratio over time.
type CacheObserver struct {
	hits, misses atomic.Int64
}

func (c *CacheObserver) RecordHit()  { c.hits.Add(1) }
func (c *CacheObserver) RecordMiss() { c.misses.Add(1) }

// Ratio reports the hit ratio over the lifetime of the counter. Returns
// 0 when there have been no queries.
func (c *CacheObserver) Ratio() float64 {
	h, m := c.hits.Load(), c.misses.Load()
	if h+m == 0 {
		return 0
	}
	return float64(h) / float64(h+m)
}
