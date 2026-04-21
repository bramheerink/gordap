package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/handlers"
)

// fakeBootstrap maps zones / prefixes to server URLs for testing the
// RFC 7484 redirect path without touching the network.
type fakeBootstrap struct {
	zones map[string][]string
	nets  []struct {
		p netip.Prefix
		u []string
	}
}

func (f *fakeBootstrap) DomainServers(name string) []string {
	for n := name; n != ""; {
		if srv, ok := f.zones[n]; ok {
			return srv
		}
		i := strings.IndexByte(n, '.')
		if i < 0 {
			return nil
		}
		n = n[i+1:]
	}
	return nil
}

func (f *fakeBootstrap) IPServers(ip netip.Addr) []string {
	for _, e := range f.nets {
		if e.p.Contains(ip) {
			return e.u
		}
	}
	return nil
}

func unredirectedClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func emptyFakeDS() *fakeDS {
	return &fakeDS{
		domains:    map[string]*datasource.Domain{},
		entities:   map[string]*datasource.Contact{},
		nameserver: map[string]*datasource.Nameserver{},
	}
}

func newBootstrapServer(t *testing.T, bs handlers.BootstrapRegistry) *httptest.Server {
	t.Helper()
	srv := &handlers.Server{
		DS:        emptyFakeDS(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Bootstrap: bs,
	}
	return httptest.NewServer(handlers.NewRouter(srv, auth.NopVerifier()))
}

func TestE2E_Bootstrap_DomainRedirect(t *testing.T) {
	bs := &fakeBootstrap{zones: map[string][]string{
		"nl": {"https://rdap.sidn.example/"},
	}}
	ts := newBootstrapServer(t, bs)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/domain/unknown.nl", nil)
	resp, err := unredirectedClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://rdap.sidn.example/domain/unknown.nl" {
		t.Fatalf("Location: %q", loc)
	}
}

func TestE2E_Bootstrap_IDNDomainRedirectsToPunycode(t *testing.T) {
	bs := &fakeBootstrap{zones: map[string][]string{
		"example": {"https://rdap.example-tld/"},
	}}
	ts := newBootstrapServer(t, bs)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/domain/b%C3%BCcher.example", nil)
	resp, err := unredirectedClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	want := "https://rdap.example-tld/domain/xn--bcher-kva.example"
	if loc := resp.Header.Get("Location"); loc != want {
		t.Fatalf("Location: got %q want %q", loc, want)
	}
}

func TestE2E_Bootstrap_UnknownZoneFallsThroughTo404(t *testing.T) {
	bs := &fakeBootstrap{zones: map[string][]string{}}
	ts := newBootstrapServer(t, bs)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/domain/example.xyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestE2E_Bootstrap_IPRedirect(t *testing.T) {
	bs := &fakeBootstrap{nets: []struct {
		p netip.Prefix
		u []string
	}{
		{netip.MustParsePrefix("192.0.2.0/24"), []string{"https://rdap.ripe.example/"}},
	}}
	ts := newBootstrapServer(t, bs)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ip/192.0.2.42", nil)
	resp, err := unredirectedClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://rdap.ripe.example/ip/192.0.2.42" {
		t.Fatalf("Location: %q", loc)
	}
}

func TestE2E_Bootstrap_NilRegistry_StillFourOhFour(t *testing.T) {
	ts := newBootstrapServer(t, nil)
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL + "/domain/example.nl")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
