// Package search declares the contract for RDAP partial-match search
// (RFC 7482 §3.2 / RFC 9536 reverse search) and paging metadata
// (RFC 8977). The core DataSource stays focused on exact lookups; search
// is a separate capability that operators opt into when their back-end
// can support it (pg_trgm on small/medium registries, OpenSearch at
// scale).
package search

import (
	"context"
	"errors"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// ErrNotImplemented is returned by the null index and by any
// implementation that refuses to serve a particular field combination.
// The handler translates it to HTTP 501.
var ErrNotImplemented = errors.New("search: not implemented")

// Query captures a single search request. Patterns follow RFC 7482 §4.1
// truncation: a trailing '*' is a wildcard, an empty pattern is a
// strict match. Implementations are free to limit which field names
// they support — RFC 9536 allows servers to advertise supported fields
// in /help.
type Query struct {
	// Terms is the set of field → pattern predicates (AND semantics).
	// Canonical keys:
	//   domains:     "name", "nsLdhName", "nsIp"
	//   entities:    "fn", "handle", "email", "countryCode"
	//   nameservers: "name", "ip"
	Terms map[string]string

	// Offset/Limit drive the simple paging model. An implementation
	// that prefers cursor-based paging MAY ignore these and produce
	// its own tokens via Result.NextCursor; the handler surfaces both.
	Offset int
	Limit  int
}

// Result is the generic return shape. NextCursor is only set when the
// implementation chose to paginate with opaque tokens rather than
// offset/limit.
type Result[T any] struct {
	Items      []T
	Total      int    // best-effort; 0 if the index cannot cheaply count
	NextCursor string // opaque
}

// Index is the search-side counterpart to datasource.DataSource. Nil
// fields in Query imply "no predicate for this field". An implementation
// MAY return ErrNotImplemented for field combinations it doesn't index.
type Index interface {
	SearchDomains(ctx context.Context, q Query) (*Result[datasource.Domain], error)
	SearchEntities(ctx context.Context, q Query) (*Result[datasource.Contact], error)
	SearchNameservers(ctx context.Context, q Query) (*Result[datasource.Nameserver], error)
}

// Null is the default Index used when the operator hasn't wired one in.
// Every search returns 501. Safe to embed anywhere an Index is required.
type Null struct{}

func (Null) SearchDomains(context.Context, Query) (*Result[datasource.Domain], error) {
	return nil, ErrNotImplemented
}
func (Null) SearchEntities(context.Context, Query) (*Result[datasource.Contact], error) {
	return nil, ErrNotImplemented
}
func (Null) SearchNameservers(context.Context, Query) (*Result[datasource.Nameserver], error) {
	return nil, ErrNotImplemented
}

// MatchPattern reports whether haystack matches a simple RFC 7482-style
// wildcard pattern (only trailing '*' is meaningful). Used by backend
// implementations that don't have their own substring primitives.
func MatchPattern(haystack, pattern string) bool {
	if pattern == "" {
		return haystack == ""
	}
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		if len(haystack) < len(prefix) {
			return false
		}
		return haystack[:len(prefix)] == prefix
	}
	return haystack == pattern
}

// ClampLimit returns a sane page size. An operator can pick a ceiling
// appropriate for their back-end; we default to 50 and cap at 500 per
// RFC 8977 common practice.
func ClampLimit(requested, defaultN, max int) int {
	if requested <= 0 {
		return defaultN
	}
	if requested > max {
		return max
	}
	return requested
}
