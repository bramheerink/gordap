package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/search"
)

// Postgres search uses LIKE with the standard `%` wildcard. For big
// registries (>1M rows) operators should:
//
//	CREATE EXTENSION IF NOT EXISTS pg_trgm;
//	CREATE INDEX domains_ldh_trgm ON domains USING gin (ldh_name gin_trgm_ops);
//	CREATE INDEX entities_fullname_trgm ON entities USING gin (full_name gin_trgm_ops);
//	CREATE INDEX entity_emails_email_trgm ON entity_emails USING gin (email gin_trgm_ops);
//
// With those indexes LIKE queries stay sub-100ms up to ~100M rows.
// Larger deployments belong on OpenSearch (see PERFORMANCE.md).

// ilikePattern turns an RFC 7482 partial-match pattern ("example.*")
// into a Postgres LIKE pattern ("example.%") and escapes embedded
// wildcard metacharacters so user input can't broaden the match.
func ilikePattern(in string) string {
	var b strings.Builder
	for _, r := range in {
		switch r {
		case '*':
			b.WriteByte('%')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	// Lowercase so case-sensitive `LIKE` against the always-lowercase
	// ldh_name / handle columns can use the text_pattern_ops B-tree
	// for prefix matches and the gin_trgm_ops index for substring
	// patterns. ILIKE was index-blind in our schema and forced a
	// full sequential scan on every search request.
	return strings.ToLower(b.String())
}

func (p *Provider) SearchDomains(ctx context.Context, q search.Query) (*search.Result[datasource.Domain], error) {
	if q.Terms["name"] == "" {
		return nil, search.ErrNotImplemented
	}
	pattern := ilikePattern(q.Terms["name"])
	limit := search.ClampLimit(q.Limit, 50, 500)

	const countQ = `SELECT count(*) FROM domains WHERE ldh_name LIKE $1`
	var total int
	if err := p.pool.QueryRow(ctx, countQ, pattern).Scan(&total); err != nil {
		return nil, fmt.Errorf("postgres: count domains: %w", err)
	}

	const selQ = `
	SELECT handle, ldh_name, unicode_name, status,
	       registered_at, expires_at, last_changed, last_rdap_update
	  FROM domains
	 WHERE ldh_name LIKE $1
	 ORDER BY ldh_name
	 LIMIT $2 OFFSET $3`
	rows, err := p.pool.Query(ctx, selQ, pattern, limit, q.Offset)
	if err != nil {
		return nil, fmt.Errorf("postgres: list domains: %w", err)
	}
	defer rows.Close()
	var items []datasource.Domain
	for rows.Next() {
		var d datasource.Domain
		var expires *any
		if err := rows.Scan(&d.Handle, &d.LDHName, &d.UnicodeName, &d.Status,
			&d.Registered, &expires, &d.LastChanged, &d.LastRDAPUpdate); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return &search.Result[datasource.Domain]{Items: items, Total: total}, rows.Err()
}

func (p *Provider) SearchEntities(ctx context.Context, q search.Query) (*search.Result[datasource.Contact], error) {
	var (
		where []string
		args  []any
	)
	if v, ok := q.Terms["fn"]; ok {
		args = append(args, ilikePattern(v))
		where = append(where, fmt.Sprintf("(e.full_name LIKE $%d OR e.organization LIKE $%d)", len(args), len(args)))
	}
	if v, ok := q.Terms["handle"]; ok {
		args = append(args, ilikePattern(v))
		where = append(where, fmt.Sprintf("e.handle LIKE $%d", len(args)))
	}
	if v, ok := q.Terms["email"]; ok {
		args = append(args, ilikePattern(v))
		where = append(where,
			fmt.Sprintf("EXISTS (SELECT 1 FROM entity_emails ee WHERE ee.entity_handle = e.handle AND ee.email LIKE $%d)", len(args)))
	}
	if v, ok := q.Terms["countryCode"]; ok {
		args = append(args, strings.ToUpper(v))
		where = append(where, fmt.Sprintf("e.country_code = $%d", len(args)))
	}
	if len(where) == 0 {
		return nil, search.ErrNotImplemented
	}

	limit := search.ClampLimit(q.Limit, 50, 500)
	whereClause := strings.Join(where, " AND ")

	// Single scan with COUNT(*) OVER() — plus inlined emails/phones so
	// search results don't trigger a per-row N+1 from the caller.
	selSQL := `
	SELECT e.handle, ARRAY[]::text[] AS roles, e.kind,
	       e.full_name, e.organization, e.title,
	       e.country_code, e.locality, e.region, e.postal_code, e.street,
	       e.created_at, e.updated_at, e.extras,
	       COALESCE((SELECT array_agg(ee.email::text ORDER BY ee.email)
	                   FROM entity_emails ee WHERE ee.entity_handle = e.handle),
	                '{}'::text[]) AS emails,
	       COALESCE((SELECT json_agg(json_build_object('number', ep.number, 'kinds', ep.kinds) ORDER BY ep.number)
	                   FROM entity_phones ep WHERE ep.entity_handle = e.handle),
	                '[]'::json) AS phones,
	       count(*) OVER() AS total_count
	  FROM entities e
	 WHERE ` + whereClause + fmt.Sprintf(` ORDER BY e.handle LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, q.Offset)

	rows, err := p.pool.Query(ctx, selSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: search entities: %w", err)
	}
	defer rows.Close()
	var (
		items []datasource.Contact
		total int
	)
	for rows.Next() {
		c, err := scanEntityWithTotal(rows, &total)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return &search.Result[datasource.Contact]{Items: items, Total: total}, rows.Err()
}

// scanEntityWithTotal scans the same shape as scanContactRowFull plus
// the trailing total_count column for the search COUNT(*) OVER() trick.
func scanEntityWithTotal(s scanner, total *int) (datasource.Contact, error) {
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
		&emails, &phonesJSON, total,
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
		_ = json.Unmarshal(extrasJSON, &c.Extras)
	}
	c.Emails = emails
	if len(phonesJSON) > 0 {
		var raw []struct {
			Number string   `json:"number"`
			Kinds  []string `json:"kinds"`
		}
		_ = json.Unmarshal(phonesJSON, &raw)
		for _, p := range raw {
			c.Phones = append(c.Phones, datasource.Phone{Number: p.Number, Kinds: p.Kinds})
		}
	}
	return c, nil
}

func (p *Provider) SearchNameservers(ctx context.Context, q search.Query) (*search.Result[datasource.Nameserver], error) {
	pattern, ok := q.Terms["name"]
	if !ok {
		return nil, search.ErrNotImplemented
	}
	limit := search.ClampLimit(q.Limit, 50, 500)

	const q2 = `
	SELECT handle, ldh_name, unicode_name, ipv4, ipv6,
	       count(*) OVER() AS total_count
	  FROM nameservers WHERE ldh_name LIKE $1
	  ORDER BY ldh_name LIMIT $2 OFFSET $3`
	rows, err := p.pool.Query(ctx, q2, ilikePattern(pattern), limit, q.Offset)
	if err != nil {
		return nil, fmt.Errorf("postgres: search nameservers: %w", err)
	}
	defer rows.Close()
	var (
		items []datasource.Nameserver
		total int
	)
	for rows.Next() {
		var n datasource.Nameserver
		if err := rows.Scan(&n.Handle, &n.LDHName, &n.UnicodeName, &n.IPv4, &n.IPv6, &total); err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return &search.Result[datasource.Nameserver]{Items: items, Total: total}, rows.Err()
}
