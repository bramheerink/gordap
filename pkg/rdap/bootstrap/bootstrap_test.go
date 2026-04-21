package bootstrap

import (
	"net/netip"
	"testing"
)

func fixture() *Registry {
	r := &Registry{
		tldServers: map[string][]string{
			"nl":         {"https://rdap.sidn.nl/"},
			"co.uk":      {"https://rdap.nominet.uk/"},
			"example.nl": {"https://rdap.internal/example/"},
		},
		ipv4Prefixes: []ipEntry{
			mustEntry("192.0.2.0/24", "https://rdap.ripe.example/"),
			mustEntry("0.0.0.0/0", "https://rdap.default.example/"),
		},
		ipv6Prefixes: []ipEntry{
			mustEntry("2001:db8::/32", "https://rdap.ripe6.example/"),
		},
	}
	return r
}

func mustEntry(cidr, url string) ipEntry {
	p := netip.MustParsePrefix(cidr)
	return ipEntry{prefix: p, urls: []string{url}}
}

func TestDomainServers_LongestMatchPrefersSpecific(t *testing.T) {
	r := fixture()
	got := r.DomainServers("sub.example.nl")
	if len(got) != 1 || got[0] != "https://rdap.internal/example/" {
		t.Fatalf("expected example.nl match, got %+v", got)
	}
}

func TestDomainServers_FallsBackToTLD(t *testing.T) {
	r := fixture()
	got := r.DomainServers("foo.other.nl")
	if len(got) != 1 || got[0] != "https://rdap.sidn.nl/" {
		t.Fatalf("expected nl fallback, got %+v", got)
	}
}

func TestDomainServers_MultiLabelZone(t *testing.T) {
	r := fixture()
	got := r.DomainServers("british.co.uk")
	if len(got) != 1 || got[0] != "https://rdap.nominet.uk/" {
		t.Fatalf("expected co.uk match, got %+v", got)
	}
}

func TestDomainServers_Unknown(t *testing.T) {
	r := fixture()
	if got := r.DomainServers("example.xyz"); got != nil {
		t.Fatalf("expected nil for unknown zone, got %+v", got)
	}
}

func TestIPServers_V4Match(t *testing.T) {
	r := fixture()
	got := r.IPServers(netip.MustParseAddr("192.0.2.10"))
	if len(got) != 1 || got[0] != "https://rdap.ripe.example/" {
		t.Fatalf("expected specific v4 match, got %+v", got)
	}
}

func TestIPServers_V6Match(t *testing.T) {
	r := fixture()
	got := r.IPServers(netip.MustParseAddr("2001:db8::1"))
	if len(got) != 1 || got[0] != "https://rdap.ripe6.example/" {
		t.Fatalf("expected v6 match, got %+v", got)
	}
}

func TestIPServers_V6NoMatch(t *testing.T) {
	r := fixture()
	if got := r.IPServers(netip.MustParseAddr("2a00::1")); got != nil {
		t.Fatalf("expected nil for uncovered v6 address, got %+v", got)
	}
}

func TestParsePrefixes_IgnoresMalformed(t *testing.T) {
	out := parsePrefixes([][][]string{
		{{"10.0.0.0/8", "nope"}, {"https://ok"}},
		{{"not-a-prefix"}, {"https://skip"}},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 valid prefix, got %+v", out)
	}
}
