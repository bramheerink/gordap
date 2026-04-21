package handlers_test

import (
	"context"
	"encoding/json"
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
	"github.com/bramheerink/gordap/pkg/rdap/storage/memory"
)

type searchDSShim struct {
	*memory.Store
}

// satisfy datasource.DataSource by embedding
var _ datasource.DataSource = (*searchDSShim)(nil)

func newSearchServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := memory.New()
	for _, name := range []string{"example.nl", "foo.nl", "bar.com"} {
		store.PutDomain(&datasource.Domain{LDHName: name, Handle: "D-" + name})
	}
	store.PutEntity(&datasource.Contact{
		Handle: "REG-1", Kind: "org", Organization: "Example B.V.",
		Emails: []string{"hostmaster@example.nl"},
		Address: &datasource.Address{CountryCode: "NL"},
	})
	store.PutNameserver(&datasource.Nameserver{LDHName: "ns1.example.nl", Handle: "NS-1"})

	srv := &handlers.Server{
		DS:     store,
		Search: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return httptest.NewServer(handlers.NewRouter(srv, auth.NopVerifier()))
}

func getJSON(t *testing.T, ts *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func TestE2E_SearchDomains_ByName(t *testing.T) {
	ts := newSearchServer(t)
	defer ts.Close()
	code, body := getJSON(t, ts, "/domains?name=example.*")
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	if body["objectClassName"] != "domainSearchResults" {
		t.Fatalf("wrong envelope: %+v", body)
	}
	arr, _ := body["domainSearchResults"].([]any)
	if len(arr) != 1 {
		t.Fatalf("want 1 result, got %d", len(arr))
	}
	first := arr[0].(map[string]any)
	if first["ldhName"] != "example.nl" {
		t.Fatalf("ldhName: %v", first["ldhName"])
	}
}

func TestE2E_SearchEntities_ByEmail(t *testing.T) {
	ts := newSearchServer(t)
	defer ts.Close()
	code, body := getJSON(t, ts, "/entities?email=hostmaster@*")
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	arr, _ := body["entitySearchResults"].([]any)
	if len(arr) != 1 {
		t.Fatalf("want 1 entity, got %d", len(arr))
	}
}

func TestE2E_SearchNameservers_ByName(t *testing.T) {
	ts := newSearchServer(t)
	defer ts.Close()
	code, body := getJSON(t, ts, "/nameservers?name=ns1.*")
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	arr, _ := body["nameserverSearchResults"].([]any)
	if len(arr) != 1 {
		t.Fatalf("want 1 nameserver, got %d", len(arr))
	}
}

func TestE2E_Search_MissingPredicate_400(t *testing.T) {
	ts := newSearchServer(t)
	defer ts.Close()
	code, body := getJSON(t, ts, "/domains")
	if code != http.StatusBadRequest {
		t.Fatalf("status: %d", code)
	}
	if body["errorCode"].(float64) != 400 {
		t.Fatalf("errorCode: %+v", body)
	}
}

func TestE2E_Search_NotImplementedWhenDisabled(t *testing.T) {
	// Spin up a server without Search wired.
	srv := &handlers.Server{
		DS:     memory.New(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ts := httptest.NewServer(handlers.NewRouter(srv, auth.NopVerifier()))
	defer ts.Close()

	code, body := getJSON(t, ts, "/domains?name=example.*")
	if code != http.StatusNotImplemented {
		t.Fatalf("status: %d", code)
	}
	if body["errorCode"].(float64) != 501 {
		t.Fatalf("errorCode: %+v", body)
	}
}

// sanity: the shim that hides the concrete memory type still satisfies
// both interfaces — the search package gets memory.Store via interface
// assertion in main.go.
func TestE2E_Search_PaginationMetadata(t *testing.T) {
	store := memory.New()
	for i := 0; i < 15; i++ {
		store.PutDomain(&datasource.Domain{LDHName: string(rune('a'+i)) + ".nl", Handle: "h"})
	}
	srv := &handlers.Server{DS: store, Search: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(handlers.NewRouter(srv, auth.NopVerifier()))
	defer ts.Close()

	code, body := getJSON(t, ts, "/domains?name=*&count=5")
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	meta, ok := body["paging_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected paging metadata: %+v", body)
	}
	if meta["totalCount"].(float64) != 15 {
		t.Fatalf("totalCount: %+v", meta)
	}
	if meta["pageSize"].(float64) != 5 {
		t.Fatalf("pageSize: %+v", meta)
	}
	if meta["nextCursor"] == nil {
		t.Fatalf("expected nextCursor to be set on first page")
	}
	// And searching with *any* leading term matches all — verify "a*"
	// returns exactly 1 in the small-set filter.
	code, body = getJSON(t, ts, "/domains?name=a*")
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	if arr, _ := body["domainSearchResults"].([]any); len(arr) != 1 {
		t.Fatalf("a* count: %d", len(arr))
	}
	_ = strings.Contains // silence unused
}

// sanity for the context package — some test frameworks complain when
// the import isn't directly used in a test fn.
var _ = context.Background
var _ = netip.MustParseAddr
