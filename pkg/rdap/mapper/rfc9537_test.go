package mapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

func TestRedacted_AnonymousIndividualEmitsAllKinds(t *testing.T) {
	d := &datasource.Domain{
		LDHName:     "example.nl",
		LastChanged: time.Now(),
		Contacts:    []datasource.Contact{fullContact("individual")},
	}
	out := Domain(d, Options{Level: auth.AccessAnonymous, RedactionReason: "GDPR"})
	if len(out.Redacted) == 0 {
		t.Fatal("expected RFC 9537 markers for anonymous individual view")
	}
	byDesc := map[string]bool{}
	for _, r := range out.Redacted {
		byDesc[r.Name.Description] = true
		if r.PathLang != "jsonpath" {
			t.Errorf("pathLang: %q", r.PathLang)
		}
		if r.Method != "removal" {
			t.Errorf("method: %q", r.Method)
		}
		if r.PrePath == "" {
			t.Errorf("empty prePath on %+v", r)
		}
		if r.Reason == nil || r.Reason.Description != "GDPR" {
			t.Errorf("reason: %+v", r.Reason)
		}
	}
	for _, want := range []string{"Contact full name", "Contact email address", "Contact phone number", "Contact postal address"} {
		if !byDesc[want] {
			t.Fatalf("missing redaction marker %q in %+v", want, byDesc)
		}
	}
}

func TestRedacted_PrivilegedNoMarkers(t *testing.T) {
	d := &datasource.Domain{
		LDHName: "example.nl", LastChanged: time.Now(),
		Contacts: []datasource.Contact{fullContact("individual")},
	}
	out := Domain(d, Options{Level: auth.AccessPrivileged})
	if len(out.Redacted) != 0 {
		t.Fatalf("privileged view must emit no redaction markers, got %+v", out.Redacted)
	}
}

func TestRedacted_DuplicatesCollapsed(t *testing.T) {
	c := fullContact("individual")
	d := &datasource.Domain{
		LDHName: "example.nl", LastChanged: time.Now(),
		Contacts: []datasource.Contact{c, c, c}, // three individuals, same redactions
	}
	out := Domain(d, Options{Level: auth.AccessAnonymous})

	seen := map[string]int{}
	for _, r := range out.Redacted {
		seen[r.Name.Description]++
	}
	for desc, count := range seen {
		if count != 1 {
			t.Fatalf("%q appears %d times; duplicates should be collapsed", desc, count)
		}
	}
}

func TestRedacted_EntityTopLevel_PathsRewrittenToRoot(t *testing.T) {
	c := fullContact("individual")
	got := EntityTopLevel(&c, Options{Level: auth.AccessAnonymous})
	if len(got.Redacted) == 0 {
		t.Fatal("expected markers")
	}
	for _, r := range got.Redacted {
		if strings.HasPrefix(r.PrePath, "$.entities[*]") {
			t.Fatalf("entity-root response must not use $.entities[*] prefix: %q", r.PrePath)
		}
		if !strings.HasPrefix(r.PrePath, "$.") {
			t.Fatalf("prePath must start with $.: %q", r.PrePath)
		}
	}
}

func TestRedacted_WireJSONContainsArray(t *testing.T) {
	c := fullContact("individual")
	d := &datasource.Domain{LDHName: "x.nl", LastChanged: time.Now(), Contacts: []datasource.Contact{c}}
	b, err := json.Marshal(Domain(d, Options{Level: auth.AccessAnonymous}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"redacted"`, `"pathLang":"jsonpath"`, `"method":"removal"`, `"prePath"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("wire JSON missing %q in %s", want, s)
		}
	}
}

func TestEPPStatusMapping(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clientHold", "client hold"},
		{"pendingDelete", "pending delete"},
		{"ok", "active"},
		{"serverTransferProhibited", "server transfer prohibited"},
		{"customUnknown", "customUnknown"}, // pass-through
	}
	for _, c := range cases {
		if got := eppToRDAPStatus(c.in); got != c.want {
			t.Errorf("eppToRDAPStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMapSecureDNS(t *testing.T) {
	in := &datasource.SecureDNS{DelegationSigned: true}
	out := mapSecureDNS(in)
	if out == nil {
		t.Fatal("expected non-nil")
	}
	if out.DelegationSigned == nil || !*out.DelegationSigned {
		t.Fatalf("delegationSigned not preserved: %+v", out)
	}
	if mapSecureDNS(nil) != nil {
		t.Fatal("nil input must yield nil output")
	}
}

func TestRegistrarEntity_IANAIDAndAbuse(t *testing.T) {
	r := &datasource.Registrar{
		Handle: "RR-1", Name: "Example Registrar", IANAID: "9999",
		Abuse: &datasource.Contact{
			Handle: "AB-1", Kind: "org", Organization: "Example Abuse",
			Emails: []string{"abuse@example.com"},
		},
	}
	e := registrarEntity(r, Options{Level: auth.AccessPrivileged})
	if len(e.Roles) != 1 || e.Roles[0] != "registrar" {
		t.Fatalf("role: %+v", e.Roles)
	}
	if len(e.PublicIDs) != 1 || e.PublicIDs[0].Type != "IANA Registrar ID" || e.PublicIDs[0].Identifier != "9999" {
		t.Fatalf("publicIds: %+v", e.PublicIDs)
	}
	if len(e.Entities) != 1 || e.Entities[0].Roles[0] != "abuse" {
		t.Fatalf("abuse sub-entity missing: %+v", e.Entities)
	}
}
