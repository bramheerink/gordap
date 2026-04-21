// Package types contains the RFC 9083 wire-format structs for RDAP
// responses. Entity objects embed *jscontact.Card per the regext draft
// binding JSContact to RDAP; the types package is otherwise dependency-free.
package types

import (
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/jscontact"
)

// RDAPConformance is the set of extension identifiers the server claims to
// implement. Per RFC 9083 §4.1 this MUST appear on the top-level response.
type RDAPConformance []string

// Link is RFC 8288 compliant, serialised per RFC 9083 §4.2.
type Link struct {
	Value    string   `json:"value,omitempty"`
	Rel      string   `json:"rel,omitempty"`
	Href     string   `json:"href"`
	HrefLang []string `json:"hreflang,omitempty"`
	Title    string   `json:"title,omitempty"`
	Media    string   `json:"media,omitempty"`
	Type     string   `json:"type,omitempty"`
}

// Notice and Remark share structure (RFC 9083 §4.3).
type Notice struct {
	Title       string   `json:"title,omitempty"`
	Type        string   `json:"type,omitempty"`
	Description []string `json:"description,omitempty"`
	Links       []Link   `json:"links,omitempty"`
}

// Event per RFC 9083 §4.5.
type Event struct {
	Action string    `json:"eventAction"`
	Actor  string    `json:"eventActor,omitempty"`
	Date   time.Time `json:"eventDate"`
	Links  []Link    `json:"links,omitempty"`
}

// PublicID per RFC 9083 §4.8.
type PublicID struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

// Common holds fields shared by every RDAP object class (RFC 9083 §4).
// Embedded rather than composed to match the flat JSON structure on the wire.
type Common struct {
	RDAPConformance RDAPConformance `json:"rdapConformance,omitempty"`
	ObjectClassName string          `json:"objectClassName"`
	Handle          string          `json:"handle,omitempty"`
	Links           []Link          `json:"links,omitempty"`
	Notices         []Notice        `json:"notices,omitempty"`
	Remarks         []Notice        `json:"remarks,omitempty"`
	Events          []Event         `json:"events,omitempty"`
	Status          []string        `json:"status,omitempty"`
	Port43          string          `json:"port43,omitempty"`
	Lang            string          `json:"lang,omitempty"`
	// Redacted carries RFC 9537 markers signalling which members were
	// removed/replaced compared to the full record. Only populated on
	// top-level responses; nested entities do not repeat this array.
	Redacted []Redaction `json:"redacted,omitempty"`
}

// Redaction is RFC 9537's signal that a field is missing/masked on
// purpose. Method is one of removal|emptyValue|partialValue|replacementValue.
type Redaction struct {
	Name            RedactionLabel  `json:"name"`
	Reason          *RedactionLabel `json:"reason,omitempty"`
	PrePath         string          `json:"prePath,omitempty"`
	PostPath        string          `json:"postPath,omitempty"`
	ReplacementPath string          `json:"replacementPath,omitempty"`
	PathLang        string          `json:"pathLang,omitempty"` // typically "jsonpath"
	Method          string          `json:"method"`
}

// RedactionLabel is the common shape for Redaction's `name` and
// `reason` members: at least one of description / type is set.
type RedactionLabel struct {
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
}

// Domain is the RDAP serialisation of a domain object (RFC 9083 §5.3).
type Domain struct {
	Common
	LDHName     string       `json:"ldhName,omitempty"`
	UnicodeName string       `json:"unicodeName,omitempty"`
	Variants    []Variant    `json:"variants,omitempty"`
	Nameservers []Nameserver `json:"nameservers,omitempty"`
	SecureDNS   *SecureDNS   `json:"secureDNS,omitempty"`
	Entities    []Entity     `json:"entities,omitempty"`
	PublicIDs   []PublicID   `json:"publicIds,omitempty"`
	Network     *IPNetwork   `json:"network,omitempty"`
}

type Variant struct {
	Relation     []string      `json:"relation,omitempty"`
	IDNTable     string        `json:"idnTable,omitempty"`
	VariantNames []VariantName `json:"variantNames,omitempty"`
}

type VariantName struct {
	LDHName     string `json:"ldhName,omitempty"`
	UnicodeName string `json:"unicodeName,omitempty"`
}

type SecureDNS struct {
	ZoneSigned       *bool     `json:"zoneSigned,omitempty"`
	DelegationSigned *bool     `json:"delegationSigned,omitempty"`
	MaxSigLife       int       `json:"maxSigLife,omitempty"`
	DSData           []DSData  `json:"dsData,omitempty"`
	KeyData          []KeyData `json:"keyData,omitempty"`
}

type DSData struct {
	KeyTag     int    `json:"keyTag"`
	Algorithm  int    `json:"algorithm"`
	Digest     string `json:"digest"`
	DigestType int    `json:"digestType"`
}

type KeyData struct {
	Flags     int    `json:"flags"`
	Protocol  int    `json:"protocol"`
	PublicKey string `json:"publicKey"`
	Algorithm int    `json:"algorithm"`
}

// Nameserver is the RDAP serialisation per RFC 9083 §5.2.
type Nameserver struct {
	Common
	LDHName     string       `json:"ldhName,omitempty"`
	UnicodeName string       `json:"unicodeName,omitempty"`
	IPAddresses *IPAddresses `json:"ipAddresses,omitempty"`
	Entities    []Entity     `json:"entities,omitempty"`
}

type IPAddresses struct {
	V4 []string `json:"v4,omitempty"`
	V6 []string `json:"v6,omitempty"`
}

// Entity per RFC 9083 §5.1. Contact information is carried in the
// JSContact Card (`jscard`) per draft-ietf-regext-rdap-jscontact, with
// an optional jCard projection (`vcardArray`, RFC 7095) for clients
// and conformance tools on the ICANN side of the migration. Emitting
// both gives maximum compatibility.
type Entity struct {
	Common
	Roles        []string        `json:"roles,omitempty"`
	JSCard       *jscontact.Card `json:"jscard,omitempty"`
	VCardArray   []any           `json:"vcardArray,omitempty"`
	PublicIDs    []PublicID      `json:"publicIds,omitempty"`
	AsEventActor []Event         `json:"asEventActor,omitempty"`
	Entities     []Entity        `json:"entities,omitempty"`
}

// IPNetwork per RFC 9083 §5.4.
type IPNetwork struct {
	Common
	StartAddress string   `json:"startAddress,omitempty"`
	EndAddress   string   `json:"endAddress,omitempty"`
	IPVersion    string   `json:"ipVersion,omitempty"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	Country      string   `json:"country,omitempty"`
	ParentHandle string   `json:"parentHandle,omitempty"`
	Entities     []Entity `json:"entities,omitempty"`
}

// Error is the RDAP error response per RFC 9083 §6.
type Error struct {
	Common
	ErrorCode   int      `json:"errorCode"`
	Title       string   `json:"title,omitempty"`
	Description []string `json:"description,omitempty"`
}

// --- Search results (RFC 9083 §8 + RFC 8977 paging) ---------------------

// DomainSearchResults is the envelope returned by /domains. The array
// field name is fixed by RFC 9083 §8.
type DomainSearchResults struct {
	Common
	DomainSearchResults []Domain        `json:"domainSearchResults,omitempty"`
	PagingMetadata      *PagingMetadata `json:"paging_metadata,omitempty"`
}

// EntitySearchResults is the envelope returned by /entities.
type EntitySearchResults struct {
	Common
	EntitySearchResults []Entity        `json:"entitySearchResults,omitempty"`
	PagingMetadata      *PagingMetadata `json:"paging_metadata,omitempty"`
}

// NameserverSearchResults is the envelope returned by /nameservers.
type NameserverSearchResults struct {
	Common
	NameserverSearchResults []Nameserver    `json:"nameserverSearchResults,omitempty"`
	PagingMetadata          *PagingMetadata `json:"paging_metadata,omitempty"`
}

// PagingMetadata is RFC 8977's companion to search responses. Omitted
// when the whole result set fits in one page.
type PagingMetadata struct {
	TotalCount int    `json:"totalCount,omitempty"`
	PageNumber int    `json:"pageNumber,omitempty"`
	PageSize   int    `json:"pageSize,omitempty"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// DefaultConformance is emitted on every top-level response. Extensions
// actually implemented SHOULD be appended here.
var DefaultConformance = RDAPConformance{
	"rdap_level_0",
	"jscontact_level_0", // draft-ietf-regext-rdap-jscontact
	"redacted",          // RFC 9537 signalling (see pkg/rdap/redaction)
}
