package knot

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	_ "github.com/glebarez/go-sqlite"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/cob"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
)

// The FULL fleet loop with the GO AGENT BINARY: seed -> tick -> the agent
// process pulls, clones natively, a stub LLM writes a file, it pushes
// natively, and the proposal lands as ticket + patch.
func TestFleetGoAgentEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	key, _ := btcec.NewPrivateKey()
	db, _ := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "test.db")))
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

	// stub LLM: deterministic single-step completion
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"done":true,"summary":"wrote AGENT.md","files":{"AGENT.md":"# Agent\nproduced by the go fleet agent\n"}}`
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, mustJSONString(content))
	}))
	t.Cleanup(llm.Close)

	// build the agent binary once
	agentBin := filepath.Join(t.TempDir(), "agent")
	build := exec.Command("go", "build", "-o", agentBin, "github.com/yieldenjoyoors/forgesi/runner/agent")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}

	// seed the repo
	boot, _ := atauth.Mint(key, "did:web:forgesi.org:repo:goagent", "did:web:forgesi.org", "", time.Hour, time.Now())
	mustPOST(t, srv.URL, "/xrpc/org.tangled.repo.createRepo", `{"name":"goagent"}`, boot)
	src := t.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "todo.md"), []byte("# todo\n\n- [ ] document the agent protocol in AGENT.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.name=s", "-c", "user.email=s@k", "commit", "-q", "-m", "seed")
	git("-c", "http.extraHeader=Authorization: Bearer "+boot, "push", srv.URL+"/did:web:forgesi.org:repo:goagent", "main")

	// tick + run the agent binary
	h.TickInternal()
	cmd := exec.Command(agentBin)
	cmd.Env = append(os.Environ(),
		"KNOT="+srv.URL,
		"RUNNER_KEY=rk",
		"LLM_URL="+llm.URL+"/chat",
		"LLM_KEY=stub",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("agent: %v\n%s", err, out)
	}

	// the proposal exists and the branch carries the file
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:goagent&collection=org.tangled.review.patch", nil))
	if !strings.Contains(w.Body.String(), "org.tangled.review.patch") {
		t.Fatalf("no patch record: %s", w.Body.String())
	}
	gr := gitrepo.Repo{Be: h.be, RefPrefix: "git/did:web:forgesi.org:repo:goagent/"}
	branch := ""
	if err := db.QueryRow(`SELECT branch FROM knot_runs LIMIT 1`).Scan(&branch); err != nil || branch == "" {
		t.Fatalf("no run branch: %v", err)
	}
	branchHead, ok, _ := h.be.RefHead("git/did:web:forgesi.org:repo:goagent/refs/heads/" + branch)
	if !ok {
		t.Fatalf("branch %s missing", branch)
	}
	files, err := gr.Materialize(branchHead)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["AGENT.md"]), "go fleet agent") {
		t.Errorf("AGENT.md = %q", files["AGENT.md"])
	}
	// the ticket title mentions the todo
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/xrpc/com.atproto.repo.listRecords?repo=did:web:forgesi.org:repo:goagent&collection=org.tangled.track.ticket", nil))
	if !strings.Contains(w.Body.String(), "document the agent protocol") {
		t.Errorf("ticket missing todo title: %s", w.Body.String())
	}
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
