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
		{"example.nl", "example.nl", true},
		{"example.nl", "example.*", true},
		{"example.nl", "ex*", true},
		{"other.nl", "ex*", false},
		{"example.nl", "example.com", false},
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
