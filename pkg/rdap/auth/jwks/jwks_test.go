package jwks

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
)

// makeJWKSServer spins up a tiny httptest.Server that publishes the
// given JWK set. Returns the URL, the signing key (kept private), and
// the kid of that key.
func makeJWKSServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "test-kid-1"
	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	return srv, priv, kid
}

// signToken manually constructs a compact-serialised JWT with RS256.
func signToken(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." +
		base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestJWKS_RoundTrip_RS256_Authenticated(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL})

	tok := signToken(t, priv, kid, map[string]any{
		"sub": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "alice" {
		t.Fatalf("sub: %q", claims.Subject)
	}
	if claims.Level != auth.AccessAuthenticated {
		t.Fatalf("level: %s (want authenticated)", claims.Level)
	}
}

func TestJWKS_ScopeMapElevatesTier(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{
		JWKSURL:  srv.URL,
		ScopeMap: map[string]auth.AccessLevel{"rdap:privileged": auth.AccessPrivileged},
	})
	tok := signToken(t, priv, kid, map[string]any{
		"sub":   "admin",
		"scope": "openid rdap:privileged",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Level != auth.AccessPrivileged {
		t.Fatalf("privileged scope must elevate tier, got %s", claims.Level)
	}
}

func TestJWKS_ExpiredRejected(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL})
	tok := signToken(t, priv, kid, map[string]any{
		"sub": "x",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("expired token must not verify")
	}
}

func TestJWKS_NotYetValidRejected(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL})
	tok := signToken(t, priv, kid, map[string]any{
		"sub": "x",
		"nbf": time.Now().Add(time.Hour).Unix(),
		"exp": time.Now().Add(2 * time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("nbf in future must reject")
	}
}

func TestJWKS_IssuerAudienceChecks(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{
		JWKSURL:  srv.URL,
		Issuer:   "https://idp.example.com",
		Audience: "rdap",
	})
	good := signToken(t, priv, kid, map[string]any{
		"sub": "x", "iss": "https://idp.example.com", "aud": "rdap",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), good); err != nil {
		t.Fatalf("good token rejected: %v", err)
	}

	wrongIss := signToken(t, priv, kid, map[string]any{
		"sub": "x", "iss": "https://attacker.example", "aud": "rdap",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), wrongIss); err == nil {
		t.Fatal("wrong iss must reject")
	}

	wrongAud := signToken(t, priv, kid, map[string]any{
		"sub": "x", "iss": "https://idp.example.com", "aud": "wrong",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), wrongAud); err == nil {
		t.Fatal("wrong aud must reject")
	}
}

func TestJWKS_AudArrayAccepted(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL, Audience: "rdap"})
	tok := signToken(t, priv, kid, map[string]any{
		"sub": "x", "aud": []string{"other", "rdap"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("aud array rejected: %v", err)
	}
}

func TestJWKS_UnknownKidRejected(t *testing.T) {
	srv, priv, _ := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL})
	tok := signToken(t, priv, "bogus-kid", map[string]any{
		"sub": "x", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("unknown kid must reject")
	}
}

func TestJWKS_MalformedToken(t *testing.T) {
	v := New(Config{JWKSURL: "http://unused"})
	if _, err := v.Verify(context.Background(), "not a jwt"); err == nil {
		t.Fatal("malformed token must reject")
	}
	if _, err := v.Verify(context.Background(), "only.two"); err == nil {
		t.Fatal("2-part token must reject")
	}
}

func TestJWKS_UnsupportedAlgRejected(t *testing.T) {
	// Craft a token header with alg=HS256 and verify rejection.
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","kid":"x"}`))
	pl := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"y"}`))
	tok := hdr + "." + pl + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))

	v := New(Config{JWKSURL: "http://unused"})
	_, err := v.Verify(context.Background(), tok)
	if err == nil || !strings.Contains(err.Error(), "unsupported alg") {
		t.Fatalf("expected unsupported-alg error, got %v", err)
	}
}

func TestJWKS_SignatureTampered(t *testing.T) {
	srv, priv, kid := makeJWKSServer(t)
	defer srv.Close()
	v := New(Config{JWKSURL: srv.URL})
	tok := signToken(t, priv, kid, map[string]any{
		"sub": "x", "exp": time.Now().Add(time.Hour).Unix(),
	})
	// Replace the signature wholesale — a constant string of zeros is
	// decodable but cannot be a valid RSA-PKCS1v15 signature.
	parts := strings.Split(tok, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
	tampered := strings.Join(parts, ".")
	if _, err := v.Verify(context.Background(), tampered); err == nil {
		t.Fatal("tampered signature must reject")
	}
}
