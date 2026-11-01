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
