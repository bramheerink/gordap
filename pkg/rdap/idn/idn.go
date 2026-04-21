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
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".")
	if raw == "" {
		return "", false
	}
	ascii, err := profile.ToASCII(raw)
	if err != nil {
		return "", false
	}
	return strings.ToLower(ascii), true
}
