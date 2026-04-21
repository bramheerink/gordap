package idn

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"example.nl", "example.nl", true},
		{"EXAMPLE.NL", "example.nl", true},
		{"example.nl.", "example.nl", true},
		{"  example.nl  ", "example.nl", true},
		{"bücher.example", "xn--bcher-kva.example", true},
		{"παράδειγμα.ελ", "xn--hxajbheg2az3al.xn--qxam", true},
		{"", "", false},
		{".", "", false},
		{"-bad.example", "", false},
		{"exa mple.nl", "", false},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Normalize(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
