// Package handlers exposes the RDAP HTTP endpoints. A *Server binds an
// auth-aware DataSource, an optional BootstrapRegistry, and deployment
// profile fields into http.Handler factories that can be mounted on any
// ServeMux.
package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/cache"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/idn"
	"github.com/bramheerink/gordap/pkg/rdap/mapper"
	"github.com/bramheerink/gordap/pkg/rdap/search"
	"github.com/bramheerink/gordap/pkg/rdap/types"
	"github.com/bramheerink/gordap/pkg/rdap/validate"
)

// BootstrapRegistry is the subset of bootstrap.Registry the handler
// needs. Keeping it as a local interface avoids a hard import and lets
// tests swap in a fake.
type BootstrapRegistry interface {
	DomainServers(name string) []string
	IPServers(ip netip.Addr) []string
}

type Server struct {
	DS        datasource.DataSource
	Logger    *slog.Logger
	Bootstrap BootstrapRegistry // optional

	// Search backs RFC 7482 §3.2 / RFC 9536 reverse-search endpoints.
	// When nil the search handlers respond 501.
	Search search.Index

	// SelfLinkBase, when non-empty, is the URL prefix used to emit
	// RFC 9083 §4.2 `rel:"self"` links on every object. Typically the
	// server's public canonical URL (e.g. "https://rdap.example.com").
	SelfLinkBase string

	// ExtraConformance is appended to types.DefaultConformance on every
	// top-level response. Use pkg/rdap/profile for canned presets.
	ExtraConformance []string

	// Notices is injected on every top-level response. Use
	// pkg/rdap/profile.ICANNgTLDNotices for the ICANN-mandated set.
	Notices []types.Notice

	// EmitJCard forces jCard (vcardArray) emission alongside the
	// JSContact jscard. Required by ICANN-contracted gTLD operators
	// until the conformance tool fully accepts JSContact alone.
	EmitJCard bool

	// RedactionReason is attached to every RFC 9537 marker. A typical
	// value is "Data minimization per GDPR Art. 5(1)(c)".
	RedactionReason string

	// ResponseCache, when non-nil, is a post-render response cache
	// keyed by (object, id, access-tier). Unlike the record-level
	// cache it holds already-redacted JSON, so no PII sits in the
	// working set regardless of caller tier.
	ResponseCache *cache.ResponseCache
}

func (s *Server) opts(r *http.Request) mapper.Options {
	return mapper.Options{
		Level:            auth.FromContext(r.Context()).Level,
		SelfLinkBase:     s.SelfLinkBase,
		ExtraConformance: s.ExtraConformance,
		ExtraNotices:     s.Notices,
		EmitJCard:        s.EmitJCard,
		JCardOnly:        wantsJCardOnly(r),
		RedactionReason:  s.RedactionReason,
	}
}

// wantsJCardOnly reports whether the caller has explicitly negotiated
// jCard-exclusive output. Two signals honored:
//
//   - ?jscard=false query parameter — simple, works from any client.
//   - Accept: application/rdap+json; profile=jcard — media-type-param
//     convention common in conformance tools.
//
// Either makes the response omit the JSContact `jscard` member and
// emit only the legacy `vcardArray`. Absence leaves the server-default
// behaviour untouched.
func wantsJCardOnly(r *http.Request) bool {
	if v := r.URL.Query().Get("jscard"); v == "false" || v == "0" || v == "no" {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		for _, kv := range strings.Split(part, ";") {
			if strings.EqualFold(strings.TrimSpace(kv), "profile=jcard") {
				return true
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// accepts mirrors RFC 7480 §4: absent / */* Accept headers are
// acceptable, explicit text/html and friends are not.
func accepts(r *http.Request) bool {
	h := r.Header.Get("Accept")
	if h == "" {
		return true
	}
	for _, part := range strings.Split(h, ",") {
		mt := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		switch mt {
		case "", "*/*", "application/*", contentType, "application/json":
			return true
		}
	}
	return false
}

// redirectTo emits a 302 to the concatenation of base + objectPath. Per
// RFC 7484 §4.2 the body is optional; we keep it empty.
func redirectTo(w http.ResponseWriter, base, objectPath string) {
	base = strings.TrimRight(base, "/")
	w.Header().Set("Location", base+objectPath)
	w.WriteHeader(http.StatusFound)
}

func (s *Server) HandleDomain(w http.ResponseWriter, r *http.Request) {
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	raw := r.PathValue("name")
	if err := validate.PathSegment(raw); err != nil {
		writeError(w, http.StatusBadRequest, "Malformed domain name", s.Notices)
		return
	}
	name, ok := idn.Normalize(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "Malformed domain name", s.Notices)
		return
	}
	if err := validate.DomainLength(name); err != nil {
		writeError(w, http.StatusBadRequest, "Domain name too long", s.Notices)
		return
	}

	d, err := s.DS.GetDomain(r.Context(), name)
	if err != nil {
		if errors.Is(err, datasource.ErrNotFound) && s.Bootstrap != nil {
			if srv := s.Bootstrap.DomainServers(name); len(srv) > 0 {
				redirectTo(w, srv[0], "/domain/"+name)
				return
			}
		}
		status, title := statusFor(err)
		if status == http.StatusInternalServerError {
			s.Logger.ErrorContext(r.Context(), "get domain failed",
				slog.String("name", name), slog.Any("err", err))
		}
		writeError(w, status, title, s.Notices)
		return
	}
	writeJSON(w, http.StatusOK, mapper.Domain(d, s.opts(r)))
}

func (s *Server) HandleEntity(w http.ResponseWriter, r *http.Request) {
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	handle := r.PathValue("handle")
	if err := validate.Handle(handle); err != nil {
		writeError(w, http.StatusBadRequest, "Malformed entity handle", s.Notices)
		return
	}
	c, err := s.DS.GetEntity(r.Context(), handle)
	if err != nil {
		status, title := statusFor(err)
		writeError(w, status, title, s.Notices)
		return
	}
	writeJSON(w, http.StatusOK, mapper.EntityTopLevel(c, s.opts(r)))
}

func (s *Server) HandleNameserver(w http.ResponseWriter, r *http.Request) {
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	raw := r.PathValue("name")
	if err := validate.PathSegment(raw); err != nil {
		writeError(w, http.StatusBadRequest, "Malformed nameserver name", s.Notices)
		return
	}
	name, ok := idn.Normalize(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "Malformed nameserver name", s.Notices)
		return
	}
	if err := validate.DomainLength(name); err != nil {
		writeError(w, http.StatusBadRequest, "Nameserver name too long", s.Notices)
		return
	}
	n, err := s.DS.GetNameserver(r.Context(), name)
	if err != nil {
		status, title := statusFor(err)
		writeError(w, status, title, s.Notices)
		return
	}
	writeJSON(w, http.StatusOK, mapper.NameserverTopLevel(n, s.opts(r)))
}

func (s *Server) HandleIP(w http.ResponseWriter, r *http.Request) {
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	ip, err := netip.ParseAddr(r.PathValue("ip"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Malformed IP address", s.Notices)
		return
	}
	n, err := s.DS.GetIPNetwork(r.Context(), ip)
	if err != nil {
		if errors.Is(err, datasource.ErrNotFound) && s.Bootstrap != nil {
			if srv := s.Bootstrap.IPServers(ip); len(srv) > 0 {
				redirectTo(w, srv[0], "/ip/"+ip.String())
				return
			}
		}
		status, title := statusFor(err)
		writeError(w, status, title, s.Notices)
		return
	}
	writeJSON(w, http.StatusOK, mapper.IPNetwork(n, s.opts(r)))
}

func (s *Server) HandleHelp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, helpResponse(s))
}
