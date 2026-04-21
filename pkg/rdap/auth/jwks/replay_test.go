package jwks

import (
	"testing"
	"time"
)

func TestReplayCache_FirstCallPasses(t *testing.T) {
	c := newReplayCache()
	if c.check("jti-1", time.Now().Add(time.Minute)) {
		t.Fatal("first call must not be flagged as replay")
	}
}

func TestReplayCache_SecondCallIsReplay(t *testing.T) {
	c := newReplayCache()
	exp := time.Now().Add(time.Minute)
	c.check("jti-1", exp)
	if !c.check("jti-1", exp) {
		t.Fatal("second call with same jti must be flagged")
	}
}

func TestReplayCache_EmptyJTI_NeverReplay(t *testing.T) {
	c := newReplayCache()
	for i := 0; i < 3; i++ {
		if c.check("", time.Now().Add(time.Minute)) {
			t.Fatalf("empty jti must never be flagged (iteration %d)", i)
		}
	}
}

func TestReplayCache_IndependentJTIs(t *testing.T) {
	c := newReplayCache()
	exp := time.Now().Add(time.Minute)
	c.check("a", exp)
	if c.check("b", exp) {
		t.Fatal("different jtis must not collide")
	}
}

func TestReplayCache_ExpiredEntryCanBeReused(t *testing.T) {
	c := newReplayCache()
	past := time.Now().Add(-time.Second)
	c.check("jti-x", past)
	// First call seeded an already-expired entry. The *next* check
	// should NOT be treated as replay because the original exp has
	// passed — a token past its own exp is useless anyway, and the
	// jti is free to be reused for a brand-new signed token.
	if c.check("jti-x", time.Now().Add(time.Minute)) {
		t.Fatal("re-use of jti past original exp should not flag replay")
	}
}
