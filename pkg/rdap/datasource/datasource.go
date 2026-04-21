// Package datasource declares the transport-agnostic DataSource contract
// and the internal model types every provider returns. The RDAP layer never
// imports a concrete provider; the contract lives here so backends can ship
// in independent modules.
package datasource

import (
	"context"
	"errors"
	"net/netip"
)

// Sentinel errors. Providers MUST return ErrNotFound for missing objects so
// the handler can translate to the RFC 9083 §6 error response with code 404.
var (
	ErrNotFound     = errors.New("rdap: object not found")
	ErrUnauthorized = errors.New("rdap: caller not authorised for object")
)

// DataSource is the contract for retrieving RDAP objects. Implementations
// MUST honour ctx for cancellation and SHOULD propagate OpenTelemetry spans
// via the context.
type DataSource interface {
	GetDomain(ctx context.Context, name string) (*Domain, error)
	GetEntity(ctx context.Context, handle string) (*Contact, error)
	GetNameserver(ctx context.Context, name string) (*Nameserver, error)
	GetIPNetwork(ctx context.Context, ip netip.Addr) (*IPNetwork, error)
}

// Fetcher is a single-object lookup typed by the internal model. It lets
// shared plumbing (tracing, caching, retry) be written once and reused
// across object classes via a generic wrapper.
type Fetcher[K any, V any] func(ctx context.Context, key K) (*V, error)

// Instrument wraps a Fetcher so every call emits a span / metric. The
// generic signature means callers get back a function with the same shape
// they passed in — no type assertions at the use-site.
func Instrument[K any, V any](name string, f Fetcher[K, V], hook func(ctx context.Context, name string, err error)) Fetcher[K, V] {
	return func(ctx context.Context, k K) (*V, error) {
		v, err := f(ctx, k)
		if hook != nil {
			hook(ctx, name, err)
		}
		return v, err
	}
}
