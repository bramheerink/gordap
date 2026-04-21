package handlers

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/mapper"
	"github.com/bramheerink/gordap/pkg/rdap/search"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

// Search is the set of allowed query parameters per object class.
// Everything else in the query string is ignored; unknown predicates
// fall through to the index's own handling.
var (
	domainSearchFields     = []string{"name", "nsLdhName", "nsIp"}
	entitySearchFields     = []string{"fn", "handle", "email", "countryCode"}
	nameserverSearchFields = []string{"name", "ip"}
)

// HandleDomainsSearch implements GET /domains?name=*.
func (s *Server) HandleDomainsSearch(w http.ResponseWriter, r *http.Request) {
	if s.Search == nil {
		writeError(w, http.StatusNotImplemented, "Search not enabled", s.Notices)
		return
	}
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	q := buildQuery(r, domainSearchFields)
	if len(q.Terms) == 0 {
		writeError(w, http.StatusBadRequest, "Missing search predicate", s.Notices)
		return
	}
	res, err := s.Search.SearchDomains(r.Context(), q)
	if err != nil {
		writeSearchErr(w, err, s.Notices)
		return
	}
	opts := s.opts(r)
	envelope := types.DomainSearchResults{
		Common: types.Common{
			RDAPConformance: mapperConformance(opts),
			ObjectClassName: "domainSearchResults",
			Notices:         s.Notices,
			Links:           searchSelfLink(s.SelfLinkBase, r),
		},
		PagingMetadata: pagingMeta(q, res.Total, res.NextCursor),
	}
	for _, d := range res.Items {
		envelope.DomainSearchResults = append(envelope.DomainSearchResults, mapper.Domain(&d, opts))
	}
	writeJSON(w, http.StatusOK, envelope)
}

// HandleEntitiesSearch implements GET /entities?fn|handle|email|countryCode=*.
func (s *Server) HandleEntitiesSearch(w http.ResponseWriter, r *http.Request) {
	if s.Search == nil {
		writeError(w, http.StatusNotImplemented, "Search not enabled", s.Notices)
		return
	}
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	q := buildQuery(r, entitySearchFields)
	if len(q.Terms) == 0 {
		writeError(w, http.StatusBadRequest, "Missing search predicate", s.Notices)
		return
	}
	res, err := s.Search.SearchEntities(r.Context(), q)
	if err != nil {
		writeSearchErr(w, err, s.Notices)
		return
	}
	opts := s.opts(r)
	envelope := types.EntitySearchResults{
		Common: types.Common{
			RDAPConformance: mapperConformance(opts),
			ObjectClassName: "entitySearchResults",
			Notices:         s.Notices,
			Links:           searchSelfLink(s.SelfLinkBase, r),
		},
		PagingMetadata: pagingMeta(q, res.Total, res.NextCursor),
	}
	for _, e := range res.Items {
		envelope.EntitySearchResults = append(envelope.EntitySearchResults, mapper.Entity(e, opts))
	}
	writeJSON(w, http.StatusOK, envelope)
}

// HandleNameserversSearch implements GET /nameservers?name|ip=*.
func (s *Server) HandleNameserversSearch(w http.ResponseWriter, r *http.Request) {
	if s.Search == nil {
		writeError(w, http.StatusNotImplemented, "Search not enabled", s.Notices)
		return
	}
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	q := buildQuery(r, nameserverSearchFields)
	if len(q.Terms) == 0 {
		writeError(w, http.StatusBadRequest, "Missing search predicate", s.Notices)
		return
	}
	res, err := s.Search.SearchNameservers(r.Context(), q)
	if err != nil {
		writeSearchErr(w, err, s.Notices)
		return
	}
	envelope := types.NameserverSearchResults{
		Common: types.Common{
			RDAPConformance: mapperConformance(s.opts(r)),
			ObjectClassName: "nameserverSearchResults",
			Notices:         s.Notices,
			Links:           searchSelfLink(s.SelfLinkBase, r),
		},
		PagingMetadata: pagingMeta(q, res.Total, res.NextCursor),
	}
	for _, n := range res.Items {
		envelope.NameserverSearchResults = append(envelope.NameserverSearchResults, mapper.Nameserver(n))
	}
	writeJSON(w, http.StatusOK, envelope)
}

func buildQuery(r *http.Request, allowed []string) search.Query {
	q := search.Query{Terms: map[string]string{}}
	vals := r.URL.Query()
	for _, field := range allowed {
		if v := vals.Get(field); v != "" {
			q.Terms[field] = v
		}
	}
	// Paging controls: standard RFC 8977 convention is `count` + opaque
	// `cursor`; we also accept `offset` for simple offset paging.
	if v := vals.Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Limit = n
		}
	}
	if v := vals.Get("cursor"); v != "" {
		if decoded, err := base64.RawURLEncoding.DecodeString(v); err == nil {
			if off, err := strconv.Atoi(string(decoded)); err == nil {
				q.Offset = off
			}
		}
	}
	if v := vals.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			q.Offset = n
		}
	}
	return q
}

// pagingMeta decides whether to emit paging metadata. Omitted when the
// result fits in a single page (start=0 AND total<=limit).
func pagingMeta(q search.Query, total int, nextCursor string) *types.PagingMetadata {
	limit := search.ClampLimit(q.Limit, 50, 500)
	if q.Offset == 0 && total <= limit {
		return nil
	}
	meta := &types.PagingMetadata{
		TotalCount: total,
		PageSize:   limit,
	}
	if limit > 0 {
		meta.PageNumber = q.Offset/limit + 1
	}
	next := q.Offset + limit
	if next < total {
		meta.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(next)))
	} else if nextCursor != "" {
		meta.NextCursor = nextCursor
	}
	return meta
}

func searchSelfLink(base string, r *http.Request) []types.Link {
	if base == "" {
		return nil
	}
	href := base + r.URL.RequestURI()
	return []types.Link{{Value: href, Rel: "self", Href: href, Type: "application/rdap+json"}}
}

// mapperConformance duplicates the logic in mapper.conformance so the
// handlers can emit the same conformance list on search envelopes
// without importing an internal helper. Cheap to regenerate.
func mapperConformance(opts mapper.Options) types.RDAPConformance {
	if len(opts.ExtraConformance) == 0 {
		return types.DefaultConformance
	}
	out := make(types.RDAPConformance, 0, len(types.DefaultConformance)+len(opts.ExtraConformance))
	out = append(out, types.DefaultConformance...)
	out = append(out, opts.ExtraConformance...)
	return out
}

func writeSearchErr(w http.ResponseWriter, err error, notices []types.Notice) {
	if errors.Is(err, search.ErrNotImplemented) {
		writeError(w, http.StatusNotImplemented, "Search field not supported", notices)
		return
	}
	writeError(w, http.StatusInternalServerError, "Search failed", notices)
}

// needed for auth.Claims type symbol even when tests don't exercise it
var _ = auth.AccessPrivileged
