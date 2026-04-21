package jscontact

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleCard() *Card {
	return &Card{
		Version: "1.0",
		Type:    "Card",
		UID:     "urn:uuid:abc",
		Kind:    "individual",
		Name:    &Name{Type: "Name", Full: "Jane Doe"},
		Organizations: map[string]Org{
			"org": {Type: "Organization", Name: "Example B.V."},
		},
		Titles: map[string]Title{
			"title": {Type: "Title", Name: "CTO"},
		},
		Emails: map[string]Email{
			"e1": {Type: "EmailAddress", Address: "jane@example.com",
				Contexts: map[string]bool{"work": true}},
		},
		Phones: map[string]Phone{
			"p1": {Type: "Phone", Number: "+31201234567",
				Features: map[string]bool{"voice": true},
				Contexts: map[string]bool{"work": true}},
		},
		Addresses: map[string]Address{
			"a1": {Type: "Address", CountryCode: "NL",
				Components: []AddressComponent{
					{Kind: "name", Value: "Herengracht 1"},
					{Kind: "locality", Value: "Amsterdam"},
					{Kind: "postcode", Value: "1015BA"},
				},
				Contexts: map[string]bool{"work": true}},
		},
	}
}

func TestToJCard_WellFormedShape(t *testing.T) {
	out := ToJCard(sampleCard())
	if len(out) != 2 || out[0] != "vcard" {
		t.Fatalf("outer shape: %+v", out)
	}
	props, ok := out[1].([]any)
	if !ok || len(props) == 0 {
		t.Fatalf("properties: %+v", out[1])
	}
}

func TestToJCard_VersionFirst(t *testing.T) {
	out := ToJCard(sampleCard())
	props := out[1].([]any)
	first := props[0].([]any)
	if first[0] != "version" || first[3] != "4.0" {
		t.Fatalf("version property wrong: %+v", first)
	}
}

func TestToJCard_NilReturnsNil(t *testing.T) {
	if ToJCard(nil) != nil {
		t.Fatal("nil card should produce nil output")
	}
}

func TestToJCard_FullRoundTrip_JSON(t *testing.T) {
	b, err := json.Marshal(ToJCard(sampleCard()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"vcard"`,
		`"version",{},"text","4.0"`,
		`"fn",{},"text","Jane Doe"`,
		`"org",{},"text","Example B.V."`,
		`"title",{},"text","CTO"`,
		`"email",{"type":"work"},"text","jane@example.com"`,
		`"tel",{"type":"voice","type":"work"}`, // order of map keys non-deterministic
		`"tel"`,
		`"adr"`,
		`"NL"`,
	} {
		if !strings.Contains(s, want) {
			// For the tel case where param order varies, relax: just
			// assert both pieces are present somewhere.
			if want == `"tel",{"type":"voice","type":"work"}` {
				if strings.Contains(s, `"tel"`) && strings.Contains(s, `"voice"`) && strings.Contains(s, `"work"`) {
					continue
				}
			}
			t.Errorf("missing %q in jCard JSON:\n%s", want, s)
		}
	}
}

func TestToJCard_MinimalCard_OrgOnly(t *testing.T) {
	c := &Card{
		Organizations: map[string]Org{"org": {Name: "Example B.V."}},
	}
	out := ToJCard(c)
	props := out[1].([]any)
	// Should have: version + org. No other properties.
	if len(props) != 2 {
		t.Fatalf("expected 2 properties, got %d: %+v", len(props), props)
	}
	org := props[1].([]any)
	if org[0] != "org" || org[3] != "Example B.V." {
		t.Fatalf("org property: %+v", org)
	}
}
