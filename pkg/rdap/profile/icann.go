// Package profile ships canned Options presets for common RDAP
// deployment profiles: ICANN gTLD (RP/TIG v2.2), plain STD-95, and room
// to grow for RIR / ccTLD profiles. Operators compose a profile with
// their deployment-specific values (ToS URL, self-link base) and hand it
// to handlers.Server so every response carries the mandated notices and
// conformance identifiers.
package profile

import "github.com/bramheerink/gordap/pkg/rdap/types"

// ICANNgTLDConformance is the set of rdapConformance identifiers ICANN
// contracted parties MUST advertise per RP/TIG v2.2 §1.3, in addition to
// the STD-95 baseline already set by types.DefaultConformance.
func ICANNgTLDConformance() []string {
	return []string{
		"icann_rdap_response_profile_1",
		"icann_rdap_technical_implementation_guide_1",
	}
}

// ICANNgTLDNotices returns the three notices every top-level response
// MUST carry per RP2.2 §2.8 and §0.0:
//   - Terms of Service notice linked to the CP's own ToS URL.
//   - Status Codes notice linking to https://icann.org/epp.
//   - RDDS Inaccuracy Complaint Form notice linking to https://icann.org/wicf.
//
// The wording is a verbatim quote of the profile text so the ICANN
// Conformance Tool accepts the response.
func ICANNgTLDNotices(tosURL string) []types.Notice {
	return []types.Notice{
		{
			Title: "Terms of Service",
			Description: []string{
				"By submitting an RDAP query, you agree to the operator's Acceptable Use Policy.",
				"See the linked URL for the full text.",
			},
			Links: []types.Link{{
				Value: tosURL,
				Rel:   "terms-of-service",
				Href:  tosURL,
				Type:  "text/html",
			}},
		},
		{
			Title: "Status Codes",
			Description: []string{
				"For more information on domain status codes, please visit https://icann.org/epp",
			},
			Links: []types.Link{{
				Rel:  "glossary",
				Href: "https://icann.org/epp",
				Type: "text/html",
			}},
		},
		{
			Title: "RDDS Inaccuracy Complaint Form",
			Description: []string{
				"URL of the ICANN RDDS Inaccuracy Complaint Form: https://icann.org/wicf",
			},
			Links: []types.Link{{
				Href: "https://icann.org/wicf",
				Type: "text/html",
			}},
		},
	}
}
