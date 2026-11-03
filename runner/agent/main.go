// Command agent is the ForgeSI fleet runner: pull a run, clone the repo
// natively (smart-HTTP v2 over the knot), do the work with an LLM, push
// the branch, report done. Pure Go — the fleet container is a static
// binary in a scratch image; no interpreter, no git binary (this binary
// speaks the git wire protocol itself via internal/packfile).
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/packfile"
	"github.com/yieldenjoyoors/forgesi/internal/packline"
)

var (
	knot      = "http://127.0.0.1:8797"
	runnerKey = ""
	llmURL    = "https://api.z.ai/api/coding/paas/v4/chat/completions"
	llmKey    = ""
	llmModel  = "glm-4.7"
)

// httpClient is the outbound client; the wasm build swaps it for the
// workers fetch-backed transport (runtime_wasm.go).
var httpClient = http.DefaultClient

// getEnv is per-build (os.Getenv on the CLI, worker bindings in wasm);
// the runtime files assign it in init.
var getEnv func(string) string

func env(k, d string) string {
	if v := getEnv(k); v != "" {
		return v
	}
	return d
}

// initConfig resolves the configuration lazily — the wasm build can only
// read worker bindings once the runtime is up, not during package var init.
func initConfig() {
	knot = env("KNOT", knot)
	runnerKey = getEnv("RUNNER_KEY")
	llmURL = env("LLM_URL", llmURL)
	llmKey = getEnv("LLM_KEY")
	llmModel = env("LLM_MODEL", llmModel)
}

var configOnce sync.Once

func ensureConfig() { configOnce.Do(initConfig) }

func log(a ...any) { fmt.Fprintln(os.Stderr, a...) }

// memBackend is the agent's local object store for the clone it works on.
type memBackend struct{ objects map[string][]byte }

func newMemBackend() *memBackend { return &memBackend{objects: map[string][]byte{}} }
func (m *memBackend) PutObject(c []byte) (string, error) {
	oid := packfile.OID(c)
	m.objects[oid] = c
	return oid, nil
}
func (m *memBackend) GetObject(oid string) ([]byte, error) {
	c, ok := m.objects[oid]
	if !ok {
		return nil, fmt.Errorf("object %s missing", oid)
	}
	return c, nil
}
func (m *memBackend) RefHead(ref string) (string, bool, error) { return "", false, nil }
func (m *memBackend) SetRef(ref, oid string) error             { return nil }
func (m *memBackend) ListRefs(prefix, after string, limit int) ([]string, error) {
	return nil, nil
}

// RunRequest is one fleet run's payload.
type RunRequest struct {
	RunID     string `json:"runId"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	TodoTitle string `json:"todoTitle"`
	Token     string `json:"token"`
}

// postForm is xrpc for the git wire: POST a framed body, return the raw
// response (no JSON decoding).
func postForm(url string, body io.Reader) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	return httpClient.Do(req)
}

func xrpc(method, path string, body any, token string, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, knot+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// clone fetches the repo head via smart-HTTP v2 and materializes files.
func clone(repo string) (gitrepo.Repo, string, map[string][]byte, error) {
	be := newMemBackend()
	gr := gitrepo.Repo{Be: be}
	base := knot + "/" + repo

	// ls-refs → default branch head
	var cmd bytes.Buffer
	cmd.Write(packline.Pkt("command=ls-refs\n"))
	cmd.Write(packline.Pkt("agent=forgesi-agent/0.1\n"))
	cmd.Write(packline.DelimPkt())
	cmd.Write(packline.Pkt("symrefs\n"))
	cmd.Write(packline.FlushPkt())
	res, err := postForm(base+"/git-upload-pack", &cmd)
	if err != nil {
		return gr, "", nil, err
	}
	head, err := lsRefsFirst(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return gr, "", nil, err
	}
	if head == "" {
		return gr, "", nil, fmt.Errorf("clone: no head ref found")
	}

	// fetch: want <head>, done
	var req bytes.Buffer
	req.Write(packline.Pkt("command=fetch\n"))
	req.Write(packline.Pkt("agent=forgesi-agent/0.1\n"))
	req.Write(packline.DelimPkt())
	req.Write(packline.Pkt("want " + head + "\n"))
	req.Write(packline.Pkt("done\n"))
	req.Write(packline.FlushPkt())
	res2, err := postForm(base+"/git-upload-pack", &req)
	if err != nil {
		return gr, "", nil, err
	}
	if res2.StatusCode >= 300 {
		b, _ := io.ReadAll(res2.Body)
		_ = res2.Body.Close()
		return gr, "", nil, fmt.Errorf("fetch: %d %s", res2.StatusCode, b)
	}
	// v2 response: sections; the packfile section header then sideband ch1
	// chunks; with no haves there is no acknowledgments section.
	pr := packline.NewReader(res2.Body)
	var pack bytes.Buffer
	inPack := false
	for {
		payload, kind, err := pr.Next()
		if err != nil || kind == "flush" {
			if kind == "flush" && inPack {
				break
			}
			if err != nil {
				break
			}
			continue
		}
		if kind == "data" {
			if !inPack && strings.HasPrefix(string(payload), "packfile") {
				inPack = true
				continue
			}
			if inPack && len(payload) > 0 && payload[0] == 1 {
				pack.Write(payload[1:])
			}
		}
	}
	if pack.Len() == 0 {
		return gr, "", nil, fmt.Errorf("clone: empty pack (packfile section never found)")
	}
	objs, err := packfile.Decode(bytes.NewReader(pack.Bytes()))
	if err != nil {
		return gr, "", nil, fmt.Errorf("clone: decode pack: %w", err)
	}
	for _, o := range objs {
		if _, err := be.PutObject(packfile.GitObject(packfile.TypeName(o.Type), o.Data)); err != nil {
			return gr, "", nil, err
		}
	}
	files, err := gr.Materialize(head)
	if err != nil {
		return gr, "", nil, err
	}
	return gr, head, files, nil
}

// lsRefsFirst parses a v2 ls-refs response and returns the default head.
func lsRefsFirst(r io.Reader) (string, error) {
	pr := packline.NewReader(r)
	for {
		payload, kind, err := pr.Next()
		if err != nil || kind == "flush" || kind == "delim" {
			if err != nil {
				return "", err
			}
			continue
		}
		line := string(bytes.TrimRight(payload, "\n"))
		line = strings.SplitN(line, "\x00", 2)[0]
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "HEAD" {
			return f[0], nil
		}
		if len(f) >= 2 && f[1] == "refs/heads/main" {
			return f[0], nil
		}
	}
}

// push encodes the reachable objects and runs receive-pack with the token.
func push(repo, branch, newCommit, token string, gr gitrepo.Repo) error {
	objs, err := gr.WalkReachable([]string{newCommit})
	if err != nil {
		return err
	}
	pack, err := packfile.Encode(objs)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	oldOID := "0000000000000000000000000000000000000000"
	body.Write(packline.EncodePkt([]byte(oldOID + " " + newCommit + " refs/heads/" + branch + "\x00 report-status\n")))
	body.Write(packline.FlushPkt())
	body.Write(pack)
	req, _ := http.NewRequest(http.MethodPost, knot+"/"+repo+"/git-receive-pack", &body)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	respBody, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("push: %d %s", res.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "unpack ok") {
		return fmt.Errorf("push rejected: %s", respBody)
	}
	return nil
}

// chat runs one LLM step; the contract is the same JSON the harness used.
func chat(prompt string) (done bool, summary string, files map[string][]byte, err error) {
	payload := map[string]any{
		"model": llmModel,
		"messages": []any{
			map[string]any{"role": "system", "content": systemPrompt},
			map[string]any{"role": "user", "content": prompt},
		},
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, llmURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+llmKey)
	res, err := httpClient.Do(req)
	if err != nil {
		return false, "", nil, err
	}
	raw, rerr := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if rerr != nil {
		return false, "", nil, rerr
	}
	if res.StatusCode >= 300 {
		return false, "", nil, fmt.Errorf("llm http %d: %s", res.StatusCode, raw[:min(160, len(raw))])
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "", nil, fmt.Errorf("llm json: %w: len=%d valid=%v body=%q", err, len(raw), json.Valid(raw), raw)
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```")
	text = strings.TrimSuffix(text, "```")
	var parsed struct {
		Done    bool              `json:"done"`
		Summary string            `json:"summary"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return false, "", nil, fmt.Errorf("llm json: %w: %s", err, text[:min(120, len(text))])
	}
	files = map[string][]byte{}
	for p, c := range parsed.Files {
		files[p] = []byte(c)
	}
	return parsed.Done, parsed.Summary, files, nil
}

const systemPrompt = `You are a coding agent working in a git repository.
Reply ONLY with a JSON object: {"done": bool, "summary": string (what you did, one line), "files": {"path": "full new file content", ...}}.
Write complete file contents for every file you create or change; omit files you don't touch. When the task is complete, set done=true. Never run git commands.`

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// (main and serve moved to runner/cmd/agent and runner/cmd/worker)
func startServe() {
	http.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var run RunRequest
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					log("run panicked:", rec)
					Report(run.RunID, "failed", fmt.Sprintf("panic: %v", rec), "", "", "", nil)
				}
			}()
			runOne(run)
		}()
		w.WriteHeader(http.StatusAccepted)
	})
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log("fleet agent serving on :8080")
	_ = http.ListenAndServe(":8080", nil)
}

// runOne executes a single run end-to-end.
// Run executes one run end-to-end (the wasm worker entry).
func Run(req RunRequest) {
	ensureConfig()
	runOne(req)
}

// Pull is the CLI pull mode: fetch the next queued run and execute it.
func Pull() {
	ensureConfig()
	var run RunRequest
	if err := xrpc(http.MethodGet, "/xrpc/org.tangled.fleet.nextRun", nil, runnerKey, &run); err != nil {
		log("no run:", err)
		os.Exit(0)
	}
	runOne(run)
}

// Serve is the serve mode: HTTP entries for the runner worker.
func Serve() {
	ensureConfig()
	startServe()
}

func runOne(run RunRequest) {
	log("run", run.RunID, run.Repo, "branch", run.Branch, "-", run.TodoTitle)

	gr, base, files, err := clone(run.Repo)
	if err != nil {
		Report(run.RunID, "failed", "clone: "+err.Error(), "", "", "", nil)
		return
	}
	log("cloned", len(files), "files at", base[:min(8, len(base))])

	var listing []string
	for p := range files {
		listing = append(listing, p)
	}
	sort.Strings(listing)
	prompt := "Repository files:\n" + strings.Join(listing, "\n") +
		"\n\nTask: " + run.TodoTitle + "\nDo the task. Reply with the JSON described."

	merged := map[string][]byte{}
	for p, c := range files {
		merged[p] = c
	}
	done, summary := false, ""
	for step := 0; step < 5; step++ {
		done, summary, files, err = chat(prompt)
		if err != nil {
			Report(run.RunID, "failed", "llm: "+err.Error(), "", "", "", nil)
			return
		}
		for p, c := range files {
			merged[p] = c
		}
		if done {
			break
		}
		prompt = "Continue the task. Reply with the JSON described."
	}
	if len(files) == 0 {
		Report(run.RunID, "failed", "agent produced no changes", "", "", "", nil)
		return
	}

	// CI-lite: replicate the fast half of CI before proposing anything.
	// Everything checkable without a toolchain is checked in-process
	// (Go files must parse, JSON must be valid); failures go back to the
	// model for a repair round. Only verified changes get pushed.
	var checks []CheckResult
	for attempt := 0; attempt < 3; attempt++ {
		checks = verifyChanges(files, merged)
		if checksOK(checks) {
			break
		}
		if attempt == 2 {
			Report(run.RunID, "failed", "checks failed after repairs:\n"+failedChecks(checks), "", "", "", checks)
			return
		}
		log("checks failed — repair round", attempt+1)
		_, _, fixed, err := chat("Your changes failed verification:\n" + failedChecks(checks) +
			"\nFix the failing files. Reply with the JSON described.")
		if err != nil {
			Report(run.RunID, "failed", "llm (repair): "+err.Error(), "", "", "", checks)
			return
		}
		for p, c := range fixed {
			files[p] = c
			merged[p] = c
		}
	}

	// build the new tree+commit natively
	treeOID, err := gr.WriteTreeFromFiles(merged)
	if err != nil {
		Report(run.RunID, "failed", "tree: "+err.Error(), "", "", "", nil)
		return
	}
	commitContent := gitrepo.EncodeCommit(gitrepo.CommitSpec{
		Tree: treeOID, Parents: []string{base},
		Message: "agent: " + run.TodoTitle,
		When:    time.Now().UTC(), Ident: "fleet-agent <agent@knot>",
	})
	newCommit, err := gr.StoreObject("commit", commitContent)
	if err != nil {
		Report(run.RunID, "failed", "commit: "+err.Error(), "", "", "", nil)
		return
	}
	if err := push(run.Repo, run.Branch, newCommit, run.Token, gr); err != nil {
		Report(run.RunID, "failed", "push: "+err.Error(), "", "", "", checks)
		return
	}
	log("pushed", newCommit[:min(8, len(newCommit))])
	Report(run.RunID, "done", summary, run.Branch, base, newCommit, checks)
}

// CheckResult is one CI-style verification of the agent's changes.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// verifyChanges replicates the fast half of CI in-process: anything
// checkable without a toolchain is checked over the files the agent
// touched (with their final merged content). Go files must parse;
// JSON must be valid. Pure stdlib — runs in wasm.
func verifyChanges(touched, merged map[string][]byte) []CheckResult {
	paths := make([]string, 0, len(touched))
	for p := range touched {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []CheckResult
	for _, p := range paths {
		content, ok := merged[p]
		if !ok {
			content = touched[p]
		}
		switch {
		case strings.HasSuffix(p, ".go"):
			name := "go-syntax:" + p
			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, p, content, parser.AllErrors); err != nil {
				out = append(out, CheckResult{Name: name, OK: false, Detail: err.Error()})
			} else {
				out = append(out, CheckResult{Name: name, OK: true})
			}
		case strings.HasSuffix(p, ".json"):
			name := "json:" + p
			if json.Valid(content) {
				out = append(out, CheckResult{Name: name, OK: true})
			} else {
				out = append(out, CheckResult{Name: name, OK: false, Detail: "invalid JSON"})
			}
		}
	}
	return out
}

func checksOK(checks []CheckResult) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}

func failedChecks(checks []CheckResult) string {
	var sb strings.Builder
	for _, c := range checks {
		if !c.OK {
			fmt.Fprintf(&sb, "%s: %s\n", c.Name, c.Detail)
		}
	}
	return sb.String()
}

// Report is the run callback to the knot (exported for the worker entry).
func Report(runID, status, summary, branch, base, head string, checks []CheckResult) {
	if status != "done" {
		log("run failed:", summary)
	}
	body := map[string]any{
		"runId": runID, "status": status, "summary": summary,
		"branch": branch, "base": base, "head": head,
	}
	if len(checks) > 0 {
		body["checks"] = checks
	}
	if err := xrpc(http.MethodPost, "/xrpc/org.tangled.fleet.runDone", body, runnerKey, nil); err != nil {
		log("report:", err)
	}
}
