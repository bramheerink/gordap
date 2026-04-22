// Package postgres is the first-class DataSource for gordap. The schema
// (see schema.sql) keeps RFC 9083 mandatory / queryable fields in typed
// columns and reserves JSONB for genuinely open data (secure_dns
// variants, per-registrar `extras`). Multi-valued contact channels live
// in join tables so RFC 9536 reverse-search remains an indexed lookup.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is the subset of pgxpool we depend on. An interface makes the
// provider trivially unit-testable without a running database.
type Pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type Provider struct {
	pool Pool
}

func New(pool *pgxpool.Pool) *Provider { return &Provider{pool: pool} }

// NewWithPool is the injectable constructor used by tests.
func NewWithPool(p Pool) *Provider { return &Provider{pool: p} }

// GetDomain pulls a complete domain object — including nameservers,
// contacts, and each contact's emails + phones — in a SINGLE Postgres
// round-trip. Nested data is shaped as JSON inside the same row, then
// decoded on the Go side.
//
// The previous implementation issued 3-5 sequential round-trips per
// cold lookup (domain row → nameservers → contacts → per-contact
// emails + phones). On a cold workload that was the dominant
// component of tail latency; consolidating into one query cuts the
// round-trip count to 1 regardless of how many nameservers or
// contacts a domain has.
//
// PG's JSON aggregation costs CPU but the trade is favourable on any
// workload where network/scheduling dominates — i.e. all of them when
// the application and the database aren't on the same NUMA node.
func (p *Provider) GetDomain(ctx context.Context, name string) (*datasource.Domain, error) {
	const q = `
	SELECT d.handle, d.ldh_name, d.unicode_name, d.status,
	       d.registered_at, d.expires_at, d.last_changed,
	       d.last_rdap_update, d.secure_dns,
	       COALESCE(
	         (SELECT json_agg(json_build_object(
	            'handle',       n.handle,
	            'ldh_name',     n.ldh_name,
	            'unicode_name', n.unicode_name,
	            'ipv4',         n.ipv4,
	            'ipv6',         n.ipv6
	          ) ORDER BY n.ldh_name)
	          FROM domain_nameservers dn
	          JOIN nameservers n ON n.handle = dn.nameserver_handle
	          WHERE dn.domain_handle = d.handle),
	         '[]'::json) AS nameservers,
	       COALESCE(
	         (SELECT json_agg(json_build_object(
	            'handle',       e.handle,
	            'roles',        (SELECT array_agg(DISTINCT dc2.role)
	                               FROM domain_contacts dc2
	                              WHERE dc2.domain_handle = d.handle
	                                AND dc2.entity_handle = e.handle),
	            'kind',         e.kind,
	            'full_name',    e.full_name,
	            'organization', e.organization,
	            'title',        e.title,
	            'country_code', e.country_code,
	            'locality',     e.locality,
	            'region',       e.region,
	            'postal_code',  e.postal_code,
	            'street',       e.street,
	            'created_at',   e.created_at,
	            'updated_at',   e.updated_at,
	            'extras',       e.extras,
	            'emails',       COALESCE((SELECT array_agg(ee.email::text ORDER BY ee.email)
	                                       FROM entity_emails ee
	                                      WHERE ee.entity_handle = e.handle),
	                                     '{}'::text[]),
	            'phones',       COALESCE((SELECT json_agg(json_build_object('number', ep.number, 'kinds', ep.kinds) ORDER BY ep.number)
	                                       FROM entity_phones ep
	                                      WHERE ep.entity_handle = e.handle),
	                                     '[]'::json)
	          ) ORDER BY e.handle)
	          FROM domain_contacts dc
	          JOIN entities e ON e.handle = dc.entity_handle
	          WHERE dc.domain_handle = d.handle),
	         '[]'::json) AS contacts
	  FROM domains d
	 WHERE d.ldh_name = lower($1)`

	var (
		d         datasource.Domain
		expires   *time.Time
		secureDNS []byte
		nsJSON    []byte
		contJSON  []byte
	)
	row := p.pool.QueryRow(ctx, q, name)
	if err := row.Scan(&d.Handle, &d.LDHName, &d.UnicodeName, &d.Status,
		&d.Registered, &expires, &d.LastChanged, &d.LastRDAPUpdate, &secureDNS,
		&nsJSON, &contJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, datasource.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get domain: %w", err)
	}
	if expires != nil {
		d.Expires = *expires
	}
	if len(secureDNS) > 0 {
		if err := json.Unmarshal(secureDNS, &d.SecureDNS); err != nil {
			return nil, fmt.Errorf("postgres: decode secureDNS: %w", err)
		}
	}
	ns, err := decodeNameservers(nsJSON)
	if err != nil {
		return nil, err
	}
	d.Nameservers = ns
	contacts, err := decodeContacts(contJSON)
	if err != nil {
		return nil, err
	}
	d.Contacts = contacts
	return &d, nil
}

// decodeNameservers turns the json_agg output into native types.
func decodeNameservers(raw []byte) ([]datasource.Nameserver, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []struct {
		Handle      string   `json:"handle"`
		LDHName     string   `json:"ldh_name"`
		UnicodeName string   `json:"unicode_name"`
		IPv4        []string `json:"ipv4"`
		IPv6        []string `json:"ipv6"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("postgres: decode nameservers JSON: %w", err)
	}
	out := make([]datasource.Nameserver, 0, len(rows))
	for _, r := range rows {
		n := datasource.Nameserver{Handle: r.Handle, LDHName: r.LDHName, UnicodeName: r.UnicodeName}
		for _, s := range r.IPv4 {
			if a, err := netip.ParseAddr(s); err == nil {
				n.IPv4 = append(n.IPv4, a)
			}
		}
		for _, s := range r.IPv6 {
			if a, err := netip.ParseAddr(s); err == nil {
				n.IPv6 = append(n.IPv6, a)
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// decodeContacts turns the nested contact JSON (with emails + phones
// folded in) into datasource.Contact values.
func decodeContacts(raw []byte) ([]datasource.Contact, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []struct {
		Handle       string          `json:"handle"`
		Roles        []string        `json:"roles"`
		Kind         string          `json:"kind"`
		FullName     string          `json:"full_name"`
		Organization string          `json:"organization"`
		Title        string          `json:"title"`
		CountryCode  *string         `json:"country_code"`
		Locality     string          `json:"locality"`
		Region       string          `json:"region"`
		PostalCode   string          `json:"postal_code"`
		Street       []string        `json:"street"`
		CreatedAt    time.Time       `json:"created_at"`
		UpdatedAt    time.Time       `json:"updated_at"`
		Extras       json.RawMessage `json:"extras"`
		Emails       []string        `json:"emails"`
		Phones       []struct {
			Number string   `json:"number"`
			Kinds  []string `json:"kinds"`
		} `json:"phones"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("postgres: decode contacts JSON: %w", err)
	}
	out := make([]datasource.Contact, 0, len(rows))
	for _, r := range rows {
		c := datasource.Contact{
			Handle: r.Handle, Roles: r.Roles, Kind: r.Kind,
			FullName: r.FullName, Organization: r.Organization, Title: r.Title,
			Emails:    r.Emails,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		if r.CountryCode != nil || r.Locality != "" || r.Region != "" || r.PostalCode != "" || len(r.Street) > 0 {
			addr := &datasource.Address{
				Locality: r.Locality, Region: r.Region,
				PostalCode: r.PostalCode, Street: r.Street,
			}
			if r.CountryCode != nil {
				addr.CountryCode = *r.CountryCode
			}
			c.Address = addr
		}
		if len(r.Extras) > 0 && string(r.Extras) != "null" {
			_ = json.Unmarshal(r.Extras, &c.Extras)
		}
		for _, p := range r.Phones {
			c.Phones = append(c.Phones, datasource.Phone{Number: p.Number, Kinds: p.Kinds})
		}
		out = append(out, c)
	}
	return out, nil
}

func (p *Provider) domainNameservers(ctx context.Context, handle string) ([]datasource.Nameserver, error) {
	const q = `
	SELECT n.handle, n.ldh_name, n.unicode_name, n.ipv4, n.ipv6
	  FROM domain_nameservers dn
	  JOIN nameservers n ON n.handle = dn.nameserver_handle
	 WHERE dn.domain_handle = $1`
	rows, err := p.pool.Query(ctx, q, handle)
	if err != nil {
		return nil, fmt.Errorf("postgres: list nameservers: %w", err)
	}
	defer rows.Close()
	var out []datasource.Nameserver
	for rows.Next() {
		var n datasource.Nameserver
		var v4, v6 []netip.Addr
		if err := rows.Scan(&n.Handle, &n.LDHName, &n.UnicodeName, &v4, &v6); err != nil {
			return nil, err
		}
		n.IPv4, n.IPv6 = v4, v6
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Provider) domainContacts(ctx context.Context, handle string) ([]datasource.Contact, error) {
	// Single query with correlated subqueries for emails + phones. The
	// older N+1 pattern (one followup query per contact for emails,
	// another for phones) was the dominant tail in stress tests with
	// >40% cache miss — a domain with one contact triggered five PG
	// round-trips. This version is one round-trip per domain regardless
	// of contact count.
	const q = `
	SELECT e.handle, array_agg(DISTINCT dc.role) AS roles,
	       e.kind, e.full_name, e.organization, e.title,
	       e.country_code, e.locality, e.region, e.postal_code, e.street,
	       e.created_at, e.updated_at, e.extras,
	       COALESCE((SELECT array_agg(ee.email::text ORDER BY ee.email)
	                   FROM entity_emails ee WHERE ee.entity_handle = e.handle),
	                '{}'::text[]) AS emails,
	       COALESCE((SELECT json_agg(json_build_object('number', ep.number, 'kinds', ep.kinds) ORDER BY ep.number)
	                   FROM entity_phones ep WHERE ep.entity_handle = e.handle),
	                '[]'::json) AS phones
	  FROM domain_contacts dc
	  JOIN entities e ON e.handle = dc.entity_handle
	 WHERE dc.domain_handle = $1
	 GROUP BY e.handle`
	rows, err := p.pool.Query(ctx, q, handle)
	if err != nil {
		return nil, fmt.Errorf("postgres: list contacts: %w", err)
	}
	defer rows.Close()
	var out []datasource.Contact
	for rows.Next() {
		c, err := scanContactRowFull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

// scanContactRowFull reads one entity row plus its channels in the
// same scan, eliminating the per-contact N+1 round-trips. The phones
// JSON shape mirrors what hydrateContactDetails wrote in the v1
// schema — keep them in sync if either side changes.
func scanContactRowFull(s scanner) (datasource.Contact, error) {
	var (
		c          datasource.Contact
		country    *string
		locality   string
		region     string
		postal     string
		street     []string
		extrasJSON []byte
		emails     []string
		phonesJSON []byte
	)
	if err := s.Scan(&c.Handle, &c.Roles, &c.Kind,
		&c.FullName, &c.Organization, &c.Title,
		&country, &locality, &region, &postal, &street,
		&c.CreatedAt, &c.UpdatedAt, &extrasJSON,
		&emails, &phonesJSON,
	); err != nil {
		return datasource.Contact{}, err
	}
	if country != nil || locality != "" || region != "" || postal != "" || len(street) > 0 {
		addr := &datasource.Address{Locality: locality, Region: region, PostalCode: postal, Street: street}
		if country != nil {
			addr.CountryCode = *country
		}
		c.Address = addr
	}
	if len(extrasJSON) > 0 {
		if err := json.Unmarshal(extrasJSON, &c.Extras); err != nil {
			return datasource.Contact{}, fmt.Errorf("postgres: decode entity extras: %w", err)
		}
	}
	c.Emails = emails
	if len(phonesJSON) > 0 {
		var raw []struct {
			Number string   `json:"number"`
			Kinds  []string `json:"kinds"`
		}
		if err := json.Unmarshal(phonesJSON, &raw); err != nil {
			return datasource.Contact{}, fmt.Errorf("postgres: decode phones JSON: %w", err)
		}
		for _, p := range raw {
			c.Phones = append(c.Phones, datasource.Phone{Number: p.Number, Kinds: p.Kinds})
		}
	}
	return c, nil
}

// scanContactRow is kept for the test that exercises the legacy scan
// shape (no joined channels). New call sites should use
// scanContactRowFull instead.
func scanContactRow(s scanner) (datasource.Contact, error) {
	var (
		c           datasource.Contact
		country     *string
		locality    string
		region      string
		postal      string
		street      []string
		extrasJSON  []byte
	)
	if err := s.Scan(&c.Handle, &c.Roles, &c.Kind,
		&c.FullName, &c.Organization, &c.Title,
		&country, &locality, &region, &postal, &street,
		&c.CreatedAt, &c.UpdatedAt, &extrasJSON,
	); err != nil {
		return datasource.Contact{}, err
	}
	if country != nil || locality != "" || region != "" || postal != "" || len(street) > 0 {
		addr := &datasource.Address{Locality: locality, Region: region, PostalCode: postal, Street: street}
		if country != nil {
			addr.CountryCode = *country
		}
		c.Address = addr
	}
	if len(extrasJSON) > 0 {
		if err := json.Unmarshal(extrasJSON, &c.Extras); err != nil {
			return datasource.Contact{}, fmt.Errorf("postgres: decode entity extras: %w", err)
		}
	}
	return c, nil
}

// attachChannels remains for any caller that scans the legacy shape
// (not used by GetEntity / domainContacts anymore). Kept callable so a
// future bulk-load path could reuse it; remove if unused at next
// audit.
func (p *Provider) attachChannels(ctx context.Context, c *datasource.Contact) error {
	rows, err := p.pool.Query(ctx,
		`SELECT email FROM entity_emails WHERE entity_handle = $1 ORDER BY email`, c.Handle)
	if err != nil {
		return fmt.Errorf("postgres: list emails: %w", err)
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		c.Emails = append(c.Emails, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = p.pool.Query(ctx,
		`SELECT number, kinds FROM entity_phones WHERE entity_handle = $1 ORDER BY number`, c.Handle)
	if err != nil {
		return fmt.Errorf("postgres: list phones: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ph datasource.Phone
		if err := rows.Scan(&ph.Number, &ph.Kinds); err != nil {
			return err
		}
		c.Phones = append(c.Phones, ph)
	}
	return rows.Err()
}

func (p *Provider) GetEntity(ctx context.Context, handle string) (*datasource.Contact, error) {
	const q = `
	SELECT handle, ARRAY[]::text[] AS roles, kind,
	       full_name, organization, title,
	       country_code, locality, region, postal_code, street,
	       created_at, updated_at, extras
	  FROM entities WHERE handle = $1`
	row := p.pool.QueryRow(ctx, q, handle)
	c, err := scanContactRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, datasource.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get entity: %w", err)
	}
	if err := p.attachChannels(ctx, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *Provider) GetNameserver(ctx context.Context, name string) (*datasource.Nameserver, error) {
	const q = `SELECT handle, ldh_name, unicode_name, ipv4, ipv6
	             FROM nameservers WHERE lower(ldh_name) = lower($1) LIMIT 1`
	var n datasource.Nameserver
	var v4, v6 []netip.Addr
	row := p.pool.QueryRow(ctx, q, name)
	if err := row.Scan(&n.Handle, &n.LDHName, &n.UnicodeName, &v4, &v6); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, datasource.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get nameserver: %w", err)
	}
	n.IPv4, n.IPv6 = v4, v6
	return &n, nil
}

func (p *Provider) GetIPNetwork(ctx context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	// `>>=` is the PostgreSQL CIDR containment operator; with the GIST
	// index declared in schema.sql this is a single logarithmic lookup.
	const q = `
	SELECT handle, prefix, name, type, country, parent_handle,
	       status, registered_at, last_changed
	  FROM ip_networks
	 WHERE prefix >>= $1
	 ORDER BY masklen(prefix) DESC
	 LIMIT 1`
	var (
		n      datasource.IPNetwork
		prefix string
	)
	row := p.pool.QueryRow(ctx, q, ip.String())
	if err := row.Scan(&n.Handle, &prefix, &n.Name, &n.Type, &n.Country,
		&n.ParentHandle, &n.Status, &n.Registered, &n.LastChanged); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, datasource.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get ip network: %w", err)
	}
	pfx, err := netip.ParsePrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("postgres: decode prefix %q: %w", prefix, err)
	}
	n.Prefix = pfx
	return &n, nil
}
