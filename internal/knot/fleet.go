package knot

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
	"github.com/yieldenjoyoors/forgesi/internal/gitrepo"
	"github.com/yieldenjoyoors/forgesi/internal/todo"
)

// The fleet engine: the orchestrator tick reads todo.md from each repo's
// HEAD, assigns the first unowned item to an agent (an attributed commit),
// mints a push token, and starts a run on the runner. The runner clones,
// works, pushes its branch, and calls runDone — the knot then opens the
// proposal (ticket + patch).

func (k *Knot) fleetRoutes() {
	k.mux.HandleFunc("POST /xrpc/org.tangled.fleet.tick", k.handleTick)
	k.mux.HandleFunc("POST /xrpc/org.tangled.fleet.runDone", k.handleRunDone)
	k.mux.HandleFunc("POST /xrpc/org.tangled.review.submitPatch", k.handleSubmitPatch)
	k.mux.HandleFunc("GET /xrpc/org.tangled.fleet.nextRun", k.handleNextRun)
}

// tick is the orchestrator: assign + start one run per repo with an open
// item. Starter launches the run (runner fetch in production; the local
// E2E pulls via nextRun).
func (k *Knot) tick() error {
	if k.opt.DB == nil || k.opt.MintKey == nil {
		return fmt.Errorf("fleet requires D1 and a signing key")
	}
	for _, repo := range k.registry.list() {
		gr := gitrepo.Repo{Be: k.be}
		content, err := gr.ReadFileAt("", "todo.md")
		if err != nil {
			continue // no todo.md yet
		}
		items := todo.Parse(string(content))
		item, ok := todo.Pick(items)
		if !ok {
			continue
		}
		if k.hasRunningRun(repo.Did) {
			continue // one run per repo at a time
		}
		agent := "agent-1"
		// 1. the attributed assignment commit
		updated := todo.Assign(string(content), item.Line, agent, "in-progress")
		if _, err := gr.CommitFile(repo.DefaultBranch, "todo.md", []byte(updated),
			"orchestrator: assign '"+item.Title+"' to @"+agent,
			"orchestrator <orchestrator@knot>"); err != nil {
			continue
		}
		// 2. mint the run's push token (agent branch)
		branch := fmt.Sprintf("agent/%d", item.Line)
		token, err := atauth.Mint(k.opt.MintKey, repo.Did, k.did(),
			"org.tangled.git.receivePack", 30*time.Minute, time.Now())
		if err != nil {
			continue
		}
		// 3. record the run
		runID := newRunID()
		_, err = k.opt.DB.Exec(
			`INSERT OR REPLACE INTO knot_runs (id, repo, branch, todo_line, todo_title, status, created_at) VALUES (?, ?, ?, ?, ?, 'running', ?)`,
			runID, repo.Did, branch, item.Line, item.Title, time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			continue
		}
		// 4. start the run
		if k.opt.Starter != nil {
			_ = k.opt.Starter(RunRequest{
				RunID: runID, Repo: repo.Did, Branch: branch,
				TodoTitle: item.Title, Token: token,
			})
		}
	}
	return nil
}

// RunRequest is the payload a runner receives.
type RunRequest struct {
	RunID     string `json:"runId"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	TodoTitle string `json:"todoTitle"`
	Token     string `json:"token"`
}

func (k *Knot) hasRunningRun(repo string) bool {
	var n int
	_ = k.opt.DB.QueryRow(`SELECT COUNT(*) FROM knot_runs WHERE repo = ? AND status = 'running'`, repo).Scan(&n)
	return n > 0
}

func newRunID() string {
	return "run-" + time.Now().UTC().Format("20060102T150405") + "-" + fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff)
}

// handleTick triggers the orchestrator (authed, or the cron path).
func (k *Knot) handleTick(w http.ResponseWriter, r *http.Request) {
	if k.opt.Auth.Authorize(r, k.did()) == "" && !k.opt.CronInternal {
		w.Header().Set("WWW-Authenticate", `Basic realm="knot"`)
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	if err := k.tick(); err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ticked"})
}

// runnerAuthorized checks the shared runner key (pull mode's credential).
func (k *Knot) runnerAuthorized(r *http.Request) bool {
	if k.opt.RunnerKey == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	return h == "Bearer "+k.opt.RunnerKey
}

// handleNextRun hands the next queued run to a runner (dev/local pull
// model; production pushes via Starter).
func (k *Knot) handleNextRun(w http.ResponseWriter, r *http.Request) {
	if !k.runnerAuthorized(r) {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "runner key required")
		return
	}
	if k.opt.DB == nil {
		writeErr(w, http.StatusNotImplemented, "NotImplemented", "no D1")
		return
	}
	row := k.opt.DB.QueryRow(`SELECT id, repo, branch, todo_title FROM knot_runs WHERE status = 'running' ORDER BY created_at LIMIT 1`)
	var req RunRequest
	if err := row.Scan(&req.RunID, &req.Repo, &req.Branch, &req.TodoTitle); err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "NotFound", "no queued runs")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	token, err := atauth.Mint(k.opt.MintKey, req.Repo, k.did(),
		"org.tangled.git.receivePack", 30*time.Minute, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "InternalServerError", err.Error())
		return
	}
	req.Token = token
	writeJSON(w, http.StatusOK, req)
}

// handleRunDone is the runner's callback: mark the run, open the proposal
// (ticket + patch records) for the pushed branch.
func (k *Knot) handleRunDone(w http.ResponseWriter, r *http.Request) {
	if !k.runnerAuthorized(r) && k.opt.Auth.Authorize(r, k.did()) == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "runner key or service auth required")
		return
	}
	var body struct {
		RunID   string `json:"runId"`
		Status  string `json:"status"` // done | failed
		Summary string `json:"summary"`
		Branch  string `json:"branch"`
		Base    string `json:"base"`
		Head    string `json:"head"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if body.Status == "" {
		body.Status = "done"
	}
	_, _ = k.opt.DB.Exec(`UPDATE knot_runs SET status = ?, summary = ?, ended_at = ? WHERE id = ?`,
		body.Status, body.Summary, time.Now().UTC().Format(time.RFC3339), body.RunID)

	if body.Status == "done" && body.Head != "" {
		// the proposal: a ticket with targetBranch + the patch record
		var repo string
		_ = k.opt.DB.QueryRow(`SELECT repo FROM knot_runs WHERE id = ?`, body.RunID).Scan(&repo)
		if repo != "" {
			k.openProposal(repo, body.Branch, body.Base, body.Head, body.Summary)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// openProposal writes the ticket and patch COBs (the Tangled PR shape).
func (k *Knot) openProposal(repo, branch, base, head, summary string) {
	editor := "did:web:" + k.opt.Host + ":orchestrator"
	now := time.Now().UTC().Format(time.RFC3339)
	ticket := map[string]any{
		"$type":        "org.tangled.track.ticket",
		"title":        map[string]any{"$type": "org.tangled.markup.markdown#inline", "text": "agent: " + summary},
		"body":         map[string]any{"$type": "org.tangled.markup.markdown", "text": summary},
		"state":        "open",
		"targetBranch": branchOf(base),
		"createdAt":    now,
	}
	w, err := k.store.Create(repo, "org.tangled.track.ticket", "", ticket, editor)
	if err != nil {
		return
	}
	patch := map[string]any{
		"$type":     "org.tangled.review.patch",
		"ticket":    w.Rkey,
		"base":      map[string]any{"$type": "org.tangled.git.oid", "oid": base},
		"head":      map[string]any{"$type": "org.tangled.git.oid", "oid": head},
		"createdAt": now,
	}
	_, _ = k.store.Create(repo, "org.tangled.review.patch", "", patch, editor)
	k.emit(repo, "commit", "org.tangled.track.ticket", w.Rkey, w.CID, w.Change, ticket)
}

// handleSubmitPatch accepts the standard wire shape too (parity with the
// web client): attach a patch to an existing ticket.
func (k *Knot) handleSubmitPatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo  string `json:"repo"`
		Patch struct {
			Ticket string `json:"ticket"`
			Base   struct {
				OID string `json:"oid"`
			} `json:"base"`
			Head struct {
				OID string `json:"oid"`
			} `json:"head"`
			SourceRepo   string `json:"sourceRepo"`
			SourceBranch string `json:"sourceBranch"`
		} `json:"patch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	editor := k.opt.Auth.Authorize(r, body.Repo)
	if editor == "" {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", "missing service auth")
		return
	}
	if _, ok := k.registry.byDID(body.Repo); !ok {
		writeErr(w, http.StatusBadRequest, "InvalidRequest", "no such repo")
		return
	}
	patch := map[string]any{
		"$type":     "org.tangled.review.patch",
		"ticket":    body.Patch.Ticket,
		"base":      map[string]any{"$type": "org.tangled.git.oid", "oid": body.Patch.Base.OID},
		"head":      map[string]any{"$type": "org.tangled.git.oid", "oid": body.Patch.Head.OID},
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	res, err := k.store.Create(body.Repo, "org.tangled.review.patch", "", patch, editor)
	k.writeCobResult(w, res, err)
}

func branchOf(base string) string {
	if base == "" {
		return "main"
	}
	return strings.TrimPrefix(base, "refs/heads/")
}

// TickInternal runs the orchestrator from the in-process cron caller.
func (k *Knot) TickInternal() {
	k.opt.CronInternal = true
	defer func() { k.opt.CronInternal = false }()
	_ = k.tick()
}
