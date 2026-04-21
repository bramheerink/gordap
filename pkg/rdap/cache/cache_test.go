package cache

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// counterDS counts calls to each method. Any lookup returns a stub record.
type counterDS struct {
	domainCalls atomic.Int64
	entityCalls atomic.Int64
	nsCalls     atomic.Int64
	ipCalls     atomic.Int64
	err         error
}

func (c *counterDS) GetDomain(_ context.Context, name string) (*datasource.Domain, error) {
	c.domainCalls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &datasource.Domain{Handle: "DOM-" + name, LDHName: name}, nil
}
func (c *counterDS) GetEntity(_ context.Context, h string) (*datasource.Contact, error) {
	c.entityCalls.Add(1)
	return &datasource.Contact{Handle: h}, nil
}
func (c *counterDS) GetNameserver(_ context.Context, n string) (*datasource.Nameserver, error) {
	c.nsCalls.Add(1)
	return &datasource.Nameserver{LDHName: n}, nil
}
func (c *counterDS) GetIPNetwork(_ context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	c.ipCalls.Add(1)
	return &datasource.IPNetwork{Prefix: netip.PrefixFrom(ip, 24)}, nil
}

func TestCache_HitsReduceBackendCalls(t *testing.T) {
	ds := &counterDS{}
	c := New(ds, Config{Size: 16, TTL: time.Minute})
	for i := 0; i < 5; i++ {
		if _, err := c.GetDomain(context.Background(), "example.nl"); err != nil {
			t.Fatal(err)
		}
	}
	if got := ds.domainCalls.Load(); got != 1 {
		t.Fatalf("expected 1 backend call, got %d", got)
	}
}

func TestCache_NegativeCachingHonoursShorterTTL(t *testing.T) {
	ds := &counterDS{err: datasource.ErrNotFound}
	c := New(ds, Config{Size: 16, TTL: time.Minute, NegTTL: 10 * time.Millisecond})

	for i := 0; i < 3; i++ {
		if _, err := c.GetDomain(context.Background(), "missing.nl"); !errors.Is(err, datasource.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	}
	if got := ds.domainCalls.Load(); got != 1 {
		t.Fatalf("within NegTTL window: expected 1 backend call, got %d", got)
	}

	time.Sleep(15 * time.Millisecond)
	_, _ = c.GetDomain(context.Background(), "missing.nl")
	if got := ds.domainCalls.Load(); got != 2 {
		t.Fatalf("after NegTTL expiry: expected 2 backend calls, got %d", got)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := &clockStub{t: now}
	ds := &counterDS{}
	c := New(ds, Config{Size: 4, TTL: time.Minute, NowFunc: clock.now})

	_, _ = c.GetDomain(context.Background(), "x.nl")
	clock.advance(30 * time.Second)
	_, _ = c.GetDomain(context.Background(), "x.nl") // still warm
	clock.advance(time.Minute)
	_, _ = c.GetDomain(context.Background(), "x.nl") // expired — reloads

	if got := ds.domainCalls.Load(); got != 2 {
		t.Fatalf("expected 2 backend calls across TTL boundary, got %d", got)
	}
}

func TestCache_LRUEvictionUnderPressure(t *testing.T) {
	ds := &counterDS{}
	c := New(ds, Config{Size: 2, TTL: time.Minute})

	_, _ = c.GetDomain(context.Background(), "a.nl")
	_, _ = c.GetDomain(context.Background(), "b.nl")
	_, _ = c.GetDomain(context.Background(), "c.nl") // evicts a.nl
	_, _ = c.GetDomain(context.Background(), "a.nl") // miss, reload
	if got := ds.domainCalls.Load(); got != 4 {
		t.Fatalf("expected 4 backend calls (cap=2 forces reload), got %d", got)
	}
}

func TestCache_Singleflight_CollapsesConcurrentMisses(t *testing.T) {
	const N = 64
	var release sync.WaitGroup
	release.Add(1)
	ds := &blockingDS{release: &release}
	c := New(ds, Config{Size: 16, TTL: time.Minute})

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.GetDomain(context.Background(), "hot.nl")
		}()
	}

	// Let all goroutines enter the singleflight queue before the backend
	// is allowed to return. A small sleep is acceptable here: the test
	// is asserting at-most-one backend call regardless of timing.
	time.Sleep(20 * time.Millisecond)
	release.Done()
	wg.Wait()

	if got := ds.calls.Load(); got != 1 {
		t.Fatalf("singleflight: expected 1 backend call, got %d", got)
	}
}

type blockingDS struct {
	counterDS
	calls   atomic.Int64
	release *sync.WaitGroup
}

func (b *blockingDS) GetDomain(ctx context.Context, name string) (*datasource.Domain, error) {
	b.calls.Add(1)
	b.release.Wait()
	return &datasource.Domain{LDHName: name}, nil
}

type clockStub struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockStub) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockStub) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }
