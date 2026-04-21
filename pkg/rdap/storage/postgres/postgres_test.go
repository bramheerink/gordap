package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fakeScanner implements the scanner interface used by scanContactRow. The
// fields are assigned in the order the query returns them.
type fakeScanner struct {
	handle       string
	roles        []string
	kind         string
	fullName     string
	organization string
	title        string
	country      *string
	locality     string
	region       string
	postalCode   string
	street       []string
	createdAt    time.Time
	updatedAt    time.Time
	extras       []byte
	scanErr      error
}

func (f *fakeScanner) Scan(dest ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	*(dest[0].(*string)) = f.handle
	*(dest[1].(*[]string)) = f.roles
	*(dest[2].(*string)) = f.kind
	*(dest[3].(*string)) = f.fullName
	*(dest[4].(*string)) = f.organization
	*(dest[5].(*string)) = f.title
	*(dest[6].(**string)) = f.country
	*(dest[7].(*string)) = f.locality
	*(dest[8].(*string)) = f.region
	*(dest[9].(*string)) = f.postalCode
	*(dest[10].(*[]string)) = f.street
	*(dest[11].(*time.Time)) = f.createdAt
	*(dest[12].(*time.Time)) = f.updatedAt
	*(dest[13].(*[]byte)) = f.extras
	return nil
}

func TestScanContactRow_FullRow(t *testing.T) {
	nl := "NL"
	s := &fakeScanner{
		handle: "H-1", roles: []string{"registrant"}, kind: "org",
		fullName: "Jane Doe", organization: "Example B.V.", title: "CTO",
		country: &nl, locality: "Amsterdam", region: "NH", postalCode: "1015BA",
		street:    []string{"Herengracht 1"},
		createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		updatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		extras:    json.RawMessage(`{"loa":"high","vat":"NL001"}`),
	}
	c, err := scanContactRow(s)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if c.Handle != "H-1" || c.Kind != "org" || c.FullName != "Jane Doe" {
		t.Fatalf("scalar fields: %+v", c)
	}
	if c.Address == nil || c.Address.CountryCode != "NL" || c.Address.PostalCode != "1015BA" {
		t.Fatalf("address: %+v", c.Address)
	}
	if got, _ := c.Extras["loa"].(string); got != "high" {
		t.Fatalf("extras should be populated: %+v", c.Extras)
	}
	if got, _ := c.Extras["vat"].(string); got != "NL001" {
		t.Fatalf("extras vat: %+v", c.Extras)
	}
}

func TestScanContactRow_NoAddress(t *testing.T) {
	s := &fakeScanner{handle: "H-2", kind: "individual"}
	c, err := scanContactRow(s)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if c.Address != nil {
		t.Fatalf("expected nil address for empty fields, got %+v", c.Address)
	}
}

func TestScanContactRow_ScanError(t *testing.T) {
	s := &fakeScanner{scanErr: errors.New("bang")}
	if _, err := scanContactRow(s); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestScanContactRow_MalformedExtras(t *testing.T) {
	s := &fakeScanner{handle: "H-3", kind: "org", extras: []byte(`{not json`)}
	if _, err := scanContactRow(s); err == nil {
		t.Fatal("expected decode error on malformed extras JSONB")
	}
}
