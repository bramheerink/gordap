package handlers

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/mapper"
)

// HandleRIRBottom implements the rdap-bottom navigation verb from
// draft-ietf-regext-rdap-rir-search — return the most-specific network
// containing the queried prefix. That's exactly what the regular IP
// lookup already does (longest-prefix match in the datasource), so we
// route through GetIPNetwork. This keeps the single-record RIR-search
// experience functional without a full hierarchy rewrite.
func (s *Server) HandleRIRBottom(w http.ResponseWriter, r *http.Request) {
	if !accepts(r) {
		writeError(w, http.StatusNotAcceptable, "Unsupported media type", s.Notices)
		return
	}
	// Path pattern uses `{ip...}` so CIDR syntax (a/24) remains intact.
	raw := r.PathValue("ip")
	// Accept either plain address or CIDR; we probe the address portion.
	addr, err := parseIPForSearch(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Malformed IP address", s.Notices)
		return
	}
	n, err := s.DS.GetIPNetwork(r.Context(), addr)
	if err != nil {
		if errors.Is(err, datasource.ErrNotFound) {
			writeError(w, http.StatusNotFound, "No containing network", s.Notices)
			return
		}
		writeError(w, http.StatusInternalServerError, "Lookup failed", s.Notices)
		return
	}
	writeJSON(w, http.StatusOK, mapper.IPNetwork(n, s.opts(r)))
}

// HandleRIRNotImpl is the scoped 501 for the hierarchy-walk verbs
// (rdap-top, rdap-up, rdap-down). A real implementation needs tree
// navigation the core DataSource doesn't expose yet; operators who
// need it mount their own handler.
func (s *Server) HandleRIRNotImpl(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented,
		"RIR-search hierarchy navigation not implemented",
		s.Notices,
		"This server implements draft-ietf-regext-rdap-rir-search rdap-bottom only.",
		"rdap-top / rdap-up / rdap-down require a SearchIndex with hierarchy support.")
}

func parseIPForSearch(s string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), nil
	}
	return netip.Addr{}, errors.New("not a valid IP or prefix")
}
