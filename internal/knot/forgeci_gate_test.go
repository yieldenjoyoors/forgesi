package knot

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
)

// The forgeci.json gate: fleet-identity merges follow the repo's review
// policy; humans merge anything. Policy-cleared proposals auto-merge
// after a clear machine review; human-required ones wait.

const gateHost = "forgesi.org"

func gateKnot(t *testing.T, llm func(system, user string) (string, error)) (*Knot, *btcec.PrivateKey) {
	t.Helper()
	key, _ := btcec.NewPrivateKey()
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: gateHost, Version: "0.1.0",
		Auth: ServiceAuth{V: testVerifier(key)},
		DB:   db, MintKey: key, RunnerKey: "rk",
		ReviewLLM: llm,
	})
	return h, key
}

func gateToken(t *testing.T, key *btcec.PrivateKey, iss, aud string) string {
	t.Helper()
	tok, err := atauth.Mint(key, iss, aud, "", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// gatePost issues an authed POST and returns the recorder.
func gatePost(h *Knot, path, token string, bodyFmt string, args ...any) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fmt.Sprintf(bodyFmt, args...)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// gatePutFile is the write helper: editor iss, aud = repo DID.
func gatePutFile(t *testing.T, h *Knot, key *btcec.PrivateKey, repo, editor, path, content, branch string) {
	t.Helper()
	tok := gateToken(t, key, editor, repo)
	w := gatePost(h, "/xrpc/org.tangled.repo.putFile", tok,
		`{"repo":%q,"path":%q,"content":%q,"branch":%q}`, repo, path, content, branch)
	if w.Code != 200 {
		t.Fatalf("putFile %s: %d %s", path, w.Code, w.Body.String())
	}
}

func gateMerge(t *testing.T, h *Knot, key *btcec.PrivateKey, repo, editor, base, head string) *httptest.ResponseRecorder {
	t.Helper()
	tok := gateToken(t, key, editor, repo)
	return gatePost(h, "/xrpc/org.tangled.git.merge", tok,
		`{"repo":%q,"base":%q,"head":%q}`, repo, base, head)
}

func gateGetFile(t *testing.T, h *Knot, key *btcec.PrivateKey, repo, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	tok := gateToken(t, key, repo, repo)
	req := httptest.NewRequest(http.MethodPost, "/xrpc/org.tangled.repo.getFile",
		strings.NewReader(fmt.Sprintf(`{"repo":%q,"path":%q,"ref":"main"}`, repo, path)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w, w.Body.String()
}

func TestForgeciGateBlocksFleetButNotHumans(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:gate"
	knotDID := "did:web:" + gateHost
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "gate"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}

	// policy: default agent — docs changes clear without a human
	gatePutFile(t, h, key, repo, alice, "forgeci.json", `{"review":{"default":"agent"}}`, "main")
	gatePutFile(t, h, key, repo, alice, "docs/notes.md", "hello", "agent/1")

	// fleet merge of an agent-cleared proposal: allowed
	if w := gateMerge(t, h, key, repo, knotDID, "main", "agent/1"); w.Code != 200 {
		t.Fatalf("fleet merge (policy agent): %d %s", w.Code, w.Body.String())
	}

	// tighten: everything human-required
	gatePutFile(t, h, key, repo, alice, "forgeci.json", `{"review":{"default":"human"}}`, "main")
	gatePutFile(t, h, key, repo, alice, "docs/more.md", "more", "agent/2")

	if w := gateMerge(t, h, key, repo, knotDID, "main", "agent/2"); w.Code != http.StatusForbidden {
		t.Fatalf("fleet merge (policy human): %d %s — want 403", w.Code, w.Body.String())
	}
	// a human merges the same proposal: their merge IS the approval
	if w := gateMerge(t, h, key, repo, alice, "main", "agent/2"); w.Code != 200 {
		t.Fatalf("human merge (policy human): %d %s", w.Code, w.Body.String())
	}

	// absent policy = human required (safe default) — new repo
	repo2 := "did:web:" + gateHost + ":repo:gate2"
	boot2 := gateToken(t, key, repo2, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot2, `{"name":%q}`, "gate2"); w.Code != 200 {
		t.Fatalf("createRepo2: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo2, alice, "README.md", "x", "agent/9")
	if w := gateMerge(t, h, key, repo2, knotDID, "main", "agent/9"); w.Code != http.StatusForbidden {
		t.Fatalf("fleet merge (no policy): %d %s — want 403", w.Code, w.Body.String())
	}
}

func TestForgeciAutoMergeAfterClearReview(t *testing.T) {
	verdicts := make(chan string, 4)
	h, key := gateKnot(t, func(system, user string) (string, error) {
		verdicts <- system
		return "✅ The docs change looks correct and complete.", nil
	})
	repo := "did:web:" + gateHost + ":repo:auto"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "auto"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	// policy: agent review alone clears everything
	gatePutFile(t, h, key, repo, alice, "forgeci.json", `{"review":{"default":"agent"}}`, "main")
	// the plan: one open line — the auto-merge should flip it
	gatePutFile(t, h, key, repo, alice, "todo.md", "# auto\n\n- [ ] write the docs\n", "main")
	// let the putFile's TryRun finish its assignment commit, so the agent
	// branch below forks from the assigned main (as it does in prod).
	time.Sleep(400 * time.Millisecond)

	// the agent branch: adds a docs file
	gatePutFile(t, h, key, repo, alice, "docs/api.md", "the api", "agent/5")

	// resolve real base/head oids for the proposal
	gr := h.gitrepoRepo(repo)
	baseOID, okBase, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
	headOID, okHead, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/agent/5")
	if !okBase || !okHead {
		t.Fatalf("refs missing: base=%v head=%v", okBase, okHead)
	}

	h.openProposal(repo, "agent/5", baseOID, headOID, "adds docs/api.md", "write the docs")
	<-verdicts // the review ran; the auto-merge follows in the same goroutine

	// diagnostics: the comments tell the whole story on failure
	dumpComments := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.feed.comment", nil))
		return w.Body.String()
	}

	// docs/api.md merged onto main and the todo line flipped
	var todoBody string
	ok := false
	for i := 0; i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
		w, body := gateGetFile(t, h, key, repo, "todo.md")
		todoBody = body
		if w.Code == 200 && strings.Contains(body, "[x] write the docs") {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("todo line not flipped after auto-merge; todo.md=%s comments=%s", todoBody, dumpComments())
	}
	w, body := gateGetFile(t, h, key, repo, "docs/api.md")
	if w.Code != 200 || !strings.Contains(body, "the api") {
		t.Fatalf("docs/api.md not merged onto main: %d %s", w.Code, body)
	}

	// the merge note landed as a comment
	sawNote := false
	for i := 0; i < 50 && !sawNote; i++ {
		time.Sleep(20 * time.Millisecond)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.feed.comment", nil))
		sawNote = strings.Contains(w.Body.String(), "auto-merged")
	}
	if !sawNote {
		t.Fatalf("auto-merge comment missing")
	}
}

// listFiles: the whole tree at a ref, sorted — the file browser's feed.
func TestListFiles(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:tree"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "tree"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "docs/readme.md", "docs", "main")
	gatePutFile(t, h, key, repo, alice, "todo.md", "- [ ] x\n", "main")

	tok := gateToken(t, key, repo, repo)
	w := gatePost(h, "/xrpc/org.tangled.repo.listFiles", tok, `{"repo":%q}`, repo)
	if w.Code != 200 {
		t.Fatalf("listFiles: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"docs/readme.md"`) || !strings.Contains(w.Body.String(), `"todo.md"`) {
		t.Fatalf("files missing: %s", w.Body.String())
	}
	if strings.Index(w.Body.String(), "docs/readme.md") > strings.Index(w.Body.String(), "todo.md") {
		t.Fatalf("paths not sorted: %s", w.Body.String())
	}
	// unknown ref: 404
	w = gatePost(h, "/xrpc/org.tangled.repo.listFiles", tok, `{"repo":%q,"ref":"nope"}`, repo)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bad ref: %d %s", w.Code, w.Body.String())
	}
}

// createRepo writes org.tangled.repo.manifest/self — the knot-side half
// of the discovery claim (the owner's PDS carries the other half).
func TestCreateRepoWritesManifest(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:manifest"
	owner := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q,"owner":%q}`, "manifest", owner); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.repo.manifest", nil))
	if w.Code != 200 {
		t.Fatalf("listRecords manifest: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"owner":"`+owner+`"`) ||
		!strings.Contains(body, `"name":"manifest"`) ||
		!strings.Contains(body, "org.tangled.repo.manifest") {
		t.Fatalf("manifest wrong: %s", body)
	}
}

// patch rounds: a resubmission of the same todo line appends a patch to
// the open ticket instead of opening a duplicate (stacked-PR model).
func TestOpenProposalAppendsPatchRounds(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:rounds"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "rounds"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "todo.md", "- [ ] fix the thing\n", "main")
	time.Sleep(300 * time.Millisecond) // let the assignment land
	gatePutFile(t, h, key, repo, alice, "fix.txt", "attempt one", "agent/1")

	gr := h.gitrepoRepo(repo)
	baseOID, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
	head1, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/agent/1")

	h.openProposal(repo, "agent/1", baseOID, head1, "first attempt", "fix the thing")

	// second attempt: new head on the same branch, same todo title
	gatePutFile(t, h, key, repo, alice, "fix.txt", "attempt two", "agent/1")
	head2, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/agent/1")
	h.openProposal(repo, "agent/1", baseOID, head2, "second attempt", "fix the thing")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.track.ticket&limit=50", nil))
	tickets := strings.Count(w.Body.String(), `"org.tangled.track.ticket"`)
	if tickets != 1 {
		t.Fatalf("tickets = %d, want 1 (no duplicates across rounds)", tickets)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.review.patch&limit=50", nil))
	patches := strings.Count(w.Body.String(), `"org.tangled.review.patch"`)
	if patches != 2 {
		t.Fatalf("patches = %d, want 2 (one per round)", patches)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=org.tangled.feed.comment&limit=50", nil))
	if !strings.Contains(w.Body.String(), "round 2") {
		t.Fatalf("round-2 comment missing: %s", w.Body.String())
	}
}

// runDone records the agent's CI-lite checks; fleet.runs surfaces them.
func TestRunDoneStoresChecks(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:cirun"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "cirun"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, "did:web:alice.example", "todo.md", "- [ ] do ci\n", "main")
	time.Sleep(300 * time.Millisecond) // TryRun assigns

	// find the running run
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/org.tangled.fleet.runs?repo="+repo, nil))
	var runs struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	_ = json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&runs)
	if len(runs.Runs) == 0 {
		t.Fatalf("no run assigned from todo.md")
	}
	runID := runs.Runs[0].ID

	// the agent reports done + checks (no head: no proposal); runDone's
	// service-auth path is knot-audience
	tok := gateToken(t, key, repo, "did:web:"+gateHost)
	w = gatePost(h, "/xrpc/org.tangled.fleet.runDone", tok, `{"runId":%q,"status":"done","summary":"ok","checks":[{"name":"go-syntax:main.go","ok":true},{"name":"json:cfg.json","ok":false,"detail":"invalid JSON"}]}`, runID)
	if w.Code != 200 {
		t.Fatalf("runDone: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/org.tangled.fleet.runs?repo="+repo, nil))
	var after struct {
		Runs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Checks []struct {
				Name string `json:"name"`
				OK   bool   `json:"ok"`
			} `json:"checks"`
		} `json:"runs"`
	}
	_ = json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&after)
	if len(after.Runs) == 0 || len(after.Runs[0].Checks) != 2 {
		t.Fatalf("checks not surfaced: %s", w.Body.String())
	}
	if after.Runs[0].Checks[1].OK {
		t.Fatalf("failing check lost its ok=false")
	}
}

// the diff endpoint: changed paths only, line-level LCS rows.
func TestRepoDiff(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:diffy"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "diffy"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "readme.md", "line one\nline two\nline three\n", "main")
	baseOID := func() string {
		gr := h.gitrepoRepo(repo)
		oid, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
		return oid
	}()
	gatePutFile(t, h, key, repo, alice, "readme.md", "line one\nline TWO changed\nline three\nline four\n", "agent/1")
	gatePutFile(t, h, key, repo, alice, "new.go", "package x\n", "agent/1")
	gr := h.gitrepoRepo(repo)
	headOID, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/agent/1")

	tok := gateToken(t, key, repo, repo)
	w := gatePost(h, "/xrpc/org.tangled.repo.diff", tok,
		`{"repo":%q,"base":%q,"head":%q}`, repo, baseOID, headOID)
	if w.Code != 200 {
		t.Fatalf("diff: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"path":"new.go"`, `"status":"added"`, `"status":"modified"`,
		`{"t":"-","s":"line two"}`, `{"t":"+","s":"line TWO changed"}`, `{"t":"+","s":"line four"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("diff missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, `"status":"removed"`) {
		t.Errorf("unexpected removal: %s", body)
	}

	// bad input: branch names are rejected, oids required
	w = gatePost(h, "/xrpc/org.tangled.repo.diff", tok, `{"repo":%q,"base":"main","head":"agent/1"}`, repo)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for branch names, got %d", w.Code)
	}
}

// commitInfo: message, parents, and the diff against the first parent.
func TestCommitInfo(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:cinfo"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "cinfo"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "a.txt", "one\n", "main")
	gr := h.gitrepoRepo(repo)
	first, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
	gatePutFile(t, h, key, repo, alice, "a.txt", "one\ntwo\n", "main")
	second, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")

	tok := gateToken(t, key, repo, repo)
	w := gatePost(h, "/xrpc/org.tangled.repo.commitInfo", tok, `{"repo":%q,"oid":%q}`, repo, second)
	if w.Code != 200 {
		t.Fatalf("commitInfo: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{first, `"message":"Update a.txt"`, `{"t":"+","s":"two"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("commitInfo missing %s in %s", want, body)
		}
	}

	// unknown oid: 404
	w = gatePost(h, "/xrpc/org.tangled.repo.commitInfo", tok, `{"repo":%q,"oid":%q}`, repo, strings.Repeat("a", 40))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// putFile delete: the tree entry drops, listFiles stops showing it.
func TestPutFileDelete(t *testing.T) {
	h, key := gateKnot(t, nil)
	repo := "did:web:" + gateHost + ":repo:del"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "del"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "doomed.md", "bye\n", "main")
	gatePutFile(t, h, key, repo, alice, "keep.md", "hi\n", "main")

	tok := gateToken(t, key, repo, repo)
	w := gatePost(h, "/xrpc/org.tangled.repo.putFile", tok,
		`{"repo":%q,"path":"doomed.md","delete":true,"branch":"main"}`, repo)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo="+repo+"&collection=x", nil))
	w = gatePost(h, "/xrpc/org.tangled.repo.listFiles", tok, `{"repo":%q}`, repo)
	if strings.Contains(w.Body.String(), "doomed.md") {
		t.Fatalf("doomed.md still listed: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "keep.md") {
		t.Fatalf("keep.md vanished: %s", w.Body.String())
	}
}

func TestForgeciBlockVerdictHoldsAutoMerge(t *testing.T) {
	verdicts := make(chan string, 4)
	h, key := gateKnot(t, func(system, user string) (string, error) {
		verdicts <- system
		return "BLOCK: the change deletes the test suite.", nil
	})
	repo := "did:web:" + gateHost + ":repo:blocked"
	alice := "did:web:alice.example"

	boot := gateToken(t, key, repo, "did:web:"+gateHost)
	if w := gatePost(h, "/xrpc/org.tangled.repo.createRepo", boot, `{"name":%q}`, "blocked"); w.Code != 200 {
		t.Fatalf("createRepo: %d %s", w.Code, w.Body.String())
	}
	gatePutFile(t, h, key, repo, alice, "forgeci.json", `{"review":{"default":"agent"}}`, "main")
	gatePutFile(t, h, key, repo, alice, "docs/x.md", "x", "agent/1")

	gr := h.gitrepoRepo(repo)
	baseOID, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
	headOID, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/agent/1")
	mainBefore, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")

	h.openProposal(repo, "agent/1", baseOID, headOID, "removes the tests", "remove the tests")
	<-verdicts

	time.Sleep(300 * time.Millisecond) // let any (wrong) auto-merge land
	mainAfter, _, _ := gr.Be.RefHead(gr.RefPrefix + "refs/heads/main")
	if mainBefore != mainAfter {
		t.Fatalf("BLOCK verdict must not auto-merge: %s -> %s", mainBefore, mainAfter)
	}
}
