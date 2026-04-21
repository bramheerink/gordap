package handlers

import (
	"net/http"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

// NewRouter wires every RDAP endpoint on a stdlib ServeMux. The Go 1.22+
// pattern syntax keeps routing declarative without a third-party router.
func NewRouter(s *Server, verifier auth.Verifier) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /domain/{name}", s.HandleDomain)
	mux.HandleFunc("GET /entity/{handle}", s.HandleEntity)
	mux.HandleFunc("GET /nameserver/{name}", s.HandleNameserver)
	mux.HandleFunc("GET /ip/{ip}", s.HandleIP)
	mux.HandleFunc("GET /help", s.HandleHelp)

	// RFC 7482 §3.2 / RFC 9536 search endpoints. Go 1.22+ mux treats
	// these as distinct patterns from /domain/{name} because the
	// trailing path segment doesn't exist.
	mux.HandleFunc("GET /domains", s.HandleDomainsSearch)
	mux.HandleFunc("GET /entities", s.HandleEntitiesSearch)
	mux.HandleFunc("GET /nameservers", s.HandleNameserversSearch)

	// draft-ietf-regext-rdap-rir-search hierarchy navigation. Routed
	// through the standard search machinery for rdap-bottom (exact
	// containing network); the rest are declared so the 501 response
	// is scoped and carries the RDAP error envelope instead of a bare
	// 404. ARIN-class deployments can override the handlers in a
	// custom router.
	mux.HandleFunc("GET /ips/rirSearch1/rdap-bottom/{ip...}", s.HandleRIRBottom)
	mux.HandleFunc("GET /ips/rirSearch1/rdap-top/{ip...}", s.HandleRIRNotImpl)
	mux.HandleFunc("GET /ips/rirSearch1/rdap-up/{ip...}", s.HandleRIRNotImpl)
	mux.HandleFunc("GET /ips/rirSearch1/rdap-down/{ip...}", s.HandleRIRNotImpl)

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Unknown RDAP path", s.Notices)
	})

	// Response cache goes inside auth (so Claims are on the context
	// when we compute the tier-specific key) and around the mux (so
	// it observes the final rendered body).
	return auth.Middleware(verifier)(responseCacheMiddleware(s.ResponseCache)(mux))
}

// helpResponse implements /help per RFC 9082 §3.1.6 plus the
// versioning_help member from draft-ietf-regext-rdap-versioning.
// Clients that want to negotiate an extension set use the `versioning`
// query parameter on subsequent requests; this endpoint tells them
// which versions the server speaks.
func helpResponse(s *Server) any {
	notices := s.Notices
	if len(notices) == 0 {
		notices = []types.Notice{
			{Title: "Terms of Service",
				Description: []string{"Access is subject to the server's AUP."}},
			{Title: "Tiered Access",
				Description: []string{
					"Anonymous queries receive a redacted view per GDPR.",
					"Authenticated callers obtain additional contact channels.",
				}},
		}
	}
	conf := types.DefaultConformance
	if len(s.ExtraConformance) > 0 {
		conf = append(types.RDAPConformance{}, types.DefaultConformance...)
		conf = append(conf, s.ExtraConformance...)
	}
	return struct {
		types.Common
		VersioningHelp []versioningEntry `json:"versioning_help,omitempty"`
	}{
		Common: types.Common{
			RDAPConformance: conf,
			ObjectClassName: "help",
			Notices:         notices,
		},
		VersioningHelp: buildVersioningHelp(conf),
	}
}

// versioningEntry is the per-extension record published in the
// versioning_help array. Mirrors the shape described in
// draft-ietf-regext-rdap-versioning §4.2.
type versioningEntry struct {
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Extensions  []string `json:"extensions,omitempty"`
}

// buildVersioningHelp describes every extension identifier we advertise
// in rdapConformance. Clients that accept
// `application/rdap-x+json; extensions="..."` or the `versioning` query
// parameter can use this to negotiate a pinned set; for now we serve
// one version and advertise it descriptively.
func buildVersioningHelp(conf types.RDAPConformance) []versioningEntry {
	descriptions := map[string]string{
		"rdap_level_0":                                "STD 95 / RFC 9083 baseline",
		"jscontact_level_0":                           "JSContact contact information (draft-ietf-regext-rdap-jscontact)",
		"redacted":                                    "RFC 9537 redaction signalling",
		"icann_rdap_response_profile_1":               "ICANN gTLD RDAP Response Profile v2.2",
		"icann_rdap_technical_implementation_guide_1": "ICANN gTLD RDAP Technical Implementation Guide v2.2",
	}
	out := []versioningEntry{{
		Version:     "1",
		Description: "Default version",
		Extensions:  conf,
	}}
	for _, ext := range conf {
		if d, ok := descriptions[ext]; ok {
			out = append(out, versioningEntry{Version: ext, Description: d, Extensions: []string{ext}})
		}
	}
	return out
}
