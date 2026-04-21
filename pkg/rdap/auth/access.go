// Package auth models the tiered-access story the GDPR layer relies on.
// The rest of the system only cares about AccessLevel; how it was derived
// (JWT, OIDC, mutual-TLS, IP allow-list) is an implementation detail of the
// Verifier passed to Middleware.
package auth

import "context"

// AccessLevel is the coarse bucket that drives the redaction layer. Fine-
// grained scopes can live on the Claims struct and be consulted by the
// mapper; the default behaviour only needs this enum.
type AccessLevel int

const (
	AccessAnonymous AccessLevel = iota
	AccessAuthenticated
	AccessPrivileged // registrars, operators, LEAs
)

func (a AccessLevel) String() string {
	switch a {
	case AccessAuthenticated:
		return "authenticated"
	case AccessPrivileged:
		return "privileged"
	default:
		return "anonymous"
	}
}

// Claims is the decoded principal. Stays minimal on purpose — extensions
// can wrap it rather than growing the core.
type Claims struct {
	Subject string
	Level   AccessLevel
	Scopes  []string
}

type ctxKey struct{}

func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext always returns a Claims value. Callers never have to nil-check;
// an anonymous caller is just the zero value.
func FromContext(ctx context.Context) Claims {
	if c, ok := ctx.Value(ctxKey{}).(Claims); ok {
		return c
	}
	return Claims{Level: AccessAnonymous}
}
