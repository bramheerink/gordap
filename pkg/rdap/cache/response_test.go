package cache

import (
	"testing"
	"time"
)

func TestResponseCache_RoundTrip(t *testing.T) {
	rc := NewResponseCache(4, time.Minute)
	rc.Put("domain", "example.nl", "anonymous", []byte(`{"ok":true}`), 200, map[string]string{"x": "y"})

	b, s, h, ok := rc.Get("domain", "example.nl", "anonymous")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(b) != `{"ok":true}` || s != 200 || h["x"] != "y" {
		t.Fatalf("wrong value: %q %d %+v", b, s, h)
	}
}

func TestResponseCache_TierIsolation(t *testing.T) {
	rc := NewResponseCache(4, time.Minute)
	rc.Put("domain", "example.nl", "anonymous", []byte("ANON"), 200, nil)
	rc.Put("domain", "example.nl", "privileged", []byte("PRIV"), 200, nil)

	b, _, _, _ := rc.Get("domain", "example.nl", "anonymous")
	if string(b) != "ANON" {
		t.Fatalf("anonymous bucket contaminated: %s", b)
	}
	b, _, _, _ = rc.Get("domain", "example.nl", "privileged")
	if string(b) != "PRIV" {
		t.Fatalf("privileged bucket wrong: %s", b)
	}
}

func TestResponseCache_TTLExpiry(t *testing.T) {
	rc := NewResponseCache(4, 10*time.Millisecond)
	rc.Put("d", "x", "a", []byte("v"), 200, nil)
	if _, _, _, ok := rc.Get("d", "x", "a"); !ok {
		t.Fatal("immediate hit expected")
	}
	time.Sleep(15 * time.Millisecond)
	if _, _, _, ok := rc.Get("d", "x", "a"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestResponseCache_LRUEvicts(t *testing.T) {
	rc := NewResponseCache(2, time.Minute)
	rc.Put("d", "a", "t", []byte("A"), 200, nil)
	rc.Put("d", "b", "t", []byte("B"), 200, nil)
	rc.Put("d", "c", "t", []byte("C"), 200, nil) // evicts a
	if _, _, _, ok := rc.Get("d", "a", "t"); ok {
		t.Fatal("a should have been evicted")
	}
	if _, _, _, ok := rc.Get("d", "b", "t"); !ok {
		t.Fatal("b should still be present")
	}
}

func TestResponseCache_ZeroSize_Disabled(t *testing.T) {
	rc := NewResponseCache(0, time.Minute)
	rc.Put("d", "x", "t", []byte("v"), 200, nil)
	if _, _, _, ok := rc.Get("d", "x", "t"); ok {
		t.Fatal("zero-size cache must not store entries")
	}
	if rc.Len() != 0 {
		t.Fatalf("len: %d", rc.Len())
	}
}

func TestResponseCache_BodyIsDefensivelyCopied(t *testing.T) {
	rc := NewResponseCache(1, time.Minute)
	orig := []byte("original")
	rc.Put("d", "x", "t", orig, 200, nil)
	orig[0] = 'X' // mutate caller's buffer

	b, _, _, _ := rc.Get("d", "x", "t")
	if string(b) != "original" {
		t.Fatalf("cache did not copy body: %s", b)
	}
}
