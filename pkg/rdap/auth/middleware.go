package auth

import (
	"context"
	"net/http"
	"strings"
)

// Verifier validates a bearer token and returns the decoded Claims. The
// interface keeps the middleware free of JWT/OIDC library choices — swap
// in github.com/coreos/go-oidc, a JWKS verifier, or a mock in tests.
type Verifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

// nopVerifier accepts no tokens. Use when the server is deployed read-only.
type nopVerifier struct{}

func (nopVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return Claims{Level: AccessAnonymous}, nil
}

func NopVerifier() Verifier { return nopVerifier{} }

// Middleware extracts a bearer token, verifies it, and stores the resulting
// Claims on the request context. Missing / invalid tokens degrade to
// AccessAnonymous rather than returning 401 — RDAP explicitly supports
// unauthenticated queries, redaction handles the rest.
func Middleware(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := Claims{Level: AccessAnonymous}
			if h := r.Header.Get("Authorization"); h != "" {
				if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
					if c, err := v.Verify(r.Context(), tok); err == nil {
						claims = c
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}
