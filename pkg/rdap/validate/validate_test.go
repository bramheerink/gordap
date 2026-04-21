package validate

import (
	"strings"
	"testing"
)

func TestDomainLength(t *testing.T) {
	cases := []struct {
		name string
		in   string
		err  error
	}{
		{"ok", "example.nl", nil},
		{"empty", "", ErrCharSet},
		{"at limit", strings.Repeat("a.", 126) + "a", nil}, // 253 chars
		{"over limit", strings.Repeat("a", 254), ErrTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DomainLength(c.in); got != c.err {
				t.Errorf("got %v, want %v", got, c.err)
			}
		})
	}
}

func TestHandle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		err  error
	}{
		{"simple", "DOM-123", nil},
		{"underscored ROID", "2336799_DOMAIN_COM-VRSN", nil},
		{"empty", "", ErrCharSet},
		{"space", "BAD HANDLE", ErrCharSet},
		{"path traversal attempt", "../etc/passwd", ErrCharSet},
		{"unicode", "bücher", ErrCharSet},
		{"dot prefix", ".hidden", ErrCharSet},
		{"dot suffix", "hidden.", ErrCharSet},
		{"too long", strings.Repeat("A", 65), ErrTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Handle(c.in); got != c.err {
				t.Errorf("got %v, want %v", got, c.err)
			}
		})
	}
}

func TestPathSegment_RejectsAbsurdLength(t *testing.T) {
	huge := strings.Repeat("x", MaxQueryLength+1)
	if PathSegment(huge) != ErrTooLong {
		t.Fatal("expected ErrTooLong for oversized path segment")
	}
	if PathSegment("ok") != nil {
		t.Fatal("normal input should pass")
	}
}
