package memory

import (
	"context"
	"sort"
	"strings"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/search"
)

// The Store also satisfies search.Index. The in-memory implementation
// is a linear scan — fine for tests and demos, the Postgres provider
// uses pg_trgm for production scale.

func (s *Store) SearchDomains(_ context.Context, q search.Query) (*search.Result[datasource.Domain], error) {
	s.mu.RLock()
	all := make([]*datasource.Domain, 0, len(s.domains))
	for _, d := range s.domains {
		all = append(all, d)
	}
	s.mu.RUnlock()

	// Deterministic ordering for pagination correctness.
	sort.Slice(all, func(i, j int) bool { return all[i].LDHName < all[j].LDHName })

	var matched []datasource.Domain
	for _, d := range all {
		if !domainMatches(d, q.Terms) {
			continue
		}
		matched = append(matched, *d)
	}
	return paginate(matched, q), nil
}

func (s *Store) SearchEntities(_ context.Context, q search.Query) (*search.Result[datasource.Contact], error) {
	s.mu.RLock()
	all := make([]*datasource.Contact, 0, len(s.entities))
	for _, e := range s.entities {
		all = append(all, e)
	}
	s.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].Handle < all[j].Handle })

	var matched []datasource.Contact
	for _, e := range all {
		if !entityMatches(e, q.Terms) {
			continue
		}
		matched = append(matched, *e)
	}
	return paginate(matched, q), nil
}

func (s *Store) SearchNameservers(_ context.Context, q search.Query) (*search.Result[datasource.Nameserver], error) {
	s.mu.RLock()
	all := make([]*datasource.Nameserver, 0, len(s.nameserver))
	for _, n := range s.nameserver {
		all = append(all, n)
	}
	s.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].LDHName < all[j].LDHName })

	var matched []datasource.Nameserver
	for _, n := range all {
		if !nameserverMatches(n, q.Terms) {
			continue
		}
		matched = append(matched, *n)
	}
	return paginate(matched, q), nil
}

func domainMatches(d *datasource.Domain, terms map[string]string) bool {
	for k, v := range terms {
		switch k {
		case "name":
			if !search.MatchPattern(strings.ToLower(d.LDHName), strings.ToLower(v)) &&
				!search.MatchPattern(strings.ToLower(d.UnicodeName), strings.ToLower(v)) {
				return false
			}
		case "nsLdhName":
			if !anyNameserverMatches(d.Nameservers, v) {
				return false
			}
		default:
			return false // unknown predicate
		}
	}
	return true
}

func anyNameserverMatches(ns []datasource.Nameserver, pattern string) bool {
	for _, n := range ns {
		if search.MatchPattern(strings.ToLower(n.LDHName), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func entityMatches(c *datasource.Contact, terms map[string]string) bool {
	for k, v := range terms {
		switch k {
		case "fn":
			full := c.FullName
			if full == "" {
				full = c.Organization
			}
			if !search.MatchPattern(strings.ToLower(full), strings.ToLower(v)) {
				return false
			}
		case "handle":
			if !search.MatchPattern(c.Handle, v) {
				return false
			}
		case "email":
			matched := false
			for _, e := range c.Emails {
				if search.MatchPattern(strings.ToLower(e), strings.ToLower(v)) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		case "countryCode":
			if c.Address == nil ||
				!search.MatchPattern(strings.ToUpper(c.Address.CountryCode), strings.ToUpper(v)) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func nameserverMatches(n *datasource.Nameserver, terms map[string]string) bool {
	for k, v := range terms {
		switch k {
		case "name":
			if !search.MatchPattern(strings.ToLower(n.LDHName), strings.ToLower(v)) {
				return false
			}
		case "ip":
			matched := false
			for _, ip := range append(append([]string{}, addrsToStrings(n.IPv4)...), addrsToStrings(n.IPv6)...) {
				if ip == v {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func addrsToStrings[T interface{ String() string }](in []T) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.String()
	}
	return out
}

func paginate[T any](all []T, q search.Query) *search.Result[T] {
	total := len(all)
	limit := search.ClampLimit(q.Limit, 50, 500)
	start := q.Offset
	if start < 0 {
		start = 0
	}
	if start >= total {
		return &search.Result[T]{Total: total}
	}
	end := start + limit
	if end > total {
		end = total
	}
	return &search.Result[T]{Items: all[start:end], Total: total}
}
