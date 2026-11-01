package knot

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

type rawRes struct {
	code int
	body string
}

func mustPOSTRaw(t *testing.T, base, path, body, token string) rawRes {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return rawRes{code: res.StatusCode, body: string(b)}
}

// The repo action surface over HTTP: putFile/getFile with optimistic
// concurrency, and merge (fast-forward + three-way). Real storage, real
// routes, minted tokens (aud = repo DID, the same rule the console and
// agents use).
func TestRepoActionsFileAndMerge(t *testing.T) {
	key, _ := btcec.NewPrivateKey()
	db, _ := sql.Open("sqlite", "file:"+t.TempDir()+"/ra.db")
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth: ServiceAuth{V: testVerifier(key)},
		DB:   db,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	boot, _ := atauth.Mint(key, "did:web:forgesi.org:repo:actions", "did:web:forgesi.org", "", 60000000000, nowPlus(600))
	mustPOST(t, srv.URL, "/xrpc/org.tangled.repo.createRepo", `{"name":"actions"}`, boot)
	repo := "did:web:forgesi.org:repo:actions"
	// editor token: aud = repo DID (the record-write rule)
	editor, _ := atauth.Mint(key, "did:plc:human", repo, "", 60000000000, nowPlus(600))
	boot = editor

	// getFile on an empty repo: main doesn't exist yet
	res := mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.getFile", `{"repo":"`+repo+`","path":"todo.md"}`, boot)
	if res.code != http.StatusNotFound {
		t.Fatalf("getFile before first commit: %d %s", res.code, res.body)
	}

	// putFile creates main
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"todo.md","content":"- [ ] first\n"}`, boot)
	if res.code != http.StatusOK {
		t.Fatalf("putFile: %d %s", res.code, res.body)
	}
	var put struct {
		Commit, Head string
	}
	_ = json.Unmarshal([]byte(res.body), &put)

	// getFile reads it back with the head
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.getFile",
		`{"repo":"`+repo+`","path":"todo.md"}`, boot)
	if res.code != 200 || !strings.Contains(res.body, "first") || !strings.Contains(res.body, put.Head[:8]) {
		t.Fatalf("getFile round-trip: %d %s", res.code, res.body)
	}

	// stale baseHead -> 409 HeadMoved carrying the fresh head
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"todo.md","content":"- [ ] stale\n","baseHead":"deadbeef"}`, boot)
	if res.code != http.StatusConflict {
		t.Fatalf("stale putFile: %d %s", res.code, res.body)
	}

	// merge an agent branch into main (fast-forward): agent branched from
	// main HEAD and added a file
	seed := mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.getFile",
		`{"repo":"`+repo+`","path":"todo.md"}`, boot)
	var gf struct {
		Head string
	}
	_ = json.Unmarshal([]byte(seed.body), &gf)

	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"CHANGELOG.md","content":"v1\n","branch":"agent/1","baseHead":"`+gf.Head+`"}`, boot)
	if res.code != 200 {
		t.Fatalf("putFile on branch: %d %s", res.code, res.body)
	}
	var bput struct {
		Head string
	}
	_ = json.Unmarshal([]byte(res.body), &bput)

	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.git.merge",
		`{"repo":"`+repo+`","head":"agent/1"}`, boot)
	if res.code != 200 {
		t.Fatalf("merge: %d %s", res.code, res.body)
	}
	var mg struct {
		Commit      string
		FastForward bool
	}
	_ = json.Unmarshal([]byte(res.body), &mg)
	if !mg.FastForward || mg.Commit != bput.Head {
		t.Fatalf("merge result: %s", res.body)
	}

	// merged content is on main now
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.getFile",
		`{"repo":"`+repo+`","path":"CHANGELOG.md"}`, boot)
	if res.code != 200 || !strings.Contains(res.body, "v1") {
		t.Fatalf("merged file on main: %d %s", res.code, res.body)
	}

	// unauthenticated write fails closed
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"todo.md","content":"nope"}`, "faketoken")
	if res.code != http.StatusUnauthorized {
		t.Fatalf("unauth putFile: %d", res.code)
	}
}

// The full fleet loop over HTTP: seed todo.md via putFile, tick, pull the
// run, push a branch, runDone -> proposal, merge with the ticket rkey ->
// the todo line flips to [x]. Then retry requeues a failed run.
func TestFleetLoopMergeCompletesTodo(t *testing.T) {
	key, _ := btcec.NewPrivateKey()
	db, _ := sql.Open("sqlite", "file:"+t.TempDir()+"/loop.db")
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth:      ServiceAuth{V: testVerifier(key)},
		DB:        db,
		MintKey:   key,
		RunnerKey: "rk",
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	boot, _ := atauth.Mint(key, "did:web:forgesi.org:repo:loop", "did:web:forgesi.org", "", 60000000000, nowPlus(600))
	mustPOST(t, srv.URL, "/xrpc/org.tangled.repo.createRepo", `{"name":"loop"}`, boot)
	repo := "did:web:forgesi.org:repo:loop"
	editor, _ := atauth.Mint(key, "did:web:forgesi.org", repo, "", 60000000000, nowPlus(600))

	// the work queue
	res := mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"todo.md","content":"- [ ] ship the loop test\n- [ ] never touched\n"}`, editor)
	if res.code != 200 {
		t.Fatalf("seed todo: %d %s", res.code, res.body)
	}

	// tick assigns the first item (knot-audience token)
	tickTok, _ := atauth.Mint(key, "did:web:forgesi.org", "did:web:forgesi.org", "", 60000000000, nowPlus(600))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.fleet.tick", nil)
	req.Header.Set("Authorization", "Bearer "+tickTok)
	tres, err := http.DefaultClient.Do(req)
	if err != nil || tres.StatusCode != 200 {
		t.Fatalf("tick: %v %v", err, tres)
	}
	_ = tres.Body.Close()

	// pull the run
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/xrpc/org.tangled.fleet.nextRun", nil)
	req.Header.Set("Authorization", "Bearer rk")
	nres, err := http.DefaultClient.Do(req)
	if err != nil || nres.StatusCode != 200 {
		t.Fatalf("nextRun: %v %v", err, nres)
	}
	var run RunRequest
	_ = json.NewDecoder(nres.Body).Decode(&run)
	_ = nres.Body.Close()

	// agent branch with a change
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.putFile",
		`{"repo":"`+repo+`","path":"loop.txt","content":"done\n","branch":"`+run.Branch+`"}`, editor)
	if res.code != 200 {
		t.Fatalf("agent branch: %d %s", res.code, res.body)
	}
	var bput struct {
		Head string
	}
	_ = json.Unmarshal([]byte(res.body), &bput)

	// runDone -> proposal
	done := `{"runId":"` + run.RunID + `","status":"done","summary":"loop test","branch":"` + run.Branch + `","base":"","head":"` + bput.Head + `"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.fleet.runDone", strings.NewReader(done))
	req.Header.Set("Authorization", "Bearer rk")
	dres, err := http.DefaultClient.Do(req)
	if err != nil || dres.StatusCode != 200 {
		t.Fatalf("runDone: %v %v", err, dres)
	}
	_ = dres.Body.Close()

	// find the proposal ticket rkey
	lres, _ := http.Get(srv.URL + "/xrpc/com.atproto.repo.listRecords?repo=" + repo + "&collection=org.tangled.track.ticket&limit=50")
	var lr struct {
		Records []struct {
			URI   string `json:"uri"`
			Value struct {
				Title struct {
					Text string `json:"text"`
				} `json:"title"`
			} `json:"value"`
		} `json:"records"`
	}
	_ = json.NewDecoder(lres.Body).Decode(&lr)
	_ = lres.Body.Close()
	var rkey string
	for _, r := range lr.Records {
		if strings.HasPrefix(r.Value.Title.Text, "agent: ship the loop test") {
			rkey = strings.Split(r.URI, "/")[4]
		}
	}
	if rkey == "" {
		t.Fatalf("no proposal ticket found: %+v", lr.Records)
	}

	// merge with the ticket -> todo line flips
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.git.merge",
		`{"repo":"`+repo+`","head":"`+run.Branch+`","ticket":"`+rkey+`"}`, editor)
	if res.code != 200 {
		t.Fatalf("merge: %d %s", res.code, res.body)
	}

	// server truth: the line is [x] on main
	res = mustPOSTRaw(t, srv.URL, "/xrpc/org.tangled.repo.getFile",
		`{"repo":"`+repo+`","path":"todo.md","ref":"main"}`, editor)
	if !strings.Contains(res.body, "[x] ship the loop test") {
		t.Fatalf("todo not completed on merge: %s", res.body)
	}
	if strings.Contains(res.body, "[ ] ship the loop test") {
		t.Fatal("line still open")
	}

	// retry: mark the real run failed, requeue it
	fail := `{"runId":"` + run.RunID + `","status":"failed","summary":"boom"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.fleet.runDone", strings.NewReader(fail))
	req.Header.Set("Authorization", "Bearer rk")
	_, _ = http.DefaultClient.Do(req)
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.fleet.retry", strings.NewReader(`{"runId":"`+run.RunID+`"}`))
	req.Header.Set("Authorization", "Bearer rk")
	rres, err := http.DefaultClient.Do(req)
	if err != nil || rres.StatusCode != 200 {
		t.Fatalf("retry: %v %v", err, rres)
	}
	_ = rres.Body.Close()
	// the retried run is pullable
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/xrpc/org.tangled.fleet.nextRun", nil)
	req.Header.Set("Authorization", "Bearer rk")
	n2, err := http.DefaultClient.Do(req)
	if err != nil || n2.StatusCode != 200 {
		t.Fatalf("nextRun after retry: %v %v", err, n2)
	}
	_ = n2.Body.Close()
}
