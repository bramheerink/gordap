// Package jwks implements auth.Verifier against an OpenID Connect
// provider's JWKS endpoint. Stdlib-only: no third-party JWT/JWKS
// dependency pulled into the core. Supports RS256 and ES256 — the two
// algorithms that cover the overwhelming majority of real OIDC
// deployments (ICANN-RDAP-OIDC draft, Keycloak, Auth0, Google, Azure
// AD, AWS Cognito, dex).
//
// Scope-to-tier mapping is the operator's call: pass a ScopeMap so the
// verifier can translate the OAuth2 `scope` claim into AccessLevel.
package jwks

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
)

// Config controls a Verifier. All fields except IssuerURL are optional.
type Config struct {
	JWKSURL   string        // https://issuer/.well-known/jwks.json
	Issuer    string        // expected `iss` claim; empty to skip check
	Audience  string        // expected `aud` claim; empty to skip check
	RefreshEvery time.Duration // JWKS cache TTL; default 10 min
	HTTPClient *http.Client  // default http.Client with 10s timeout

	// ScopeMap maps individual scope strings to the tier they grant.
	// The highest tier the token claims wins. Unknown scopes are
	// ignored. If nil, any valid token gets AccessAuthenticated.
	ScopeMap map[string]auth.AccessLevel
}

// Verifier satisfies auth.Verifier.
type Verifier struct {
	cfg    Config
	client *http.Client

	mu         sync.RWMutex
	keys       map[string]crypto.PublicKey
	nextFetch  time.Time
}

// New constructs a Verifier. The JWKS is fetched lazily on the first
// Verify call; a missing JWKS endpoint means every token is rejected.
func New(cfg Config) *Verifier {
	if cfg.RefreshEvery <= 0 {
		cfg.RefreshEvery = 10 * time.Minute
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Verifier{cfg: cfg, client: client}
}

// Verify parses, signature-checks and claim-validates a JWT. On
// success it returns auth.Claims with the subject and the highest tier
// the token's scopes grant.
func (v *Verifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return auth.Claims{}, errors.New("jwks: malformed token")
	}
	headerRaw, err := base64url(parts[0])
	if err != nil {
		return auth.Claims{}, fmt.Errorf("jwks: decode header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerRaw, &hdr); err != nil {
		return auth.Claims{}, fmt.Errorf("jwks: parse header: %w", err)
	}
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return auth.Claims{}, fmt.Errorf("jwks: unsupported alg %q", hdr.Alg)
	}

	key, err := v.keyFor(ctx, hdr.Kid)
	if err != nil {
		return auth.Claims{}, err
	}

	signedInput := []byte(parts[0] + "." + parts[1])
	sig, err := base64url(parts[2])
	if err != nil {
		return auth.Claims{}, fmt.Errorf("jwks: decode sig: %w", err)
	}
	digest := sha256.Sum256(signedInput)

	switch hdr.Alg {
	case "RS256":
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return auth.Claims{}, errors.New("jwks: kid key is not RSA")
		}
		if err := rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest[:], sig); err != nil {
			return auth.Claims{}, fmt.Errorf("jwks: RS256 verify: %w", err)
		}
	case "ES256":
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return auth.Claims{}, errors.New("jwks: kid key is not EC")
		}
		if len(sig) != 64 {
			return auth.Claims{}, fmt.Errorf("jwks: ES256 sig must be 64 bytes, got %d", len(sig))
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(ecKey, digest[:], r, s) {
			return auth.Claims{}, errors.New("jwks: ES256 verification failed")
		}
	}

	payloadRaw, err := base64url(parts[1])
	if err != nil {
		return auth.Claims{}, fmt.Errorf("jwks: decode payload: %w", err)
	}
	var claims struct {
		Sub   string  `json:"sub"`
		Iss   string  `json:"iss"`
		Aud   any     `json:"aud"` // string or []string per RFC 7519
		Exp   int64   `json:"exp"`
		Nbf   int64   `json:"nbf"`
		Scope string  `json:"scope"`
	}
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return auth.Claims{}, fmt.Errorf("jwks: parse payload: %w", err)
	}

	now := time.Now().Unix()
	if claims.Exp > 0 && now >= claims.Exp {
		return auth.Claims{}, errors.New("jwks: token expired")
	}
	if claims.Nbf > 0 && now < claims.Nbf {
		return auth.Claims{}, errors.New("jwks: token not yet valid")
	}
	if v.cfg.Issuer != "" && claims.Iss != v.cfg.Issuer {
		return auth.Claims{}, fmt.Errorf("jwks: issuer mismatch: %q", claims.Iss)
	}
	if v.cfg.Audience != "" && !audContains(claims.Aud, v.cfg.Audience) {
		return auth.Claims{}, errors.New("jwks: audience mismatch")
	}

	scopes := strings.Fields(claims.Scope)
	return auth.Claims{
		Subject: claims.Sub,
		Scopes:  scopes,
		Level:   highestTier(scopes, v.cfg.ScopeMap),
	}, nil
}

func audContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok && str == want {
				return true
			}
		}
	}
	return false
}

func highestTier(scopes []string, m map[string]auth.AccessLevel) auth.AccessLevel {
	if len(m) == 0 {
		return auth.AccessAuthenticated
	}
	level := auth.AccessAuthenticated
	for _, s := range scopes {
		if t, ok := m[s]; ok && t > level {
			level = t
		}
	}
	return level
}

// keyFor returns the public key for the given kid, refreshing the JWKS
// cache when it is stale. Concurrent callers share the fetch via a
// double-checked lock pattern.
func (v *Verifier) keyFor(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.RLock()
	if time.Now().Before(v.nextFetch) {
		if k, ok := v.keys[kid]; ok {
			v.mu.RUnlock()
			return k, nil
		}
	}
	v.mu.RUnlock()

	v.mu.Lock()
	defer v.mu.Unlock()
	// Re-check after acquiring write lock — another goroutine may have
	// just refreshed while we were blocked.
	if time.Now().Before(v.nextFetch) {
		if k, ok := v.keys[kid]; ok {
			return k, nil
		}
	}
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	k, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: unknown kid %q", kid)
	}
	return k, nil
}

// fetchLocked re-populates the key cache. Caller MUST hold v.mu for
// write.
func (v *Verifier) fetchLocked(ctx context.Context) error {
	if v.cfg.JWKSURL == "" {
		return errors.New("jwks: no JWKSURL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: fetch status %d", resp.StatusCode)
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("jwks: decode: %w", err)
	}
	out := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		pk, err := k.publicKey()
		if err != nil {
			// Skip unusable keys instead of failing the whole refresh.
			// Providers sometimes include keys we don't understand.
			continue
		}
		out[k.Kid] = pk
	}
	v.keys = out
	v.nextFetch = time.Now().Add(v.cfg.RefreshEvery)
	return nil
}

// jwk models the RFC 7517 JSON Web Key members we consume.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	// RSA
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`
	// EC
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64url(k.N)
		if err != nil {
			return nil, err
		}
		e, err := base64url(k.E)
		if err != nil {
			return nil, err
		}
		// RFC 7518 §6.3.1.2: E is a big-endian octet sequence; common
		// values are 65537 (0x010001) but any size is legal.
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(n),
			E: int(new(big.Int).SetBytes(e).Int64()),
		}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("jwks: unsupported EC curve %q", k.Crv)
		}
		x, err := base64url(k.X)
		if err != nil {
			return nil, err
		}
		y, err := base64url(k.Y)
		if err != nil {
			return nil, err
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}, nil
	default:
		return nil, fmt.Errorf("jwks: unsupported kty %q", k.Kty)
	}
}

// base64url decodes an RFC 7515-style base64url string (unpadded).
func base64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
