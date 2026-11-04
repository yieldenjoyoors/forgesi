# Agents

Working conventions for any agent (fleet or human-driven) touching this
repository. The fleet loads this file automatically; read it first.

## What this is

ForgeSI is a Tangled knot that runs entirely on Cloudflare: one Go-wasm
Worker (`forgesi-knot`) serving the XRPC surface, git smart-HTTP, the
firehose, the fleet orchestrator and the console, with **git objects,
refs and COB records stored in Cloudflare Artifacts** (one Artifacts
repo per knot repo) and D1 as the registry + event log + run ledger.
The agent runner is a plain wasm Worker (`forgesi-runner`).

## Hard rules

- Cloudflare only: Workers, D1, Artifacts. No VMs, no SSH, no other
  clouds. Containers are dead on this account — never reach for them.
- All git refs are namespaced `git/<repoDID>/…` in the knot; storage
  authority is the per-repo Artifacts remote. Never write a bare
  `refs/heads/…` to the knot store.
- Humans are the merge gate unless the repo's `forgeci.json` clears
  the change for agent review; absent policy = human required.
- Outbound HTTP from wasm MUST use the fetch-backed transport
  (`HTTP: httpRuntime`) — plain net/http cannot dial. This has bitten
  the runner, the reviewer, and the wake; do not rediscover it.
- Anything side-effecting in a request handler (wakes, reviews) runs
  under `waitUntilHook` — a raw goroutine is killed when the handler
  returns.
- `todo.md` is the fleet's queue. Every mutation commits through git
  with optimistic head checks; a 409 means re-apply, never force.

## Gates (all green before every deploy)

```
gofmt -w .
go vet ./internal/... ./runner/agent/ ./runner/cmd/agent/
GOOS=js GOARCH=wasm go vet ./cmd/worker/ ./runner/cmd/worker/
go test ./internal/... ./runner/agent/
golangci-lint run ./internal/... ./runner/agent/
workers-assets-gen -mode=go -o dist-gen
GOOS=js GOARCH=wasm go build -trimpath -o dist-gen/app.wasm ./cmd/worker
npm run build:web
```

Deploy AND push after every change. The SPA shell is embedded in the
wasm — a web/ change without the wasm rebuild ships a stale shell.

## Style

- Small surfaces: prefer deleting to adding. The console is plan,
  activity, files — no dashboard sprawl.
- Errors as comments on the record they concern; the feed labels
  events for what they are. Nothing is a bare oid.
- Tests mint real tokens against a per-test file DB
  (`t.TempDir()`); never share a named in-memory SQLite.
- The repo drives agents: AGENTS.md (this file), `.agents/workflow.md`,
  `.agents/skills/*` — extend those before touching runner defaults.

## Fleet notes

- Runs: assign → wake (POST to the runner) → clone → GLM writes →
  CI-lite (gofmt auto-applied; Go must parse; JSON must be valid;
  repair rounds on failure) → push → runDone + checks → proposal
  (patch rounds on resubmission) → GLM review (BLOCK: holds) →
  policy auto-merge or human.
- Zombie `running` rows block the whole fleet (`hasRunningRun`) —
  close them via runDone; check for them before debugging anything
  else. An auto-expire is planned.
- Prod write probes: `cmd/mintprobe` (KEYFILE=/tmp/knotkey.txt; the
  file silently falling missing makes mintprobe sign garbage — restore
  from `.dev.vars` before blaming the knot).
