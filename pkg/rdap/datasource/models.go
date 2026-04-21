package datasource

import (
	"net/netip"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/types"
)

// The types in this file are the *internal* representation — what providers
// return and what the RDAP mapper consumes. They stay independent of
// wire-format concerns (field ordering, omitempty rules, RFC 9537 markers)
// so a storage engine can evolve without touching the JSON schema.

// Domain is the storage-layer view of a domain registration.
type Domain struct {
	Handle      string
	LDHName     string
	UnicodeName string
	Status      []string
	Registered  time.Time
	Expires     time.Time
	LastChanged time.Time
	// LastRDAPUpdate is when this record was last synchronised from the
	// authoritative backend. Drives the "last update of RDAP database"
	// event required by ICANN RP2.2 §2.3.1.3. Defaults to LastChanged if
	// a provider doesn't populate it.
	LastRDAPUpdate time.Time
	Nameservers    []Nameserver
	SecureDNS      *SecureDNS
	Contacts       []Contact
	Registrar      *Registrar
}

type Nameserver struct {
	Handle      string
	LDHName     string
	UnicodeName string
	IPv4        []netip.Addr
	IPv6        []netip.Addr
}

type SecureDNS struct {
	DelegationSigned bool
	DSData           []types.DSData
}

// Contact is the raw contact record straight from the datasource. Redaction
// happens later; this struct carries every field and the redaction layer
// prunes based on the caller's access tier.
//
// Extras is the provider's escape hatch for registrar-specific JSONB data
// (LoA scores, VAT IDs, regional flags). The RDAP mapper ignores it; a
// custom mapper can consult it for extension-aware output.
type Contact struct {
	Handle       string
	Roles        []string
	Kind         string // individual|org
	FullName     string
	Organization string
	Title        string
	Emails       []string
	Phones       []Phone
	Address      *Address
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Extras       map[string]any
}

type Phone struct {
	Number string
	Kinds  []string // voice, fax, mobile, ...
}

type Address struct {
	Street      []string
	Locality    string
	Region      string
	PostalCode  string
	CountryCode string
}

type Registrar struct {
	Handle string
	Name   string
	IANAID string
	URL    string
	Abuse  *Contact
}

// IPNetwork is a storage-layer IP block. Uses netip.Prefix to stay
// allocation-free and IPv4/IPv6 agnostic.
type IPNetwork struct {
	Handle       string
	Prefix       netip.Prefix
	Name         string
	Type         string
	Country      string
	ParentHandle string
	Status       []string
	Registered   time.Time
	LastChanged  time.Time
	Contacts     []Contact
}
