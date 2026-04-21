package mapper

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

func TestEmitJCard_PopulatesVCardArray(t *testing.T) {
	d := &datasource.Domain{
		LDHName:     "example.nl",
		LastChanged: time.Now(),
		Contacts:    []datasource.Contact{fullContact("org")},
	}
	out := Domain(d, Options{Level: auth.AccessPrivileged, EmitJCard: true})
	if len(out.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(out.Entities))
	}
	e := out.Entities[0]
	if e.VCardArray == nil {
		t.Fatal("vcardArray should be populated when EmitJCard=true")
	}
	if e.JSCard == nil {
		t.Fatal("jscard should still be present alongside vcardArray")
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"vcardArray":["vcard"`) {
		t.Fatalf("JSON missing vcardArray: %s", b)
	}
}

func TestEmitJCard_OffByDefault(t *testing.T) {
	d := &datasource.Domain{
		LDHName:     "example.nl",
		LastChanged: time.Now(),
		Contacts:    []datasource.Contact{fullContact("org")},
	}
	out := Domain(d, Options{Level: auth.AccessPrivileged})
	if out.Entities[0].VCardArray != nil {
		t.Fatal("vcardArray must be nil unless EmitJCard is set")
	}
}
