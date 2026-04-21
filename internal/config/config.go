// Package config loads optional YAML overlays for the reference binary.
// Flag values always win; the YAML file fills in gaps. Kept internal
// because operators building with the pkg/rdap/* toolkit wire their own
// config story.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config mirrors every knob the binary exposes. YAML keys are
// lower_snake; zero values in YAML mean "don't override".
type Config struct {
	Addr            string        `yaml:"addr"`
	DatabaseURL     string        `yaml:"database_url"`
	Demo            *bool         `yaml:"demo"`
	SelfLinkBase    string        `yaml:"self_link_base"`
	ReadHeaderTimo  time.Duration `yaml:"read_header_timeout"`
	ShutdownTimo    time.Duration `yaml:"shutdown_timeout"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	MaxBodyBytes    *int64        `yaml:"max_body_bytes"`
	RateLimitRPS    *float64      `yaml:"rate_limit_rps"`
	RateLimitBurst  *float64      `yaml:"rate_limit_burst"`
	GzipMinSize     *int          `yaml:"gzip_min_size"`
	CacheSize       *int          `yaml:"cache_size"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
	CacheNegTTL     time.Duration `yaml:"cache_neg_ttl"`
	TLSCert         string        `yaml:"tls_cert"`
	TLSKey          string        `yaml:"tls_key"`
	EnableBootstrap *bool         `yaml:"bootstrap"`
	BootstrapEvery  time.Duration `yaml:"bootstrap_refresh"`

	// Profile encodes the RDAP deployment profile — the notices, ToS
	// link, and conformance identifiers every top-level response must
	// carry. Leave empty to serve plain STD 95 without ICANN extras.
	Profile ProfileConfig `yaml:"profile"`

	// Auth configures the bearer-token Verifier. When JWKSURL is set,
	// the binary wires an OIDC verifier; otherwise tokens are ignored
	// (everyone is Anonymous).
	Auth AuthConfig `yaml:"auth"`
}

// ProfileConfig is the multi-tenant-friendly shape operators pick
// between at boot time. Host-based multi-tenancy (one binary, N TLDs)
// would compose this struct per-host — the core binary ships one.
type ProfileConfig struct {
	// ICANNgTLD enables the ICANN RP2.2 / TIG v2.2 conformance
	// identifiers and mandatory notices. Implies ToSURL is set.
	ICANNgTLD bool   `yaml:"icann_gtld"`
	ToSURL    string `yaml:"tos_url"`

	// ExtraNotices appends arbitrary notices to the server-emitted
	// set. Useful for ccTLD-specific disclaimers.
	ExtraNotices []NoticeConfig `yaml:"extra_notices"`
}

type NoticeConfig struct {
	Title       string   `yaml:"title"`
	Description []string `yaml:"description"`
}

type AuthConfig struct {
	JWKSURL  string            `yaml:"jwks_url"`
	Issuer   string            `yaml:"issuer"`
	Audience string            `yaml:"audience"`
	ScopeMap map[string]string `yaml:"scope_map"` // scope → tier ("authenticated"|"privileged")
}

// Load reads a YAML file, or returns a zero Config when path is empty.
// Caller-supplied flag values should overlay this after loading.
func Load(path string) (*Config, error) {
	if path == "" {
		return &Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &c, nil
}
