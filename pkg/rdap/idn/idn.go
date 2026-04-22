// Package idn normalises domain names for RDAP lookups. Uses the UTS #46
// lookup profile — stricter than the registration profile because we're
// matching an existing name, not minting one, so quietly mangling input
// would hide real mismatches.
package idn

import (
	"strings"

	"golang.org/x/net/idna"
)

var profile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.StrictDomainName(true),
)

// Normalize returns the lower-cased ASCII (LDH) form of an input name.
// Inputs that are already LDH pass through; Unicode names are punycoded.
// Returns ("", false) when the input is not a valid domain.
//
// Rejects degenerate inputs found via FuzzNormalize:
//   - empty / whitespace-only strings
//   - strings consisting only of dots ("." / ".." / "...")
//   - single-label names without a dot — RDAP queries are always for
//     FQDNs, and UTS-46 strict mode is non-idempotent on bare labels
//     (it accepts them once after punycoding but rejects on a second
//     pass), which would silently break code that re-normalises.
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".") // strip every trailing dot, not just one
	if raw == "" || strings.Trim(raw, ".") == "" {
		return "", false
	}
	ascii, err := profile.ToASCII(raw)
	if err != nil {
		return "", false
	}
	if strings.Trim(ascii, ".") == "" {
		return "", false
	}
	if !strings.Contains(ascii, ".") {
		return "", false
	}
	return strings.ToLower(ascii), true
}
