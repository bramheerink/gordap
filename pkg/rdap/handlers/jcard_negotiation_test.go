package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Verifies the per-request jCard negotiation added for clients that
// need the legacy RFC 7095 format exclusively (some ICANN conformance
// tools, a few older RIR clients).

func TestE2E_JCardNegotiation_QueryParam(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/domain/example.nl?jscard=false")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	s := string(body)
	if !strings.Contains(s, `"vcardArray":["vcard"`) {
		t.Fatalf("?jscard=false must produce vcardArray:\n%s", s)
	}
	if strings.Contains(s, `"jscard":`) {
		t.Fatalf("?jscard=false must omit jscard member:\n%s", s)
	}
}

func TestE2E_JCardNegotiation_AcceptProfile(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/domain/example.nl", nil)
	req.Header.Set("Accept", "application/rdap+json; profile=jcard")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var m map[string]any
	_ = json.Unmarshal(body, &m)
	entities, _ := m["entities"].([]any)
	if len(entities) == 0 {
		t.Fatalf("no entities: %+v", m)
	}
	e := entities[0].(map[string]any)
	if _, hasJSCard := e["jscard"]; hasJSCard {
		t.Fatalf("Accept profile=jcard must omit jscard: %+v", e)
	}
	if _, hasVCard := e["vcardArray"]; !hasVCard {
		t.Fatalf("Accept profile=jcard must emit vcardArray: %+v", e)
	}
}

func TestE2E_JCardNegotiation_DefaultLeavesJSCard(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	// No query param, no profile: server defaults apply.
	resp, err := ts.Client().Get(ts.URL + "/domain/example.nl")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"jscard"`) {
		t.Fatalf("default response must carry jscard: %s", body)
	}
}
