package knot

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

// The orchestrator loop over real git storage: seed a repo with todo.md,
// tick, and verify the assignment commit landed, a run exists, and the
// pull endpoint hands the run out with a working push token.
func TestFleetTickAssignsAndMints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	key, _ := btcec.NewPrivateKey()
	db, _ := sql.Open("sqlite", "file::memory:?cache=shared")
	if err := cob.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Host: "forgesi.org", Version: "0.1.0",
		Auth:      ServiceAuth{V: testVerifier(key)},
		DB:        db,
		MintKey:   key,
		RunnerKey: "runner-secret",
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// seed: create repo, push a todo.md via real git
	boot, _ := atauth.Mint(key, "did:web:forgesi.org:repo:fleet", "did:web:forgesi.org", "", 60000000000, nowPlus(600))
	mustPOST(t, srv.URL, "/xrpc/org.tangled.repo.createRepo", `{"name":"fleet"}`, boot)
	src := t.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "todo.md"), []byte("# todo\n\n- [ ] write the mission statement\n- [x] set up the repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.name=seed", "-c", "user.email=seed@knot", "commit", "-q", "-m", "seed")
	git("-c", "http.extraHeader=Authorization: Bearer "+boot, "push", srv.URL+"/did:web:forgesi.org:repo:fleet", "main")

	// tick (cron path)
	h.TickInternal()

	// the assignment commit landed on main
	gr := gitrepo.Repo{Be: h.be}
	content, err := gr.ReadFileAt("", "todo.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "@agent-1 #in-progress") {
		t.Fatalf("todo.md after tick:\n%s", content)
	}

	// the run is queued; pull it with the runner key
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/xrpc/org.tangled.fleet.nextRun", nil)
	req.Header.Set("Authorization", "Bearer runner-secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("nextRun: %v %v", err, res)
	}
	var run RunRequest
	_ = json.NewDecoder(res.Body).Decode(&run)
	_ = res.Body.Close()
	if run.TodoTitle != "write the mission statement" || run.Branch == "" || run.Token == "" {
		t.Fatalf("run = %+v", run)
	}

	// runDone opens the proposal
	done := `{"runId":"` + run.RunID + `","status":"done","summary":"write the mission statement","branch":"` + run.Branch + `","base":"` + baseOf(t, gr) + `","head":"` + baseOf(t, gr) + `"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/xrpc/org.tangled.fleet.runDone", strings.NewReader(done))
	req.Header.Set("Authorization", "Bearer runner-secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("runDone: %v %v", err, res)
	}
	_ = res.Body.Close()

	// ticket + patch records exist
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:fleet&collection=org.tangled.review.patch", nil))
	if !strings.Contains(w.Body.String(), "org.tangled.review.patch") {
		t.Errorf("no patch record: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:fleet&collection=org.tangled.track.ticket", nil))
	if !strings.Contains(w.Body.String(), "mission statement") {
		t.Errorf("no ticket: %s", w.Body.String())
	}
}

func mustPOST(t *testing.T, base, path, body, token string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("POST %s: %v %v", path, err, res)
	}
	_ = res.Body.Close()
}

func baseOf(t *testing.T, gr gitrepo.Repo) string {
	t.Helper()
	oid, err := gr.Head()
	if err != nil {
		t.Fatal(err)
	}
	return oid
}
