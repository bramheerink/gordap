package idn

import "testing"

// FuzzNormalize hammers the UTS #46 wrapper with arbitrary bytes.
//
// Invariants we actually want to hold (full idempotence is too
// strict — UTS-46 strict mode is allowed to reject second passes
// when the input goes through punycoding into a label form that
// strict mode rules out):
//
//   - never panics
//   - never returns ok=true with an empty string
//   - never returns ok=true with a string that has no dot (that
//     would mean we accepted a bare-label "domain", which RDAP
//     never queries)
//   - returned LDH form contains only valid LDH bytes plus dots
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		"example.nl", "EXAMPLE.NL", "bücher.example",
		"", ".", "..", "-bad.example", "exa mple.nl",
		"παράδειγμα.ελ", "中国", "\x00null.example",
		"a..b", "a.b.", ".a.b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := Normalize(in)
		if !ok {
			return
		}
		if out == "" {
			t.Fatalf("Normalize(%q) returned ok=true with empty result", in)
		}
		if !contains(out, '.') {
			t.Fatalf("Normalize(%q) accepted bare label %q (no FQDN dot)", in, out)
		}
		for i := 0; i < len(out); i++ {
			b := out[i]
			ok := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') ||
				b == '-' || b == '.'
			if !ok {
				t.Fatalf("Normalize(%q)=%q has non-LDH byte %#02x at %d", in, out, b, i)
			}
		}
	})
}

func contains(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}
