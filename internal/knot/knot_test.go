package knot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// M1 identity surface: did:web document, health, and capability description.
// These are the first endpoints any Tangled consumer calls (the web app
// probes describeKnot when a user adds the knot in Settings → Knots).

func TestWellKnownDidJSON(t *testing.T) {
	h := New(Options{Host: "knot.forgesi.org", Version: "0.1.0"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/did.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc struct {
		ID              string `json:"id"`
		VerificationMet []struct {
			ID                 string `json:"id"`
			Type               string `json:"type"`
			PublicKeyMultibase string `json:"publicKeyMultibase"`
		} `json:"verificationMethod"`
		Service []struct {
			ID              string `json:"id"`
			Type            string `json:"type"`
			ServiceEndpoint string `json:"serviceEndpoint"`
		} `json:"service"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("did doc not json: %v", err)
	}
	if doc.ID != "did:web:knot.forgesi.org" {
		t.Errorf("id = %q, want did:web:knot.forgesi.org", doc.ID)
	}
	if len(doc.Service) == 0 {
		t.Fatal("no service entry")
	}
	svc := doc.Service[0]
	if svc.Type != "TangledKnot" {
		t.Errorf("service type = %q, want TangledKnot", svc.Type)
	}
	if svc.ServiceEndpoint != "https://knot.forgesi.org" {
		t.Errorf("serviceEndpoint = %q, want https://knot.forgesi.org", svc.ServiceEndpoint)
	}
}

func TestHealth(t *testing.T) {
	h := New(Options{Host: "knot.forgesi.org", Version: "0.1.0"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/xrpc/_health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestDescribeKnot(t *testing.T) {
	h := New(Options{Host: "knot.forgesi.org", Version: "0.1.0"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/xrpc/org.tangled.knot.describeKnot", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var d struct {
		Did          string   `json:"did"`
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("describeKnot not json: %v", err)
	}
	if d.Did != "did:web:knot.forgesi.org" {
		t.Errorf("did = %q, want did:web:knot.forgesi.org", d.Did)
	}
	if d.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", d.Version)
	}
	has := func(want string) bool {
		for _, c := range d.Capabilities {
			if c == want {
				return true
			}
		}
		return false
	}
	// Social COB writes and the firehose are the two capabilities the web
	// client gates on; a knot without them is legacy/read-only.
	if !has("knot-social-writes") {
		t.Errorf("capabilities missing knot-social-writes: %v", d.Capabilities)
	}
	if !has("atproto-firehose") {
		t.Errorf("capabilities missing atproto-firehose: %v", d.Capabilities)
	}
}
