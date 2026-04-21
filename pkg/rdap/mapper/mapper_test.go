package mapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

func TestBuildCard_PrivilegedOrg(t *testing.T) {
	c := fullContact("org")
	c.UpdatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	card := buildCard(c, auth.AccessPrivileged)
	if card == nil {
		t.Fatal("expected card for privileged view")
	}
	if card.Type != "Card" || card.Version != "1.0" {
		t.Fatalf("wrong type/version: %+v", card)
	}
	if card.Kind != "org" {
		t.Fatalf("kind: got %q, want org", card.Kind)
	}
	if card.Organizations["org"].Name != "Example B.V." {
		t.Fatalf("org name not mapped: %+v", card.Organizations)
	}
	if got := card.Emails["e1"].Address; got != "jane@example.com" {
		t.Fatalf("email: %q", got)
	}
}

func TestBuildCard_AnonymousNaturalPerson_ReturnsNilWhenEmpty(t *testing.T) {
	c := datasource.Contact{Handle: "H", Kind: "individual", FullName: "Jane Doe",
		Emails: []string{"jane@example.com"}}
	if card := buildCard(c, auth.AccessAnonymous); card != nil {
		t.Fatalf("expected nil card for fully-redacted contact, got %+v", card)
	}
}

func TestDomain_ShapeAndEvents_IncludingRDAPDBUpdate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	d := &datasource.Domain{
		Handle: "DOM-1", LDHName: "example.nl", UnicodeName: "example.nl",
		Status: []string{"active"}, Registered: now, LastChanged: now,
		Expires: now.AddDate(1, 0, 0), LastRDAPUpdate: last,
		Nameservers: []datasource.Nameserver{{LDHName: "ns1.example.nl"}},
		Contacts:    []datasource.Contact{fullContact("org")},
	}
	out := Domain(d, Options{Level: auth.AccessPrivileged})

	actions := map[string]time.Time{}
	for _, e := range out.Events {
		actions[e.Action] = e.Date
	}
	for _, want := range []string{"registration", "last changed", "expiration", "last update of RDAP database"} {
		if _, ok := actions[want]; !ok {
			t.Fatalf("missing event %q in %+v", want, out.Events)
		}
	}
	if !actions["last update of RDAP database"].Equal(last) {
		t.Fatalf("db-update event date: got %v want %v", actions["last update of RDAP database"], last)
	}
}

func TestDomain_SelfLinkInjected(t *testing.T) {
	d := &datasource.Domain{Handle: "DOM", LDHName: "example.nl", LastChanged: time.Now()}
	out := Domain(d, Options{SelfLinkBase: "https://rdap.example.com"})
	if len(out.Links) != 1 || out.Links[0].Rel != "self" {
		t.Fatalf("expected self link, got %+v", out.Links)
	}
	if out.Links[0].Href != "https://rdap.example.com/domain/example.nl" {
		t.Fatalf("self href: %q", out.Links[0].Href)
	}
}

func TestDomain_ConformanceAndNoticesFromOptions(t *testing.T) {
	d := &datasource.Domain{LDHName: "example.nl", LastChanged: time.Now()}
	opts := Options{
		ExtraConformance: []string{"icann_rdap_response_profile_1"},
		ExtraNotices:     []types.Notice{{Title: "ToS"}},
	}
	out := Domain(d, opts)

	conf := strings.Join(out.RDAPConformance, ",")
	if !strings.Contains(conf, "icann_rdap_response_profile_1") {
		t.Fatalf("extra conformance not injected: %v", out.RDAPConformance)
	}
	if !strings.Contains(conf, "rdap_level_0") {
		t.Fatalf("baseline conformance missing: %v", out.RDAPConformance)
	}
	if len(out.Notices) != 1 || out.Notices[0].Title != "ToS" {
		t.Fatalf("notices not injected: %+v", out.Notices)
	}
}

func TestDomain_DBUpdateFallsBackToLastChanged(t *testing.T) {
	// Providers that don't populate LastRDAPUpdate should still get the
	// mandatory ICANN event — fall back to LastChanged.
	d := &datasource.Domain{LDHName: "example.nl",
		LastChanged: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}
	out := Domain(d, Options{})
	var got *time.Time
	for _, e := range out.Events {
		if e.Action == "last update of RDAP database" {
			t := e.Date
			got = &t
		}
	}
	if got == nil {
		t.Fatal("expected fallback RDAP-db event to be emitted")
	}
	if !got.Equal(d.LastChanged) {
		t.Fatalf("fallback date: got %v, want %v", got, d.LastChanged)
	}
}

func TestDomain_JSONShape(t *testing.T) {
	d := &datasource.Domain{Handle: "DOM-1", LDHName: "example.nl",
		Status: []string{"active"}, LastChanged: time.Now()}
	b, _ := json.Marshal(Domain(d, Options{}))
	s := string(b)
	for _, want := range []string{`"objectClassName":"domain"`, `"rdapConformance"`, `"ldhName":"example.nl"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
}

func TestDeterministicUID_Stable(t *testing.T) {
	if deterministicUID("H-1") != deterministicUID("H-1") {
		t.Fatal("expected deterministic UID across calls")
	}
	if deterministicUID("H-1") == deterministicUID("H-2") {
		t.Fatal("different handles must produce different UIDs")
	}
}
