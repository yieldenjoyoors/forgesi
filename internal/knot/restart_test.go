package knot

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// Two Knot instances over ONE database = isolate restart. Every write the
// first served must be visible to the second: this is the multi-isolate
// contract wrangler dev taught us in the live smoke.

func TestKnotStateSurvivesIsolateRestart(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}

	mk := func() http.Handler {
		return New(Options{Host: "knot.forgesi.org", Version: "0.1.0",
			Auth: DevAuthorizer{DID: "did:plc:tester"}, DB: db})
	}
	post := func(h http.Handler, path, body string) map[string]any {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer dev")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var m map[string]any
		_ = json.NewDecoder(w.Body).Decode(&m)
		m["_status"] = w.Code
		return m
	}
	get := func(h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var m map[string]any
		_ = json.NewDecoder(w.Body).Decode(&m)
		return w, m
	}

	// "isolate 1"
	h1 := mk()
	if r := post(h1, "/xrpc/org.tangled.repo.createRepo", `{"name":"persist"}`); r["_status"] != 200 {
		t.Fatalf("createRepo: %v", r)
	}
	w1 := post(h1, "/xrpc/com.atproto.repo.createRecord",
		`{"repo":"did:web:knot.forgesi.org:repo:persist","collection":"org.tangled.track.ticket","record":{"$type":"org.tangled.track.ticket","title":{"$type":"org.tangled.markup.markdown#inline","text":"survives restarts"},"state":"open","createdAt":"2026-10-31T12:00:00.000Z"}}`)
	if w1["_status"] != 200 {
		t.Fatalf("createRecord: %v", w1)
	}

	// "isolate 2" — same D1, fresh process state
	h2 := mk()
	rec, body := get(h2, "/xrpc/com.atproto.repo.listRecords?repo=did:web:knot.forgesi.org:repo:persist&collection=org.tangled.track.ticket")
	if rec.Code != 200 {
		t.Fatalf("listRecords status = %d body=%s", rec.Code, rec.Body.String())
	}
	records := body["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("records after restart = %d, want 1", len(records))
	}
	r0 := records[0].(map[string]any)
	if r0["cid"] != w1["cid"] {
		t.Errorf("cid drifted: %v vs %v", r0["cid"], w1["cid"])
	}
	title := r0["value"].(map[string]any)["title"].(map[string]any)
	if title["text"] != "survives restarts" {
		t.Errorf("title = %v", title)
	}

	// listRepos sees it too
	_, lr := get(h2, "/xrpc/org.tangled.repo.listRepos")
	repos := lr["repos"].([]any)
	if len(repos) != 1 || repos[0].(map[string]any)["name"] != "persist" {
		t.Errorf("listRepos = %v", repos)
	}
}
