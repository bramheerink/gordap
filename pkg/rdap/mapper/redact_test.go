package mapper

import (
	"testing"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

func fullContact(kind string) datasource.Contact {
	return datasource.Contact{
		Handle:       "H-1",
		Kind:         kind,
		FullName:     "Jane Doe",
		Organization: "Example B.V.",
		Title:        "CTO",
		Emails:       []string{"jane@example.com"},
		Phones:       []datasource.Phone{{Number: "+31201234567", Kinds: []string{"voice"}}},
		Address: &datasource.Address{
			Street: []string{"Herengracht 1"}, Locality: "Amsterdam",
			PostalCode: "1015BA", CountryCode: "NL",
		},
	}
}

func TestRedact_Privileged_ReturnsFullRecord(t *testing.T) {
	for _, kind := range []string{"individual", "org"} {
		t.Run(kind, func(t *testing.T) {
			v := RedactContact(fullContact(kind), auth.AccessPrivileged)
			if v.FullName == "" || v.Organization == "" || v.Title == "" {
				t.Fatalf("expected full record, got %+v", v)
			}
			if len(v.Emails) != 1 || len(v.Phones) != 1 || v.Address == nil {
				t.Fatalf("expected channels + address, got %+v", v)
			}
			if v.Address.PostalCode == "" {
				t.Fatalf("privileged view must keep postal code")
			}
		})
	}
}

func TestRedact_Authenticated_KeepsChannels_StripsAddressForPerson(t *testing.T) {
	v := RedactContact(fullContact("individual"), auth.AccessAuthenticated)
	if v.FullName != "" {
		t.Fatalf("full name must be hidden for natural person at authenticated tier")
	}
	if len(v.Emails) == 0 || len(v.Phones) == 0 {
		t.Fatalf("channels must be visible, got %+v", v)
	}
	if v.Address == nil || v.Address.CountryCode != "NL" {
		t.Fatalf("expected country-only address, got %+v", v.Address)
	}
	if v.Address.PostalCode != "" || v.Address.Locality != "" || len(v.Address.Street) != 0 {
		t.Fatalf("expected street/city/postcode stripped, got %+v", v.Address)
	}
}

func TestRedact_Authenticated_KeepsFullAddressForOrg(t *testing.T) {
	v := RedactContact(fullContact("org"), auth.AccessAuthenticated)
	if v.FullName != "Jane Doe" {
		t.Fatalf("org contact name should be visible, got %q", v.FullName)
	}
	if v.Address == nil || v.Address.PostalCode != "1015BA" {
		t.Fatalf("org address must be intact, got %+v", v.Address)
	}
}

func TestRedact_Anonymous_HidesPersonalData(t *testing.T) {
	v := RedactContact(fullContact("individual"), auth.AccessAnonymous)
	if v.FullName != "" || len(v.Emails) != 0 || len(v.Phones) != 0 {
		t.Fatalf("anonymous view must strip personal channels, got %+v", v)
	}
	if v.Organization != "Example B.V." {
		t.Fatalf("organisation should remain at anonymous tier, got %q", v.Organization)
	}
	if v.Address == nil || v.Address.CountryCode != "NL" || v.Address.PostalCode != "" {
		t.Fatalf("anonymous address must be country-only, got %+v", v.Address)
	}
}

func TestRedact_Anonymous_EmptyContactReturnsEmptyView(t *testing.T) {
	v := RedactContact(datasource.Contact{Kind: "individual"}, auth.AccessAnonymous)
	if !v.Empty() {
		t.Fatalf("expected empty view, got %+v", v)
	}
}
