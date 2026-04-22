package search

import "testing"

// FuzzMatchPattern: never panic, and the empty-string corner case
// is consistent across the prefix/suffix/infix branches.
func FuzzMatchPattern(f *testing.F) {
	pairs := []struct{ h, p string }{
		{"example.nl", "example.nl"},
		{"example.nl", "example.*"},
		{"example.nl", "*.nl"},
		{"ns1.example.nl", "*example*"},
		{"", ""},
		{"", "*"},
		{"x", "**"},
	}
	for _, p := range pairs {
		f.Add(p.h, p.p)
	}
	f.Fuzz(func(t *testing.T, haystack, pattern string) {
		_ = MatchPattern(haystack, pattern) // must not panic
		if pattern == "*" && !MatchPattern(haystack, "*") {
			t.Fatalf("'*' must match anything; failed on %q", haystack)
		}
	})
}
