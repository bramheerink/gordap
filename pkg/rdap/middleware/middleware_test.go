package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ok200(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rdap+json")
		_, _ = io.WriteString(w, body)
	})
}

func TestCORS_PreflightReturns204WithHeaders(t *testing.T) {
	h := CORS()(ok200("{}"))
	req := httptest.NewRequest(http.MethodOptions, "/domain/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight code: %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing CORS origin: %v", w.Header())
	}
}

func TestCORS_GetSetsHeaders(t *testing.T) {
	h := CORS()(ok200("{}"))
	req := httptest.NewRequest(http.MethodGet, "/domain/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing CORS origin")
	}
	if !strings.Contains(w.Header().Get("Vary"), "Origin") {
		t.Fatalf("expected Vary to mention Origin, got %q", w.Header().Get("Vary"))
	}
}

func TestRateLimiter_AllowsThenRejects(t *testing.T) {
	rl := NewRateLimiter(0, 2) // rate=0 disables recovery; burst=2
	// rate=0 short-circuits Allow — use rate>0 to actually exercise limit:
	rl = NewRateLimiter(1, 2)
	if !rl.Allow("k") {
		t.Fatal("first request should pass")
	}
	if !rl.Allow("k") {
		t.Fatal("second request (within burst) should pass")
	}
	if rl.Allow("k") {
		t.Fatal("third immediate request should be rejected")
	}
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	if !rl.Allow("a") {
		t.Fatal("key a first request")
	}
	if !rl.Allow("b") {
		t.Fatal("key b first request")
	}
	if rl.Allow("a") {
		t.Fatal("key a burst exhausted")
	}
}

func TestRateLimiter_Middleware_EmitsRetryAfter(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	h := rl.Middleware(func(_ *http.Request) string { return "fixed" })(ok200("{}"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first req: %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second req code: %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on 429")
	}
}

func TestRateLimiter_Disabled(t *testing.T) {
	rl := NewRateLimiter(0, 0)
	for i := 0; i < 100; i++ {
		if !rl.Allow("any") {
			t.Fatalf("rate=0 should pass all; failed at %d", i)
		}
	}
}

func TestGzip_CompressesLargeJSON(t *testing.T) {
	big := strings.Repeat(`{"rdapConformance":["rdap_level_0"]}`, 200)
	h := Gzip(128)(ok200(big))
	req := httptest.NewRequest(http.MethodGet, "/domain/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding, headers: %v", w.Header())
	}
	gz, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(plain) != big {
		t.Fatalf("round-trip mismatch: len got=%d want=%d", len(plain), len(big))
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("expected Vary: Accept-Encoding")
	}
}

func TestGzip_SkipsSmallBodies(t *testing.T) {
	h := Gzip(1024)(ok200("{}"))
	req := httptest.NewRequest(http.MethodGet, "/domain/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("small body should not be compressed; got %q", w.Header().Get("Content-Encoding"))
	}
	if w.Body.String() != "{}" {
		t.Fatalf("body mutated unexpectedly: %q", w.Body.String())
	}
}

func TestGzip_SkippedWhenClientDoesNotAccept(t *testing.T) {
	big := strings.Repeat(`{"a":1}`, 500)
	h := Gzip(64)(ok200(big))
	req := httptest.NewRequest(http.MethodGet, "/domain/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("client didn't advertise gzip; server should not compress")
	}
}

func TestRequestTimeout_CancelsContext(t *testing.T) {
	observed := make(chan error, 1)
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			observed <- r.Context().Err()
		case <-time.After(time.Second):
			observed <- nil
		}
	})
	h := RequestTimeout(10 * time.Millisecond)(slow)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if err := <-observed; err == nil {
		t.Fatal("expected context cancellation within timeout window")
	}
}

func TestSecurityHeaders_Present(t *testing.T) {
	h := SecurityHeaders()(ok200("{}"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, hdr := range []string{"Strict-Transport-Security", "Referrer-Policy", "X-Content-Type-Options"} {
		if w.Header().Get(hdr) == "" {
			t.Fatalf("missing %q", hdr)
		}
	}
}

func TestRateLimiter_Middleware_PanicsOnNilKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when key function is nil")
		}
	}()
	rl := NewRateLimiter(1, 1)
	_ = rl.Middleware(nil)
}

func TestMaxRequestBody_RejectsLargePayload(t *testing.T) {
	h := MaxRequestBody(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("way too many bytes here"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}
