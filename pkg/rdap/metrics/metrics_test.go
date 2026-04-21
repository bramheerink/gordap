package metrics

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

type recordingObserver struct {
	ops   []string
	errs  []error
	times []time.Duration
}

func (r *recordingObserver) Observed(_ context.Context, op string, d time.Duration, err error) {
	r.ops = append(r.ops, op)
	r.errs = append(r.errs, err)
	r.times = append(r.times, d)
}

type stubDS struct{ err error }

func (s stubDS) GetDomain(context.Context, string) (*datasource.Domain, error) {
	return &datasource.Domain{LDHName: "x.nl"}, s.err
}
func (s stubDS) GetEntity(context.Context, string) (*datasource.Contact, error) {
	return &datasource.Contact{Handle: "E"}, s.err
}
func (s stubDS) GetNameserver(context.Context, string) (*datasource.Nameserver, error) {
	return &datasource.Nameserver{LDHName: "ns"}, s.err
}
func (s stubDS) GetIPNetwork(context.Context, netip.Addr) (*datasource.IPNetwork, error) {
	return &datasource.IPNetwork{}, s.err
}

func TestWrapDataSource_NilObserver_ReturnsInner(t *testing.T) {
	inner := stubDS{}
	if got := WrapDataSource(inner, nil); got != inner {
		t.Fatalf("nil observer should pass-through, got %T", got)
	}
}

func TestWrapDataSource_RecordsAllOps(t *testing.T) {
	r := &recordingObserver{}
	ds := WrapDataSource(stubDS{}, r)

	_, _ = ds.GetDomain(context.Background(), "x.nl")
	_, _ = ds.GetEntity(context.Background(), "E")
	_, _ = ds.GetNameserver(context.Background(), "ns.x.nl")
	_, _ = ds.GetIPNetwork(context.Background(), netip.MustParseAddr("192.0.2.1"))

	want := []string{"domain", "entity", "nameserver", "ip"}
	if len(r.ops) != 4 {
		t.Fatalf("got %d ops, want 4", len(r.ops))
	}
	for i, op := range want {
		if r.ops[i] != op {
			t.Fatalf("op[%d] = %q, want %q", i, r.ops[i], op)
		}
	}
	for _, e := range r.errs {
		if e != nil {
			t.Fatalf("unexpected error: %v", e)
		}
	}
}

func TestWrapDataSource_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	r := &recordingObserver{}
	ds := WrapDataSource(stubDS{err: boom}, r)

	_, err := ds.GetDomain(context.Background(), "x.nl")
	if !errors.Is(err, boom) {
		t.Fatalf("error not propagated: %v", err)
	}
	if len(r.errs) != 1 || r.errs[0] != boom {
		t.Fatalf("expected recorded error, got %+v", r.errs)
	}
}

func TestCacheObserver_Ratio(t *testing.T) {
	c := &CacheObserver{}
	if c.Ratio() != 0 {
		t.Fatalf("empty ratio should be 0, got %f", c.Ratio())
	}
	c.RecordHit()
	c.RecordHit()
	c.RecordHit()
	c.RecordMiss()
	if c.Ratio() != 0.75 {
		t.Fatalf("ratio: got %f, want 0.75", c.Ratio())
	}
}
