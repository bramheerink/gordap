package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/cache"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/handlers"
)

// countingDS records how many times each method was invoked, so the
// response-cache tests can assert whether a warm cache actually
// short-circuits the DataSource.
type countingDS struct {
	fakeDS
	domainCalls atomic.Int64
}

func (c *countingDS) GetDomain(ctx context.Context, name string) (*datasource.Domain, error) {
	c.domainCalls.Add(1)
	return c.fakeDS.GetDomain(ctx, name)
}

func newResponseCacheServer(t *testing.T) (*httptest.Server, *countingDS, *cache.ResponseCache) {
	t.Helper()
	ds := &countingDS{fakeDS: *emptyFakeDS()}
	ds.fakeDS.domains["example.nl"] = &datasource.Domain{
		Handle: "DOM-1", LDHName: "example.nl",
		Status:      []string{"active"},
		Registered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastChanged: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	rc := cache.NewResponseCache(16, time.Minute)
	srv := &handlers.Server{
		DS:            ds,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResponseCache: rc,
	}
	return httptest.NewServer(handlers.NewRouter(srv, auth.NopVerifier())), ds, rc
}

func TestE2E_ResponseCache_SecondRequestIsHit(t *testing.T) {
	ts, ds, rc := newResponseCacheServer(t)
	defer ts.Close()

	resp1, _ := ts.Client().Get(ts.URL + "/domain/example.nl")
	_ = resp1.Body.Close()
	if resp1.Header.Get("X-Gordap-Cache") == "HIT" {
		t.Fatal("first request should not be a cache hit")
	}

	resp2, _ := ts.Client().Get(ts.URL + "/domain/example.nl")
	defer resp2.Body.Close()
	if resp2.Header.Get("X-Gordap-Cache") != "HIT" {
		t.Fatalf("second request should be HIT; header=%v", resp2.Header)
	}
	if resp2.Header.Get("Content-Type") != "application/rdap+json" {
		t.Fatalf("cached response must preserve Content-Type; got %q", resp2.Header.Get("Content-Type"))
	}

	if n := ds.domainCalls.Load(); n != 1 {
		t.Fatalf("expected exactly 1 DataSource call across 2 HTTP hits, got %d", n)
	}

	if rc.Len() != 1 {
		t.Fatalf("response cache should have 1 entry, got %d", rc.Len())
	}
}

func TestE2E_ResponseCache_ErrorsNotCached(t *testing.T) {
	ts, _, rc := newResponseCacheServer(t)
	defer ts.Close()

	// Two 404s for the same missing name.
	for i := 0; i < 2; i++ {
		resp, _ := ts.Client().Get(ts.URL + "/domain/missing.nl")
		_ = resp.Body.Close()
	}
	if rc.Len() != 0 {
		t.Fatalf("errors must not populate the response cache, got %d entries", rc.Len())
	}
}

// Silence the unused import for netip which we keep available for
// parity with the broader test file set.
var _ = netip.PrefixFrom
