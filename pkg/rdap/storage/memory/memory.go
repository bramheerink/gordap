// Package memory is an in-memory DataSource. It's useful as a demo backend,
// as a seed store in integration tests, and as the zero-config fallback the
// reference binary uses when no real database is configured.
package memory

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

type Store struct {
	mu         sync.RWMutex
	domains    map[string]*datasource.Domain
	entities   map[string]*datasource.Contact
	nameserver map[string]*datasource.Nameserver
	networks   []*datasource.IPNetwork
}

func New() *Store {
	return &Store{
		domains:    map[string]*datasource.Domain{},
		entities:   map[string]*datasource.Contact{},
		nameserver: map[string]*datasource.Nameserver{},
	}
}

func (s *Store) PutDomain(d *datasource.Domain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domains[strings.ToLower(d.LDHName)] = d
}

func (s *Store) PutEntity(e *datasource.Contact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entities[e.Handle] = e
}

func (s *Store) PutNameserver(n *datasource.Nameserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nameserver[strings.ToLower(n.LDHName)] = n
}

func (s *Store) PutNetwork(n *datasource.IPNetwork) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks = append(s.networks, n)
}

func (s *Store) GetDomain(_ context.Context, name string) (*datasource.Domain, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if d, ok := s.domains[strings.ToLower(name)]; ok {
		return d, nil
	}
	return nil, datasource.ErrNotFound
}

func (s *Store) GetEntity(_ context.Context, handle string) (*datasource.Contact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.entities[handle]; ok {
		return e, nil
	}
	return nil, datasource.ErrNotFound
}

func (s *Store) GetNameserver(_ context.Context, name string) (*datasource.Nameserver, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n, ok := s.nameserver[strings.ToLower(name)]; ok {
		return n, nil
	}
	return nil, datasource.ErrNotFound
}

func (s *Store) GetIPNetwork(_ context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.networks {
		if n.Prefix.Contains(ip) {
			return n, nil
		}
	}
	return nil, datasource.ErrNotFound
}

// Seed installs a small demonstration fixture: one .nl domain with a
// registrant, nameservers, and one IP block. Useful when booting the
// server without a real backend.
func Seed(s *Store) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s.PutEntity(&datasource.Contact{
		Handle:       "REG-EXAMPLE",
		Roles:        []string{"registrant"},
		Kind:         "org",
		Organization: "Example Holdings B.V.",
		FullName:     "Jane Doe",
		Title:        "CTO",
		Emails:       []string{"hostmaster@example.nl"},
		Phones:       []datasource.Phone{{Number: "+31201234567", Kinds: []string{"voice"}}},
		Address: &datasource.Address{
			Street:      []string{"Herengracht 1"},
			Locality:    "Amsterdam",
			PostalCode:  "1015BA",
			CountryCode: "NL",
		},
		CreatedAt: now, UpdatedAt: now,
	})

	ns1 := datasource.Nameserver{
		Handle: "NS-1", LDHName: "ns1.example.nl",
		IPv4: []netip.Addr{netip.MustParseAddr("192.0.2.53")},
	}
	ns2 := datasource.Nameserver{Handle: "NS-2", LDHName: "ns2.example.nl"}
	s.PutNameserver(&ns1)
	s.PutNameserver(&ns2)

	s.PutDomain(&datasource.Domain{
		Handle:      "DOM-EXAMPLE",
		LDHName:     "example.nl",
		UnicodeName: "example.nl",
		Status:      []string{"active"},
		Registered:     now,
		Expires:        now.AddDate(1, 0, 0),
		LastChanged:    now,
		LastRDAPUpdate: now,
		Nameservers: []datasource.Nameserver{ns1, ns2},
		Contacts: []datasource.Contact{
			*mustEntity(s, "REG-EXAMPLE"),
		},
	})

	// A second domain with an IDN label so the UTS-46 path is exercised end
	// to end when the demo is queried with the Unicode form.
	idnReg := &datasource.Contact{
		Handle:       "REG-IDN",
		Roles:        []string{"registrant"},
		Kind:         "individual",
		FullName:     "Alice Muster",
		Emails:       []string{"alice@example.test"},
		Address:      &datasource.Address{CountryCode: "DE"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.PutEntity(idnReg)
	s.PutDomain(&datasource.Domain{
		Handle:         "DOM-BUCHER",
		LDHName:        "xn--bcher-kva.example",
		UnicodeName:    "bücher.example",
		Status:         []string{"active"},
		Registered:     now,
		LastChanged:    now,
		LastRDAPUpdate: now,
		Contacts:       []datasource.Contact{*idnReg},
	})

	s.PutNetwork(&datasource.IPNetwork{
		Handle:      "NET-192.0.2",
		Prefix:      netip.MustParsePrefix("192.0.2.0/24"),
		Name:        "TEST-NET-1",
		Type:        "ALLOCATED",
		Country:     "NL",
		Status:      []string{"active"},
		Registered:  now,
		LastChanged: now,
	})
}

// mustEntity is a Seed helper: fetches from the store under its own lock
// discipline. The seed path is single-threaded so the locking is defensive.
func mustEntity(s *Store, handle string) *datasource.Contact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entities[handle]
}
