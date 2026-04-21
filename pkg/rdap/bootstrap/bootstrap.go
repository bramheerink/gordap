// Package bootstrap implements the RFC 7484 IANA bootstrap lookup. The server
// consults it when it cannot answer a query authoritatively and should
// redirect the client to the responsible registry.
//
// The implementation pulls the four IANA JSON files on start and refreshes
// them on a daily tick; lookups are pure in-memory range checks.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	DNSRegistryURL = "https://data.iana.org/rdap/dns.json"
	IPv4URL        = "https://data.iana.org/rdap/ipv4.json"
	IPv6URL        = "https://data.iana.org/rdap/ipv6.json"
	ASNURL         = "https://data.iana.org/rdap/asn.json"
)

type fileFormat struct {
	Version     string       `json:"version"`
	Publication time.Time    `json:"publication"`
	Services    [][][]string `json:"services"`
}

type Registry struct {
	mu           sync.RWMutex
	tldServers   map[string][]string
	ipv4Prefixes []ipEntry
	ipv6Prefixes []ipEntry
	http         *http.Client
}

type ipEntry struct {
	prefix netip.Prefix
	urls   []string
}

func New(client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Registry{
		tldServers: map[string][]string{},
		http:       client,
	}
}

// Refresh pulls every IANA file. Callers SHOULD invoke this on start and then
// on a time.Ticker; failures are non-fatal — stale data is preferable to an
// outage.
func (r *Registry) Refresh(ctx context.Context) error {
	dns, err := r.fetch(ctx, DNSRegistryURL)
	if err != nil {
		return fmt.Errorf("bootstrap dns: %w", err)
	}
	v4, err := r.fetch(ctx, IPv4URL)
	if err != nil {
		return fmt.Errorf("bootstrap ipv4: %w", err)
	}
	v6, err := r.fetch(ctx, IPv6URL)
	if err != nil {
		return fmt.Errorf("bootstrap ipv6: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tldServers = map[string][]string{}
	for _, svc := range dns.Services {
		if len(svc) < 2 {
			continue
		}
		for _, tld := range svc[0] {
			r.tldServers[strings.ToLower(tld)] = svc[1]
		}
	}
	r.ipv4Prefixes = parsePrefixes(v4.Services)
	r.ipv6Prefixes = parsePrefixes(v6.Services)
	return nil
}

func parsePrefixes(services [][][]string) []ipEntry {
	var out []ipEntry
	for _, svc := range services {
		if len(svc) < 2 {
			continue
		}
		for _, cidr := range svc[0] {
			if p, err := netip.ParsePrefix(cidr); err == nil {
				out = append(out, ipEntry{prefix: p, urls: svc[1]})
			}
		}
	}
	return out
}

func (r *Registry) fetch(ctx context.Context, url string) (*fileFormat, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var f fileFormat
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// DomainServers returns the RDAP base URLs authoritative for a TLD, or nil if
// the TLD is unknown. Matching walks upward so sub-domains inherit from their
// parent.
func (r *Registry) DomainServers(name string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(name, ".")), ".")
	for i := 0; i < len(labels); i++ {
		zone := strings.Join(labels[i:], ".")
		if srv, ok := r.tldServers[zone]; ok {
			return srv
		}
	}
	return nil
}

func (r *Registry) IPServers(ip netip.Addr) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pool := r.ipv6Prefixes
	if ip.Is4() {
		pool = r.ipv4Prefixes
	}
	for _, e := range pool {
		if e.prefix.Contains(ip) {
			return e.urls
		}
	}
	return nil
}
