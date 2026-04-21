// Package validate collects the input-hardening checks the handler
// applies before touching the DataSource. Kept separate from idn so the
// length/charset discipline is auditable in one spot.
//
// Deliberate non-goals:
//   - We do NOT validate that a TLD exists. That is the authoritative
//     database's job; an unknown TLD yields a clean 404 after lookup,
//     which is more informative than a blanket 400.
//   - We do NOT enforce per-TLD registration policies (minimum label
//     length, allowed scripts). Those rules live with the registry and
//     vary per zone; enforcing them here would be wrong for someone
//     serving RDAP for a TLD with different rules.
//
// What we DO enforce:
//   - RFC 1035 §3.1 upper bound: 253 octets for the LDH form.
//   - Handle length + character set: EPP ROID shape ([0-9A-Za-z_-]{1,64}).
//   - IP validation via netip.ParseAddr (performed at the call site).
package validate

import (
	"errors"
	"strings"
)

// ErrTooLong is returned for inputs past the protocol-level maximum.
var ErrTooLong = errors.New("validate: input exceeds maximum length")

// ErrCharSet is returned when a handle contains disallowed characters.
var ErrCharSet = errors.New("validate: input contains disallowed characters")

// MaxDomainLength is the RFC 1035 §3.1 limit on the LDH form of a
// domain name (exclusive of a trailing dot), in octets.
const MaxDomainLength = 253

// MaxHandleLength caps entity handles. EPP ROIDs are typically under
// 30 characters; 64 gives headroom for registry-specific formats while
// keeping the URL path short enough for request loggers.
const MaxHandleLength = 64

// MaxQueryLength is the absolute upper bound on any URL path segment
// the RDAP router accepts. Guards against absurd inputs that would
// otherwise reach storage providers and potentially trigger large
// error messages.
const MaxQueryLength = 512

// DomainLength returns the octet-length of an LDH domain name.
// Performs the RFC 1035 check; leaves IDN validity to pkg/rdap/idn.
func DomainLength(ldh string) error {
	if len(ldh) == 0 {
		return ErrCharSet
	}
	if len(ldh) > MaxDomainLength {
		return ErrTooLong
	}
	return nil
}

// Handle checks that an entity handle fits the shape EPP ROIDs use in
// practice. Permissive enough for existing deployments (we've seen
// underscores, hyphens and mixed case), strict enough to reject ambient
// attack payloads (no spaces, control chars, quotes, path separators).
func Handle(h string) error {
	if h == "" {
		return ErrCharSet
	}
	if len(h) > MaxHandleLength {
		return ErrTooLong
	}
	for i := 0; i < len(h); i++ {
		b := h[i]
		switch {
		case b >= '0' && b <= '9':
		case b >= 'A' && b <= 'Z':
		case b >= 'a' && b <= 'z':
		case b == '-' || b == '_' || b == '.':
		default:
			return ErrCharSet
		}
	}
	// Reject leading/trailing dots — not seen in real ROIDs, often a
	// probe for path tricks.
	if strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return ErrCharSet
	}
	return nil
}

// PathSegment is a last-line-of-defence length check applied before
// domain/handle-specific validation. Any path segment longer than
// MaxQueryLength is rejected outright.
func PathSegment(s string) error {
	if len(s) > MaxQueryLength {
		return ErrTooLong
	}
	return nil
}
