package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_EmptyPath_ReturnsZero(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.Addr != "" {
		t.Fatalf("expected zero config, got %+v", c)
	}
}

func TestLoad_YAMLOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gordap.yaml")
	body := `
addr: ":9090"
database_url: postgres://localhost/rdap
self_link_base: https://rdap.example.nl
cache_ttl: 30s
rate_limit_rps: 200
profile:
  icann_gtld: true
  tos_url: https://example.nl/rdap-tos
  extra_notices:
    - title: Local disclaimer
      description:
        - "Access restricted to Dutch citizens during testing"
auth:
  jwks_url: https://idp.example/.well-known/jwks.json
  issuer: https://idp.example
  audience: rdap
  scope_map:
    rdap:privileged: privileged
    rdap:authenticated: authenticated
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9090" {
		t.Fatalf("addr: %q", c.Addr)
	}
	if c.CacheTTL != 30*time.Second {
		t.Fatalf("cacheTTL: %v", c.CacheTTL)
	}
	if c.RateLimitRPS == nil || *c.RateLimitRPS != 200 {
		t.Fatalf("rateLimitRPS: %v", c.RateLimitRPS)
	}
	if !c.Profile.ICANNgTLD || c.Profile.ToSURL == "" {
		t.Fatalf("profile: %+v", c.Profile)
	}
	if len(c.Profile.ExtraNotices) != 1 {
		t.Fatalf("extra notices: %+v", c.Profile.ExtraNotices)
	}
	if c.Auth.JWKSURL == "" || c.Auth.ScopeMap["rdap:privileged"] != "privileged" {
		t.Fatalf("auth: %+v", c.Auth)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load("/no/such/path.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(path, []byte("not: yaml: valid: ["), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
