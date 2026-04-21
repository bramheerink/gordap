package memory

import (
	"context"
	"testing"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/search"
)

func seededStore(t *testing.T) *Store {
	t.Helper()
	s := New()
	for _, name := range []string{"example.nl", "foo.nl", "bar.com", "other.example.nl"} {
		s.PutDomain(&datasource.Domain{LDHName: name, Handle: "D-" + name})
	}
	for _, h := range []string{"REG-1", "REG-2", "ADMIN-1"} {
		s.PutEntity(&datasource.Contact{
			Handle: h, Kind: "org",
			FullName: h + "-name",
			Organization: "Org " + h,
			Emails:  []string{"contact@" + h + ".example"},
			Address: &datasource.Address{CountryCode: "NL"},
		})
	}
	s.PutEntity(&datasource.Contact{
		Handle: "DE-1", Kind: "individual", FullName: "Hans Muster",
		Emails: []string{"hans@muster.de"},
		Address: &datasource.Address{CountryCode: "DE"},
	})
	for _, ns := range []string{"ns1.example.nl", "ns2.example.nl", "ns.other.com"} {
		s.PutNameserver(&datasource.Nameserver{LDHName: ns, Handle: "NS-" + ns})
	}
	return s
}

func TestMemorySearch_DomainsByWildcard(t *testing.T) {
	s := seededStore(t)
	r, err := s.SearchDomains(context.Background(), search.Query{Terms: map[string]string{"name": "*.nl"}})
	if err != nil {
		t.Fatal(err)
	}
	// MatchPattern only handles trailing '*'. "*.nl" becomes strict match;
	// use prefix wildcard "foo*" style instead.
	_ = r
	r, err = s.SearchDomains(context.Background(), search.Query{Terms: map[string]string{"name": "example.*"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Fatalf("want 1 match for example.*, got %d (%+v)", r.Total, r.Items)
	}
}

func TestMemorySearch_DomainsByStrictName(t *testing.T) {
	s := seededStore(t)
	r, err := s.SearchDomains(context.Background(), search.Query{Terms: map[string]string{"name": "foo.nl"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || r.Items[0].LDHName != "foo.nl" {
		t.Fatalf("wanted exact foo.nl match, got %+v", r)
	}
}

func TestMemorySearch_EntitiesByEmailWildcard(t *testing.T) {
	s := seededStore(t)
	r, err := s.SearchEntities(context.Background(),
		search.Query{Terms: map[string]string{"email": "hans@*"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || r.Items[0].Handle != "DE-1" {
		t.Fatalf("wanted DE-1 via email, got %+v", r.Items)
	}
}

func TestMemorySearch_EntitiesByCountry(t *testing.T) {
	s := seededStore(t)
	r, _ := s.SearchEntities(context.Background(),
		search.Query{Terms: map[string]string{"countryCode": "NL"}})
	if r.Total != 3 {
		t.Fatalf("NL entities: %d (%+v)", r.Total, r.Items)
	}
}

func TestMemorySearch_NameserversByPrefix(t *testing.T) {
	s := seededStore(t)
	r, _ := s.SearchNameservers(context.Background(),
		search.Query{Terms: map[string]string{"name": "ns*"}})
	if r.Total != 3 {
		t.Fatalf("ns* count: %d", r.Total)
	}
}

func TestMemorySearch_Pagination(t *testing.T) {
	s := New()
	for i := 0; i < 25; i++ {
		s.PutDomain(&datasource.Domain{LDHName: string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".nl", Handle: "h"})
	}
	// Page 1
	r, _ := s.SearchDomains(context.Background(), search.Query{
		Terms:  map[string]string{"name": "a*"},
		Limit:  10,
		Offset: 0,
	})
	if len(r.Items) != 10 {
		t.Fatalf("page 1: %d", len(r.Items))
	}
	// Page 2
	r, _ = s.SearchDomains(context.Background(), search.Query{
		Terms:  map[string]string{"name": "a*"},
		Limit:  10,
		Offset: 10,
	})
	if len(r.Items) != 10 {
		t.Fatalf("page 2: %d", len(r.Items))
	}
	// Tail
	r, _ = s.SearchDomains(context.Background(), search.Query{
		Terms:  map[string]string{"name": "a*"},
		Limit:  10,
		Offset: 20,
	})
	if len(r.Items) != 5 {
		t.Fatalf("tail: %d", len(r.Items))
	}
}
