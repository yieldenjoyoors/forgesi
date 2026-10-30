package knot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// M1.5: the record surface over HTTP — repo creation and COB writes. Auth is
// a pluggable seam; tests run the dev authorizer (fixed identity). The
// ES256K service-auth verifier replaces it without touching handlers.

func newTestKnot(t *testing.T) (*Knot, *httptest.Server) {
	t.Helper()
	h := New(Options{Host: "knot.forgesi.org", Version: "0.1.0",
		Auth: DevAuthorizer{DID: "did:plc:tester"}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv
}

func TestCreateRepoAndWriteTicket(t *testing.T) {
	_, srv := newTestKnot(t)

	// create a repo
	body := `{"name":"demo","defaultBranch":"main"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.repo.createRepo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dev")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createRepo status = %d", resp.StatusCode)
	}
	var created struct {
		Did string `json:"did"`
		Key string `json:"key"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if created.Did != "did:web:knot.forgesi.org:repo:demo" {
		t.Errorf("repo did = %q", created.Did)
	}

	// write a ticket (COB record) into it
	body = `{"repo":"did:web:knot.forgesi.org:repo:demo","collection":"org.tangled.track.ticket","record":{"$type":"org.tangled.track.ticket","title":{"$type":"org.tangled.markup.markdown#inline","text":"fix the login loop"},"state":"open","createdAt":"2026-10-31T12:00:00.000Z"}}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/com.atproto.repo.createRecord", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dev")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createRecord status = %d", resp.StatusCode)
	}
	var wrote struct {
		URI         string `json:"uri"`
		CID         string `json:"cid"`
		XTnglEditor string `json:"x-tngl-editor"`
		XTnglChange string `json:"x-tngl-change"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&wrote)
	if !strings.HasPrefix(wrote.URI, "at://did:web:knot.forgesi.org:repo:demo/org.tangled.track.ticket/") {
		t.Errorf("uri = %q", wrote.URI)
	}
	if wrote.CID != "bafyreihvobkx3wdfi4xfcmq6mw4xljuqxawd5q5qy4kvudcz4vyyj6diti" {
		t.Errorf("cid = %q (canonical dag-cbor cid expected)", wrote.CID)
	}
	if wrote.XTnglEditor != "did:plc:tester" {
		t.Errorf("x-tngl-editor = %q", wrote.XTnglEditor)
	}
	if len(wrote.XTnglChange) != 40 {
		t.Errorf("x-tngl-change = %q (want git sha)", wrote.XTnglChange)
	}

	// read it back anonymously
	rkey := wrote.URI[strings.LastIndex(wrote.URI, "/")+1:]
	get, err := http.Get(srv.URL + "/xrpc/com.atproto.repo.getRecord?repo=did:web:knot.forgesi.org:repo:demo&collection=org.tangled.track.ticket&rkey=" + rkey)
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK {
		t.Fatalf("getRecord status = %d", get.StatusCode)
	}
	var got struct {
		URI   string         `json:"uri"`
		CID   string         `json:"cid"`
		Value map[string]any `json:"value"`
	}
	_ = json.NewDecoder(get.Body).Decode(&got)
	if got.CID != wrote.CID {
		t.Errorf("get cid = %q, want %q", got.CID, wrote.CID)
	}
	title, _ := got.Value["title"].(map[string]any)
	if title["text"] != "fix the login loop" {
		t.Errorf("value.title = %v", got.Value["title"])
	}
}

func TestPutRecordSwapConflictOverHTTP(t *testing.T) {
	_, srv := newTestKnot(t)
	post := func(path, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer dev")
		resp, _ := http.DefaultClient.Do(req)
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp, m
	}

	post("/xrpc/org.tangled.repo.createRepo", `{"name":"demo2"}`)
	_, w := post("/xrpc/com.atproto.repo.createRecord", `{"repo":"did:web:knot.forgesi.org:repo:demo2","collection":"x.y.z","rkey":"3jzfcijpj2z2a","record":{"$type":"x.y.z","state":"open","createdAt":"2026-10-31T12:00:00.000Z"}}`)

	// stale swap → 409 with error InvalidSwap (the atproto error envelope)
	resp, _ := post("/xrpc/com.atproto.repo.putRecord", `{"repo":"did:web:knot.forgesi.org:repo:demo2","collection":"x.y.z","rkey":"3jzfcijpj2z2a","swapRecord":"bafyreiwrong","record":{"$type":"x.y.z","state":"closed","createdAt":"2026-10-31T12:00:00.000Z"}}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("stale put status = %d, want 409", resp.StatusCode)
	}

	// correct swap → 200 with a new cid and change
	resp, w2 := post("/xrpc/com.atproto.repo.putRecord", `{"repo":"did:web:knot.forgesi.org:repo:demo2","collection":"x.y.z","rkey":"3jzfcijpj2z2a","swapRecord":"`+w["cid"].(string)+`","record":{"$type":"x.y.z","state":"closed","createdAt":"2026-10-31T12:00:00.000Z"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	if w2["cid"] == w["cid"] {
		t.Error("put must produce a new cid")
	}

	// version history: two versions, oldest first, changes are git shas
	get, _ := http.Get(srv.URL + "/xrpc/org.tangled.repo.listRecordVersions?uri=at://did:web:knot.forgesi.org:repo:demo2/x.y.z/3jzfcijpj2z2a&limit=50")
	var vers struct {
		Versions []struct {
			CID      string `json:"cid"`
			ChangeID string `json:"changeId"`
		} `json:"versions"`
	}
	_ = json.NewDecoder(get.Body).Decode(&vers)
	if len(vers.Versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(vers.Versions))
	}
	if vers.Versions[0].CID != w["cid"] || vers.Versions[1].CID != w2["cid"] {
		t.Errorf("version order wrong: %+v", vers.Versions)
	}
	if len(vers.Versions[1].ChangeID) != 40 {
		t.Errorf("changeId %q not a git sha", vers.Versions[1].ChangeID)
	}
}

func TestWritesRequireAuth(t *testing.T) {
	_, srv := newTestKnot(t)
	resp, err := http.Post(srv.URL+"/xrpc/com.atproto.repo.createRecord",
		"application/json", strings.NewReader(`{"repo":"did:web:x:repo:nope","collection":"x.y.z","record":{"$type":"x.y.z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated write status = %d, want 401", resp.StatusCode)
	}
}
