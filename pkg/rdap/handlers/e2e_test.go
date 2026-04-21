package handlers_test

// End-to-end tests for the HTTP surface: exercise NewRouter + auth
// middleware + mapping + redaction through a real httptest server. The
// DataSource is satisfied with an inline fake — the interface itself is
// the test seam.

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
	"time"

	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/handlers"
)

type fakeDS struct {
	domains    map[string]*datasource.Domain
	entities   map[string]*datasource.Contact
	nameserver map[string]*datasource.Nameserver
	networks   []*datasource.IPNetwork
}

func (f *fakeDS) GetDomain(_ context.Context, name string) (*datasource.Domain, error) {
	if d, ok := f.domains[strings.ToLower(name)]; ok {
		return d, nil
	}
	return nil, datasource.ErrNotFound
}
func (f *fakeDS) GetEntity(_ context.Context, handle string) (*datasource.Contact, error) {
	if e, ok := f.entities[handle]; ok {
		return e, nil
	}
	return nil, datasource.ErrNotFound
}
func (f *fakeDS) GetNameserver(_ context.Context, name string) (*datasource.Nameserver, error) {
	if n, ok := f.nameserver[strings.ToLower(name)]; ok {
		return n, nil
	}
	return nil, datasource.ErrNotFound
}
func (f *fakeDS) GetIPNetwork(_ context.Context, ip netip.Addr) (*datasource.IPNetwork, error) {
	for _, n := range f.networks {
		if n.Prefix.Contains(ip) {
			return n, nil
		}
	}
	return nil, datasource.ErrNotFound
}

// fakeVerifier accepts one hard-coded token and issues privileged claims.
type fakeVerifier struct{ token string }

func (f fakeVerifier) Verify(_ context.Context, tok string) (auth.Claims, error) {
	if tok != f.token {
		return auth.Claims{}, context.Canceled
	}
	return auth.Claims{Subject: "op", Level: auth.AccessPrivileged}, nil
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeDS) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ds := &fakeDS{
		domains:    map[string]*datasource.Domain{},
		entities:   map[string]*datasource.Contact{},
		nameserver: map[string]*datasource.Nameserver{},
	}
	ds.entities["REG-1"] = &datasource.Contact{
		Handle: "REG-1", Kind: "org", Roles: []string{"registrant"},
		Organization: "Example B.V.", FullName: "Jane Doe",
		Emails: []string{"hostmaster@example.nl"},
		Phones: []datasource.Phone{{Number: "+31201234567", Kinds: []string{"voice"}}},
		Address: &datasource.Address{
			Street: []string{"Herengracht 1"}, Locality: "Amsterdam",
			PostalCode: "1015BA", CountryCode: "NL",
		},
		CreatedAt: now, UpdatedAt: now,
	}
	ds.domains["example.nl"] = &datasource.Domain{
		Handle: "DOM-1", LDHName: "example.nl",
		Status: []string{"active"}, Registered: now, LastChanged: now, Expires: now.AddDate(1, 0, 0),
		Nameservers: []datasource.Nameserver{{LDHName: "ns1.example.nl"}},
		Contacts:    []datasource.Contact{*ds.entities["REG-1"]},
	}
	ds.nameserver["ns1.example.nl"] = &datasource.Nameserver{LDHName: "ns1.example.nl"}
	ds.networks = append(ds.networks, &datasource.IPNetwork{
		Handle: "NET-1", Prefix: netip.MustParsePrefix("192.0.2.0/24"),
		Status: []string{"active"}, Registered: now, LastChanged: now,
	})

	srv := &handlers.Server{DS: ds, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := handlers.NewRouter(srv, fakeVerifier{token: "secret"})
	return httptest.NewServer(handler), ds
}

func do(t *testing.T, ts *httptest.Server, path, accept, bearer string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var decoded map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &decoded)
	}
	return resp, decoded
}

func TestE2E_GetDomain_OK_AnonymousRedaction(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, body := do(t, ts, "/domain/example.nl", "application/rdap+json", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/rdap+json" {
		t.Fatalf("content-type: %q", ct)
	}
	if body["objectClassName"] != "domain" {
		t.Fatalf("wrong object class: %+v", body)
	}
	entities, _ := body["entities"].([]any)
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %+v", entities)
	}
	card, _ := entities[0].(map[string]any)["jscard"].(map[string]any)
	if card == nil {
		t.Fatal("jscard missing")
	}
	if _, hasEmails := card["emails"]; hasEmails {
		t.Fatalf("anonymous jscard must not include emails: %+v", card)
	}
	if _, hasPhones := card["phones"]; hasPhones {
		t.Fatalf("anonymous jscard must not include phones: %+v", card)
	}
	if _, hasOrg := card["organizations"]; !hasOrg {
		t.Fatalf("anonymous jscard must expose organization: %+v", card)
	}
}

func TestE2E_GetDomain_OK_PrivilegedSeesChannels(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, body := do(t, ts, "/domain/example.nl", "application/rdap+json", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	entities := body["entities"].([]any)
	card := entities[0].(map[string]any)["jscard"].(map[string]any)
	emails, ok := card["emails"].(map[string]any)
	if !ok || len(emails) == 0 {
		t.Fatalf("privileged caller must see emails: %+v", card)
	}
}

func TestE2E_GetDomain_NotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/domain/missing.nl", "application/rdap+json", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "error" || body["errorCode"].(float64) != 404 {
		t.Fatalf("expected RDAP error object, got %+v", body)
	}
}

func TestE2E_GetDomain_NotAcceptable(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, _ := do(t, ts, "/domain/example.nl", "text/html", "")
	if resp.StatusCode != http.StatusNotAcceptable {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestE2E_GetEntity_OK(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/entity/REG-1", "", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "entity" {
		t.Fatalf("wrong class: %+v", body)
	}
}

func TestE2E_GetNameserver_OK(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/nameserver/ns1.example.nl", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "nameserver" {
		t.Fatalf("wrong class: %+v", body)
	}
}

func TestE2E_GetIP_BadInput(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/ip/not-an-ip", "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["errorCode"].(float64) != 400 {
		t.Fatalf("wrong error code: %+v", body)
	}
}

func TestE2E_GetIP_OK(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/ip/192.0.2.42", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "ip network" {
		t.Fatalf("wrong class: %+v", body)
	}
}

func TestE2E_Help(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/help", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "help" {
		t.Fatalf("wrong class: %+v", body)
	}
	notices, _ := body["notices"].([]any)
	if len(notices) == 0 {
		t.Fatalf("expected notices, got %+v", body)
	}
}

func TestE2E_UnknownPath_RDAPError(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()
	resp, body := do(t, ts, "/autnum/64496", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if body["objectClassName"] != "error" {
		t.Fatalf("expected rdap error object, got %+v", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/rdap+json" {
		t.Fatalf("content-type: %q", got)
	}
}
