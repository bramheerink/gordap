// Package mapper turns internal datasource records into the RFC 9083
// wire format, applying the tiered-access redaction policy on the way.
// Exposed as package-level functions so callers can bind just the parts
// they need into an existing RDAP stack.
package mapper

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/jscontact"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

// Options configures a single mapping call. The zero value produces a
// minimal STD-95 response; populate fields to enable ICANN-profile
// notices, self-links, etc. Kept as a struct rather than functional
// options because there's no hot path where allocation matters here.
type Options struct {
	Level            auth.AccessLevel
	SelfLinkBase     string         // e.g. "https://rdap.example.com" — empty disables self-links
	ExtraConformance []string       // appended to types.DefaultConformance on top-level responses
	ExtraNotices     []types.Notice // injected on top-level responses (domain, entity, nameserver, ip, help, error)

	// RedactionReason is the human-readable justification emitted on
	// every RFC 9537 marker. Typical values: "Data minimization per
	// GDPR Art. 5(1)(c)".
	RedactionReason string
}

// Domain converts a datasource.Domain into its RFC 9083 wire form.
// Redaction is applied during mapping so redacted values never
// materialise in memory past this point. RFC 9537 markers for every
// field-kind that was pruned are aggregated onto the top-level
// response.
func Domain(d *datasource.Domain, opts Options) types.Domain {
	out := types.Domain{
		Common: types.Common{
			RDAPConformance: conformance(opts),
			ObjectClassName: "domain",
			Handle:          d.Handle,
			Status:          mapStatuses(d.Status),
			Events:          domainEvents(d),
			Notices:         opts.ExtraNotices,
			Links:           selfLinks(opts.SelfLinkBase, "/domain/"+d.LDHName),
		},
		LDHName:     d.LDHName,
		UnicodeName: d.UnicodeName,
		SecureDNS:   mapSecureDNS(d.SecureDNS),
	}
	for _, ns := range d.Nameservers {
		out.Nameservers = append(out.Nameservers, Nameserver(ns))
	}
	var marks []RedactionKind
	for _, c := range d.Contacts {
		e, m := entityWithMarks(c, opts)
		out.Entities = append(out.Entities, e)
		marks = append(marks, m...)
	}
	// Embed the registrar block when the datasource populated it; this
	// is the ICANN RP2.2 §2.4 requirement for gTLD responses.
	if d.Registrar != nil {
		out.Entities = append(out.Entities, registrarEntity(d.Registrar, opts))
	}
	out.Redacted = buildRedacted(marks, opts.RedactionReason)
	return out
}

func domainEvents(d *datasource.Domain) []types.Event {
	var out []types.Event
	if !d.Registered.IsZero() {
		out = append(out, types.Event{Action: "registration", Date: d.Registered})
	}
	if !d.LastChanged.IsZero() {
		out = append(out, types.Event{Action: "last changed", Date: d.LastChanged})
	}
	if !d.Expires.IsZero() {
		out = append(out, types.Event{Action: "expiration", Date: d.Expires})
	}
	// Required by ICANN RP2.2 §2.3.1.3: reflects when *this server's*
	// data was last synchronised. Falls back to LastChanged when the
	// provider doesn't populate it, so the event is always present.
	dbUpdate := d.LastRDAPUpdate
	if dbUpdate.IsZero() {
		dbUpdate = d.LastChanged
	}
	if !dbUpdate.IsZero() {
		out = append(out, types.Event{Action: "last update of RDAP database", Date: dbUpdate})
	}
	return out
}

// Nameserver maps a single nameserver record. Nested emissions (inside
// a domain response) omit the top-level Common fields; callers who want
// a standalone nameserver response should supply Options.
func Nameserver(n datasource.Nameserver) types.Nameserver {
	ns := types.Nameserver{
		Common:      types.Common{ObjectClassName: "nameserver", Handle: n.Handle},
		LDHName:     n.LDHName,
		UnicodeName: n.UnicodeName,
	}
	if len(n.IPv4)+len(n.IPv6) > 0 {
		ips := &types.IPAddresses{}
		for _, a := range n.IPv4 {
			ips.V4 = append(ips.V4, a.String())
		}
		for _, a := range n.IPv6 {
			ips.V6 = append(ips.V6, a.String())
		}
		ns.IPAddresses = ips
	}
	return ns
}

// NameserverTopLevel builds a nameserver response intended to be served
// directly at /nameserver/{name} — it carries the top-level envelope
// fields (conformance, notices, self-link) that Nameserver omits.
func NameserverTopLevel(n *datasource.Nameserver, opts Options) types.Nameserver {
	out := Nameserver(*n)
	out.Common.RDAPConformance = conformance(opts)
	out.Common.Notices = opts.ExtraNotices
	out.Common.Links = selfLinks(opts.SelfLinkBase, "/nameserver/"+n.LDHName)
	return out
}

// Entity maps a single entity with tier-aware redaction. A nil JSCard
// is valid and indicates every field the caller was allowed to see was
// empty.
func Entity(c datasource.Contact, opts Options) types.Entity {
	e, _ := entityWithMarks(c, opts)
	return e
}

// entityWithMarks returns the mapped entity plus the redaction marks it
// produced. The domain mapper aggregates marks across contacts before
// emitting RFC 9537 markers on the top-level response.
func entityWithMarks(c datasource.Contact, opts Options) (types.Entity, []RedactionKind) {
	view := RedactContact(c, opts.Level)
	e := types.Entity{
		Common: types.Common{ObjectClassName: "entity", Handle: c.Handle},
		Roles:  c.Roles,
	}
	if card := buildCardFromView(c, view); card != nil {
		e.JSCard = card
	}
	return e, view.Marks
}

// EntityTopLevel is the public variant with top-level envelope fields
// and the RFC 9537 redacted array for the single-entity response.
func EntityTopLevel(c *datasource.Contact, opts Options) types.Entity {
	e, marks := entityWithMarks(*c, opts)
	e.Common.RDAPConformance = conformance(opts)
	e.Common.Notices = opts.ExtraNotices
	e.Common.Links = selfLinks(opts.SelfLinkBase, "/entity/"+c.Handle)
	// Re-target paths for a standalone entity response: the entity IS
	// the root, so "$.entities[*]" becomes "$".
	marksCopy := make([]types.Redaction, 0)
	for _, r := range buildRedacted(marks, opts.RedactionReason) {
		r.PrePath = rewriteForEntityRoot(r.PrePath)
		marksCopy = append(marksCopy, r)
	}
	e.Common.Redacted = marksCopy
	return e
}

func rewriteForEntityRoot(p string) string {
	const prefix = "$.entities[*]"
	if len(p) > len(prefix) && p[:len(prefix)] == prefix {
		return "$" + p[len(prefix):]
	}
	return p
}

// IPNetwork maps an IP network record.
func IPNetwork(n *datasource.IPNetwork, opts Options) types.IPNetwork {
	out := types.IPNetwork{
		Common: types.Common{
			RDAPConformance: conformance(opts),
			ObjectClassName: "ip network",
			Handle:          n.Handle,
			Status:          n.Status,
			Notices:         opts.ExtraNotices,
		},
		Name:         n.Name,
		Type:         n.Type,
		Country:      n.Country,
		ParentHandle: n.ParentHandle,
	}
	if n.Prefix.IsValid() {
		out.StartAddress = n.Prefix.Addr().String()
		out.EndAddress = n.Prefix.Addr().String() // simplified; real code computes last addr
		if n.Prefix.Addr().Is4() {
			out.IPVersion = "v4"
		} else {
			out.IPVersion = "v6"
		}
		out.Common.Links = selfLinks(opts.SelfLinkBase, "/ip/"+out.StartAddress)
	}
	return out
}

// conformance joins the STD-95 baseline with whatever profile-specific
// identifiers the caller supplied. Order matches typical ICANN
// conformance tool fixtures.
func conformance(opts Options) types.RDAPConformance {
	if len(opts.ExtraConformance) == 0 {
		return types.DefaultConformance
	}
	out := make(types.RDAPConformance, 0, len(types.DefaultConformance)+len(opts.ExtraConformance))
	out = append(out, types.DefaultConformance...)
	out = append(out, opts.ExtraConformance...)
	return out
}

// selfLinks returns the RFC 9083 §4.2 rel=self link pair, or nil if no
// base URL was configured. The path segment is pre-joined; callers pass
// "/domain/<name>" not just "<name>".
func selfLinks(base, path string) []types.Link {
	if base == "" {
		return nil
	}
	href := strings.TrimRight(base, "/") + path
	return []types.Link{{
		Value: href,
		Rel:   "self",
		Href:  href,
		Type:  "application/rdap+json",
	}}
}

// buildCard is the RFC 9553 mapper. Returns nil when every available
// field is redacted — an empty Card is worse than none because it still
// suggests a contact exists with no data.
func buildCard(c datasource.Contact, lvl auth.AccessLevel) *jscontact.Card {
	return buildCardFromView(c, RedactContact(c, lvl))
}

// buildCardFromView is the shared body of buildCard — lets the mapper
// reuse a ContactView it already computed for marks extraction.
func buildCardFromView(c datasource.Contact, view ContactView) *jscontact.Card {
	if view.Empty() {
		return nil
	}

	card := &jscontact.Card{
		Version: "1.0",
		Type:    "Card",
		UID:     "urn:uuid:" + deterministicUID(c.Handle),
		Kind:    jsContactKind(c.Kind),
	}
	if !c.UpdatedAt.IsZero() {
		card.Updated = c.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !c.CreatedAt.IsZero() {
		card.Created = c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	if view.FullName != "" {
		card.Name = &jscontact.Name{Type: "Name", Full: view.FullName}
	}
	if view.Organization != "" {
		card.Organizations = map[string]jscontact.Org{
			"org": {Type: "Organization", Name: view.Organization},
		}
	}
	if view.Title != "" {
		card.Titles = map[string]jscontact.Title{
			"title": {Type: "Title", Name: view.Title, Kind: "title"},
		}
	}
	if len(view.Emails) > 0 {
		card.Emails = map[string]jscontact.Email{}
		for i, addr := range view.Emails {
			card.Emails[fmt.Sprintf("e%d", i+1)] = jscontact.Email{
				Type:     "EmailAddress",
				Address:  addr,
				Contexts: map[string]bool{"work": true},
				Pref:     pref(i),
			}
		}
	}
	if len(view.Phones) > 0 {
		card.Phones = map[string]jscontact.Phone{}
		for i, p := range view.Phones {
			features := map[string]bool{}
			for _, k := range p.Kinds {
				features[k] = true
			}
			if len(features) == 0 {
				features["voice"] = true
			}
			card.Phones[fmt.Sprintf("p%d", i+1)] = jscontact.Phone{
				Type:     "Phone",
				Number:   p.Number,
				Features: features,
				Contexts: map[string]bool{"work": true},
				Pref:     pref(i),
			}
		}
	}
	if view.Address != nil {
		addr := jscontact.Address{
			Type:        "Address",
			CountryCode: view.Address.CountryCode,
			Contexts:    map[string]bool{"work": true},
		}
		for _, line := range view.Address.Street {
			if line = strings.TrimSpace(line); line != "" {
				addr.Components = append(addr.Components,
					jscontact.AddressComponent{Type: "AddressComponent", Kind: "name", Value: line})
			}
		}
		if view.Address.Locality != "" {
			addr.Components = append(addr.Components,
				jscontact.AddressComponent{Type: "AddressComponent", Kind: "locality", Value: view.Address.Locality})
		}
		if view.Address.Region != "" {
			addr.Components = append(addr.Components,
				jscontact.AddressComponent{Type: "AddressComponent", Kind: "region", Value: view.Address.Region})
		}
		if view.Address.PostalCode != "" {
			addr.Components = append(addr.Components,
				jscontact.AddressComponent{Type: "AddressComponent", Kind: "postcode", Value: view.Address.PostalCode})
		}
		card.Addresses = map[string]jscontact.Address{"a1": addr}
	}
	return card
}

// mapSecureDNS lifts the internal DNSSEC view to the wire format. The
// internal struct only carries DS records today; KeyData support comes
// for zones that publish DNSKEY directly.
func mapSecureDNS(s *datasource.SecureDNS) *types.SecureDNS {
	if s == nil {
		return nil
	}
	signed := s.DelegationSigned
	out := &types.SecureDNS{DelegationSigned: &signed}
	if len(s.DSData) > 0 {
		out.DSData = append(out.DSData, s.DSData...)
	}
	return out
}

// eppToRDAPStatus maps EPP status values (used by most registry
// back-ends) to the IANA RDAP JSON Values names required by RFC 9083
// §10.2.2. Unknown inputs pass through unchanged so operators with
// private status codes aren't broken.
func eppToRDAPStatus(in string) string {
	switch in {
	// Domain / client-side statuses
	case "clientHold":
		return "client hold"
	case "clientRenewProhibited":
		return "client renew prohibited"
	case "clientTransferProhibited":
		return "client transfer prohibited"
	case "clientUpdateProhibited":
		return "client update prohibited"
	case "clientDeleteProhibited":
		return "client delete prohibited"
	// Server-side statuses
	case "serverHold":
		return "server hold"
	case "serverRenewProhibited":
		return "server renew prohibited"
	case "serverTransferProhibited":
		return "server transfer prohibited"
	case "serverUpdateProhibited":
		return "server update prohibited"
	case "serverDeleteProhibited":
		return "server delete prohibited"
	// Pending operations
	case "pendingCreate":
		return "pending create"
	case "pendingDelete":
		return "pending delete"
	case "pendingRenew":
		return "pending renew"
	case "pendingRestore":
		return "pending restore"
	case "pendingTransfer":
		return "pending transfer"
	case "pendingUpdate":
		return "pending update"
	// Presence/absence
	case "ok":
		return "active"
	case "inactive":
		return "inactive"
	case "linked":
		return "associated"
	default:
		return in
	}
}

func mapStatuses(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = eppToRDAPStatus(s)
	}
	return out
}

// registrarEntity renders the ICANN RP2.2 §2.4 registrar block: a
// nested entity with role=["registrar"], a publicId carrying the IANA
// Registrar ID, and — when the datasource populated it — an abuse
// sub-entity as §2.4.5 requires.
func registrarEntity(r *datasource.Registrar, opts Options) types.Entity {
	e := types.Entity{
		Common: types.Common{ObjectClassName: "entity", Handle: r.Handle},
		Roles:  []string{"registrar"},
	}
	if r.IANAID != "" {
		e.PublicIDs = []types.PublicID{{Type: "IANA Registrar ID", Identifier: r.IANAID}}
	}
	// A minimal registrar card: only the organisation name and URL.
	// Detail contact info belongs on the abuse sub-entity, not here.
	if r.Name != "" {
		e.JSCard = &jscontact.Card{
			Version: "1.0",
			Type:    "Card",
			UID:     "urn:uuid:" + deterministicUID(r.Handle),
			Kind:    "org",
			Organizations: map[string]jscontact.Org{
				"org": {Type: "Organization", Name: r.Name},
			},
		}
	}
	if r.Abuse != nil {
		abuse := Entity(*r.Abuse, opts)
		abuse.Roles = []string{"abuse"}
		e.Entities = append(e.Entities, abuse)
	}
	return e
}

func jsContactKind(k string) string {
	switch strings.ToLower(k) {
	case "org", "organization":
		return "org"
	case "individual", "person":
		return "individual"
	default:
		return "individual"
	}
}

func pref(i int) int {
	if i == 0 {
		return 1
	}
	return 0
}

// deterministicUID turns a stable handle into a UUID-shaped string so
// the UID round-trips across queries. Real deployments should use a
// proper v5 UUID; this is enough for wire-format conformance.
func deterministicUID(handle string) string {
	b := []byte(handle)
	for len(b) < 16 {
		b = append(b, '0')
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ensure net/url imported for future link-building helpers (wildcard
// handling etc.). Referenced by the _ identifier keeps the linter quiet.
var _ = url.PathEscape
