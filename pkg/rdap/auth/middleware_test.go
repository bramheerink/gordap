package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeVerifier struct {
	wantToken string
	claims    Claims
	err       error
}

func (f fakeVerifier) Verify(_ context.Context, tok string) (Claims, error) {
	if f.err != nil {
		return Claims{}, f.err
	}
	if f.wantToken != "" && tok != f.wantToken {
		return Claims{}, errors.New("bad token")
	}
	return f.claims, nil
}

func run(t *testing.T, v Verifier, req *http.Request) Claims {
	t.Helper()
	var seen Claims
	h := Middleware(v)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestMiddleware_NoHeader_DegradesToAnonymous(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/domain/example.nl", nil)
	got := run(t, fakeVerifier{}, req)
	if got.Level != AccessAnonymous {
		t.Fatalf("expected anonymous, got %s", got.Level)
	}
}

func TestMiddleware_BearerValid_StoresClaims(t *testing.T) {
	v := fakeVerifier{
		wantToken: "abc",
		claims:    Claims{Subject: "user-1", Level: AccessPrivileged, Scopes: []string{"rdap:read:all"}},
	}
	req := httptest.NewRequest(http.MethodGet, "/domain/example.nl", nil)
	req.Header.Set("Authorization", "Bearer abc")
	got := run(t, v, req)
	if got.Level != AccessPrivileged || got.Subject != "user-1" {
		t.Fatalf("unexpected claims: %+v", got)
	}
}

func TestMiddleware_VerifierError_DegradesToAnonymous(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/domain/example.nl", nil)
	req.Header.Set("Authorization", "Bearer bad")
	got := run(t, fakeVerifier{err: errors.New("boom")}, req)
	if got.Level != AccessAnonymous {
		t.Fatalf("expected anonymous fallback, got %s", got.Level)
	}
}

func TestMiddleware_NonBearerHeader_Ignored(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/domain/example.nl", nil)
	req.Header.Set("Authorization", "Basic YWxhZGRpbjpvcGVuc2VzYW1l")
	got := run(t, fakeVerifier{claims: Claims{Level: AccessPrivileged}}, req)
	if got.Level != AccessAnonymous {
		t.Fatalf("Basic auth must not be passed to bearer verifier, got %s", got.Level)
	}
}
