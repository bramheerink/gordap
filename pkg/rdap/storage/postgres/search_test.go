package postgres

import (
	"testing"
)

func TestILIKEPattern(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.*", "example.%"},
		{"foo*", "foo%"},
		{"plain", "plain"},
		// Hostile input: user includes a raw SQL wildcard. Must be
		// escaped so it cannot broaden the match.
		{"100%risky", `100\%risky`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
	}
	for _, c := range cases {
		if got := ilikePattern(c.in); got != c.want {
			t.Errorf("ilikePattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
