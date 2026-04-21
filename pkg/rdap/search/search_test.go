package search

import (
	"context"
	"errors"
	"testing"
)

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		in, pat string
		want    bool
	}{
		// Strict equality
		{"example.nl", "example.nl", true},
		{"example.nl", "example.com", false},

		// Prefix wildcard (trailing *) — RFC 7482 conformant
		{"example.nl", "example.*", true},
		{"example.nl", "ex*", true},
		{"other.nl", "ex*", false},

		// Suffix wildcard (leading *) — RIR extension
		{"ns1.example.nl", "*.nl", true},
		{"ns1.example.nl", "*example.nl", true},
		{"example.com", "*.nl", false},

		// Infix wildcard (both) — registry extension
		{"ns1.example.nl", "*example*", true},
		{"other.com", "*example*", false},

		// Match-any
		{"", "*", true},
		{"anything", "*", true},
	}
	for _, c := range cases {
		if got := MatchPattern(c.in, c.pat); got != c.want {
			t.Errorf("MatchPattern(%q, %q) = %v; want %v", c.in, c.pat, got, c.want)
		}
	}
}

func TestClampLimit(t *testing.T) {
	if ClampLimit(0, 50, 500) != 50 {
		t.Fatal("default applied when requested=0")
	}
	if ClampLimit(100, 50, 500) != 100 {
		t.Fatal("mid value passes through")
	}
	if ClampLimit(9000, 50, 500) != 500 {
		t.Fatal("max cap enforced")
	}
}

func TestNull_AlwaysNotImplemented(t *testing.T) {
	n := Null{}
	if _, err := n.SearchDomains(context.Background(), Query{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("SearchDomains should be unimplemented")
	}
	if _, err := n.SearchEntities(context.Background(), Query{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("SearchEntities should be unimplemented")
	}
	if _, err := n.SearchNameservers(context.Background(), Query{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("SearchNameservers should be unimplemented")
	}
}
