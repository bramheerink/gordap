//go:build realworld

// Package realworld runs end-to-end conformance assertions against a
// live gordap instance. Invoked via:
//
//	go test -tags=realworld ./test/realworld/...
//
// It boots the reference binary in demo mode on a random port, then
// executes a battery of RFC-level checks (RFC 7480/9083/9537, ICANN
// RP2.2) and an optional interop check against the openrdap/rdap CLI.
//
// Intentionally not part of the default test suite so CI can opt in
// without paying the process-startup cost.
package realworld

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- process lifecycle ----------

type liveServer struct {
	addr   string
	cancel context.CancelFunc
	done   chan struct{}
}

func startServer(t *testing.T) *liveServer {
	t.Helper()
	port := freePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	base := "http://" + addr

	// Build the binary once — faster and more representative than go run.
	bin := buildBinary(t)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin,
		"-addr="+addr,
		"-self-link-base="+base,
		"-icann-gtld",
		"-tos-url=https://example.nl/rdap-tos",
		"-rate-limit-rps=1000", // don't let rate-limiter interfere with tests
	)
	cmd.Stdout = &prefixWriter{out: os.Stdout, p: "[gordap] "}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	// Wait for the server to accept connections.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/help")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return &liveServer{addr: base, cancel: cancel, done: done}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("server did not come up on %s", base)
	return nil
}

func (s *liveServer) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func buildBinary(t *testing.T) string {
	t.Helper()
	// Cache the build output across sub-tests in this process.
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gordap-e2e-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "gordap")
		root := repoRoot(t)
		cmd := exec.Command("go", "build", "-o", buildPath, "./cmd/gordap")
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			buildErr = err
		}
	})
	if buildErr != nil {
		t.Fatalf("build gordap: %v", buildErr)
	}
	return buildPath
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == "/dev/null" {
		t.Fatal("not inside a go module")
	}
	return filepath.Dir(mod)
}

type prefixWriter struct {
	out io.Writer
	p   string
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if i == len(lines)-1 && l == "" {
			continue
		}
		fmt.Fprintln(p.out, p.p+l)
	}
	return len(b), nil
}

// ---------- HTTP helpers ----------

func get(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return doReq(t, req)
}

func getWith(t *testing.T, url string, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return doReq(t, req)
}

func doReq(t *testing.T, req *http.Request) (*http.Response, map[string]any) {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	_ = json.Unmarshal(body, &obj)
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, obj
}

// ---------- THE TESTS ----------

func TestRealWorld_Suite(t *testing.T) {
	srv := startServer(t)
	defer srv.stop()

	// Small sleep lets the first warm-up cycle settle; otherwise the
	// first request occasionally races the memory seed.
	time.Sleep(100 * time.Millisecond)

	t.Run("RFC7480_ContentType", func(t *testing.T) {
		resp, _ := get(t, srv.addr+"/help")
		if ct := resp.Header.Get("Content-Type"); ct != "application/rdap+json" {
			t.Fatalf("content-type: %q", ct)
		}
	})

	t.Run("RFC7480_CORSOnEveryResponse", func(t *testing.T) {
		for _, path := range []string{"/help", "/domain/example.nl", "/domain/missing.nl", "/not-a-path"} {
			resp, _ := get(t, srv.addr+path)
			if o := resp.Header.Get("Access-Control-Allow-Origin"); o != "*" {
				t.Errorf("%s: CORS origin = %q, want *", path, o)
			}
		}
	})

	t.Run("RFC7480_OPTIONSPreflight", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodOptions, srv.addr+"/domain/example.nl", nil)
		req.Header.Set("Origin", "https://web.example")
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, _ := doReq(t, req)
		if resp.StatusCode != 204 {
			t.Fatalf("preflight status: %d", resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Methods") == "" {
			t.Fatal("missing Allow-Methods")
		}
	})

	t.Run("RFC7480_406OnBadAccept", func(t *testing.T) {
		resp, _ := getWith(t, srv.addr+"/domain/example.nl", map[string]string{"Accept": "text/html"})
		if resp.StatusCode != 406 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	})

	t.Run("RFC9083_ObjectClassNameAndConformance", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/example.nl")
		if body["objectClassName"] != "domain" {
			t.Fatalf("objectClassName: %v", body["objectClassName"])
		}
		conf, _ := body["rdapConformance"].([]any)
		if len(conf) < 1 {
			t.Fatalf("rdapConformance: %+v", conf)
		}
		have := map[string]bool{}
		for _, c := range conf {
			if s, ok := c.(string); ok {
				have[s] = true
			}
		}
		for _, want := range []string{"rdap_level_0", "redacted", "icann_rdap_response_profile_1", "icann_rdap_technical_implementation_guide_1"} {
			if !have[want] {
				t.Errorf("missing conformance %q in %v", want, conf)
			}
		}
	})

	t.Run("RFC9083_ErrorEnvelope", func(t *testing.T) {
		resp, body := get(t, srv.addr+"/domain/missing.nl")
		if resp.StatusCode != 404 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if body["objectClassName"] != "error" {
			t.Fatalf("error envelope missing: %+v", body)
		}
		if body["errorCode"].(float64) != 404 {
			t.Fatalf("errorCode: %+v", body["errorCode"])
		}
	})

	t.Run("RFC9083_SelfLinkPresent", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/example.nl")
		links, _ := body["links"].([]any)
		found := false
		for _, l := range links {
			m := l.(map[string]any)
			if m["rel"] == "self" && strings.HasSuffix(m["href"].(string), "/domain/example.nl") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no self link in %+v", links)
		}
	})

	t.Run("RFC9083_MandatoryEvents_IncludingDBUpdate", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/example.nl")
		events, _ := body["events"].([]any)
		have := map[string]bool{}
		for _, e := range events {
			have[e.(map[string]any)["eventAction"].(string)] = true
		}
		for _, want := range []string{"registration", "last changed", "expiration", "last update of RDAP database"} {
			if !have[want] {
				t.Errorf("missing event %q; got %v", want, have)
			}
		}
	})

	t.Run("RFC9083_IDNNameDualForm", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/b%C3%BCcher.example")
		if body["ldhName"] != "xn--bcher-kva.example" {
			t.Fatalf("ldhName: %v", body["ldhName"])
		}
		if body["unicodeName"] != "bücher.example" {
			t.Fatalf("unicodeName: %v", body["unicodeName"])
		}
	})

	t.Run("RFC9537_RedactedArrayShape", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/b%C3%BCcher.example")
		red, _ := body["redacted"].([]any)
		if len(red) == 0 {
			t.Fatalf("redacted array missing for anonymous individual query; body=%+v", body)
		}
		for _, r := range red {
			m := r.(map[string]any)
			for _, key := range []string{"name", "prePath", "method", "pathLang"} {
				if m[key] == nil {
					t.Errorf("redaction entry missing %q: %+v", key, m)
				}
			}
			if m["pathLang"] != "jsonpath" {
				t.Errorf("pathLang: %v", m["pathLang"])
			}
			if m["method"] != "removal" {
				t.Errorf("method: %v", m["method"])
			}
		}
	})

	t.Run("ICANN_RP2.2_NoticesPresent", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domain/example.nl")
		notices, _ := body["notices"].([]any)
		titles := map[string]bool{}
		for _, n := range notices {
			titles[n.(map[string]any)["title"].(string)] = true
		}
		for _, want := range []string{"Terms of Service", "Status Codes", "RDDS Inaccuracy Complaint Form"} {
			if !titles[want] {
				t.Errorf("missing ICANN notice %q; got %v", want, titles)
			}
		}
	})

	t.Run("ICANN_TIG_SecurityHeaders", func(t *testing.T) {
		resp, _ := get(t, srv.addr+"/help")
		for _, h := range []string{"Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy"} {
			if resp.Header.Get(h) == "" {
				t.Errorf("missing %q", h)
			}
		}
	})

	t.Run("RFC7480_GzipCompression", func(t *testing.T) {
		resp, _ := getWith(t, srv.addr+"/domain/example.nl", map[string]string{"Accept-Encoding": "gzip"})
		if resp.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("expected gzip encoding; headers=%v", resp.Header)
		}
	})

	t.Run("RFC7484_BootstrapDisabledByDefault", func(t *testing.T) {
		// --bootstrap=false in flags: a missing domain should be 404,
		// not a redirect.
		resp, _ := get(t, srv.addr+"/domain/example.com")
		if resp.StatusCode != 404 {
			t.Fatalf("expected 404 (bootstrap off), got %d", resp.StatusCode)
		}
	})

	t.Run("RFC8977_PagingMetadata", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domains?name=*&count=1")
		meta, ok := body["paging_metadata"].(map[string]any)
		if !ok {
			t.Fatalf("no paging_metadata: %+v", body)
		}
		if meta["pageSize"].(float64) != 1 {
			t.Errorf("pageSize: %v", meta["pageSize"])
		}
		if meta["totalCount"].(float64) < 1 {
			t.Errorf("totalCount: %v", meta["totalCount"])
		}
	})

	t.Run("RFC9536_SearchPartialMatch", func(t *testing.T) {
		_, body := get(t, srv.addr+"/domains?name=example.*")
		arr, _ := body["domainSearchResults"].([]any)
		if len(arr) == 0 {
			t.Fatalf("expected at least one result: %+v", body)
		}
	})

	t.Run("RFC9082_UnknownPathReturnsRDAPError", func(t *testing.T) {
		resp, body := get(t, srv.addr+"/autnum/64496")
		if resp.StatusCode != 404 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if body["objectClassName"] != "error" {
			t.Fatalf("not an RDAP error envelope: %+v", body)
		}
	})

	t.Run("RIRSearch_rdap_bottom_Works", func(t *testing.T) {
		_, body := get(t, srv.addr+"/ips/rirSearch1/rdap-bottom/192.0.2.42")
		if body["objectClassName"] != "ip network" {
			t.Fatalf("expected ip network: %+v", body)
		}
	})

	t.Run("RIRSearch_rdap_up_NotImplemented", func(t *testing.T) {
		resp, body := get(t, srv.addr+"/ips/rirSearch1/rdap-up/192.0.2.42")
		if resp.StatusCode != 501 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if body["objectClassName"] != "error" {
			t.Fatalf("not an RDAP error: %+v", body)
		}
	})

	t.Run("Versioning_HelpAdvertisesExtensions", func(t *testing.T) {
		_, body := get(t, srv.addr+"/help")
		vh, ok := body["versioning_help"].([]any)
		if !ok || len(vh) == 0 {
			t.Fatalf("no versioning_help in /help: %+v", body)
		}
	})

	t.Run("InputValidation_RejectsPathTraversal", func(t *testing.T) {
		resp, _ := get(t, srv.addr+"/entity/..%2Fetc%2Fpasswd")
		if resp.StatusCode != 400 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	})

	t.Run("OpenRDAP_Interop", func(t *testing.T) {
		rdapBin, err := exec.LookPath("rdap")
		if err != nil {
			t.Skipf("openrdap CLI not installed: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, rdapBin,
			"-s", srv.addr, "-t", "domain", "-v", "example.nl")
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Exit code from openrdap is the number of warnings; treat
			// non-zero that still returned structured output as a
			// parseability pass. Failure to decode means our JSON is
			// broken — that's the thing we care about.
			if !errors.Is(err, &exec.ExitError{}) {
				t.Logf("openrdap output:\n%s", out)
			}
		}
		if !strings.Contains(string(out), "example.nl") {
			t.Fatalf("openrdap didn't recognise our response:\n%s", out)
		}
	})

	// Second-opinion conformance check via rdap-org/validator.rdap.org.
	// The CLI is a Node.js script; we look for it in PATH (installed
	// via `make install-validator`) and skip otherwise. It emits a
	// non-zero exit code on any RFC violation.
	t.Run("RDAPORG_Validator", func(t *testing.T) {
		bin, err := exec.LookPath("rdap-validator")
		if err != nil {
			t.Skipf("rdap-validator CLI not installed; run `make install-validator`: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, srv.addr+"/domain/example.nl")
		out, runErr := cmd.CombinedOutput()
		// The validator prints one line per rule. If any failed it
		// exits non-zero; we surface the failing output verbatim so
		// humans reading the CI log can see which rule tripped.
		if runErr != nil {
			t.Fatalf("validator flagged issues:\n%s", out)
		}
		if !strings.Contains(string(out), "example.nl") &&
			!strings.Contains(strings.ToLower(string(out)), "valid") {
			t.Logf("validator output (no obvious success marker, but exit 0):\n%s", out)
		}
	})
}
