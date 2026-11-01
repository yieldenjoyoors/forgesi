// The ForgeSI fleet runner as a plain Worker: the agent library compiled
// to wasm. POST /run executes one fleet run; the knot wakes it after
// each orchestrator tick. No containers — the agent is pure Go over
// HTTP (clone/push via the packfile code, LLM via fetch), so it runs in
// the Worker runtime directly.
package main

import (
	"encoding/json"
	"net/http"

	workers "github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare"
	agent "github.com/yieldenjoyoors/forgesi/runner/agent"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		if key := cloudflare.Getenv("KNOT_RUNNER_KEY"); key != "" &&
			r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "runner key required", http.StatusUnauthorized)
			return
		}
		var run agent.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cloudflare.WaitUntil(func() {
			defer func() {
				if rec := recover(); rec != nil {
					agent.Report(run.RunID, "failed", "panic", "", "", "")
				}
			}()
			agent.Run(run)
		})
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("run accepted"))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("runner ok"))
	})
	workers.Serve(mux)
}
