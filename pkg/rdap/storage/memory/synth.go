package memory

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/bramheerink/gordap/internal/synth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// SeedSynthetic populates the in-memory store with n synthetic domains
// and matching entities + nameservers, drawn from internal/synth so the
// stress runner can validate every query result against deterministic
// expectations.
//
// Use this for PG-less stress runs:
//
//	store := memory.New()
//	memory.SeedSynthetic(store, 10_000)
//
// Cost is about 1µs per domain on commodity hardware; 100k domains
// seeds in ~100ms.
func SeedSynthetic(s *Store, n int) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Entities first so domain inserts can reference them.
	for i := 0; i < n; i++ {
		s.PutEntity(buildEntity(i, now))
	}

	// One nameserver per ten domains.
	nsCount := (n + 9) / 10
	for i := 0; i < nsCount; i++ {
		s.PutNameserver(&datasource.Nameserver{
			Handle:  synth.NameserverHandle(i),
			LDHName: synth.NameserverName(i),
			IPv4:    []netip.Addr{netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i%254+1))},
		})
	}

	// Domains + their join records (memory store stores nameservers and
	// contacts inline on the Domain rather than via separate join tables).
	for i := 0; i < n; i++ {
		nsIdx := synth.NameserverForDomain(i)
		nsRef := datasource.Nameserver{
			Handle:  synth.NameserverHandle(nsIdx),
			LDHName: synth.NameserverName(nsIdx),
		}
		entity := buildEntity(synth.EntityForDomain(i), now)
		s.PutDomain(&datasource.Domain{
			Handle:         synth.DomainHandle(i),
			LDHName:        synth.DomainName(i),
			UnicodeName:    synth.DomainName(i),
			Status:         []string{"active"},
			Registered:     now,
			Expires:        now.AddDate(1, 0, 0),
			LastChanged:    now,
			LastRDAPUpdate: now,
			Nameservers:    []datasource.Nameserver{nsRef},
			Contacts:       []datasource.Contact{*entity},
		})
	}
}

func buildEntity(i int, now time.Time) *datasource.Contact {
	kind := synth.Kinds(i)
	c := &datasource.Contact{
		Handle:       synth.EntityHandle(i),
		Roles:        []string{"registrant"},
		Kind:         kind,
		FullName:     synth.EntityFullName(i),
		Organization: synth.EntityOrganization(i),
		Emails:       []string{synth.EntityEmail(i)},
		Phones:       []datasource.Phone{{Number: synth.EntityPhone(i), Kinds: []string{"voice"}}},
		Address: &datasource.Address{
			Street:      []string{synth.EntityStreet(i)},
			Locality:    synth.EntityLocality(i),
			PostalCode:  synth.EntityPostalCode(i),
			CountryCode: synth.EntityCountry(i),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return c
}
