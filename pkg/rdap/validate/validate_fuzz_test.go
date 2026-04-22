package validate

import "testing"

// FuzzHandle confirms input validation never panics on adversarial
// strings (control chars, NULs, deeply nested escapes) and that any
// input it accepts stays inside the documented charset and length.
func FuzzHandle(f *testing.F) {
	for _, seed := range []string{
		"DOM-1", "REG-EXAMPLE", "", " ", "../etc/passwd",
		"normal_handle.42", "\x00", "A" + "long" + "name",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		err := Handle(in)
		if err != nil {
			return // rejected — fine
		}
		// Accepted: enforce our promises about what made it through.
		if len(in) == 0 || len(in) > MaxHandleLength {
			t.Fatalf("Handle accepted out-of-bounds length %d: %q", len(in), in)
		}
		for i := 0; i < len(in); i++ {
			b := in[i]
			ok := (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') ||
				(b >= 'a' && b <= 'z') || b == '-' || b == '_' || b == '.'
			if !ok {
				t.Fatalf("Handle accepted disallowed byte %#02x at %d in %q", b, i, in)
			}
		}
	})
}

func FuzzPathSegment(f *testing.F) {
	f.Add("a")
	f.Add("")
	f.Add("a very long path that should still be ok if under cap")
	f.Fuzz(func(t *testing.T, in string) {
		err := PathSegment(in)
		if err == nil && len(in) > MaxQueryLength {
			t.Fatalf("PathSegment accepted oversized input: %d > %d", len(in), MaxQueryLength)
		}
	})
}
