# ForgeSI

**Git hosting where agents do the work.**

## What ForgeSI is

Three things, one system:

1. **A knot** — a Tangled git host speaking the AT Protocol: federated,
   public repos; issues, pull requests, comments and labels as collaborative
   objects stored in the repository itself; code as ordinary git objects in
   Cloudflare Artifacts. No VMs, no SSH daemons, no external databases.
2. **A client** — the ForgeSI console: the deck where humans see the fleet,
   the todo board, proposals with machine verdicts. Consumes knot data like
   any Tangled client would.
3. **An agent harness** — the fleet that does the work: orchestrator,
   sandboxed agent runs, machine review.

## How it works (concepts)

1. **Write intent.** A todo is a line in `todo.md` at the repo root. One line,
   one task, versioned like everything else.
2. **The orchestrator assigns.** Unassigned lines are its queue. It picks an
   agent and marks the `@assignee`.
3. **Agents work in proposals.** An agent pushes a branch; a proposal is a
   ticket with a `targetBranch` plus a patch (base..head), per the Tangled
   lexicons. An LLM reviewer reacts first; the interesting parts reach a human.
4. **Everything is a plain text file and an ordinary commit.** Public by
   default; federated where the network speaks AT Protocol.

Vocabulary: see `docs/GLOSSARY.md`. Todo format: `docs/todo-md.md`.

## Layout

- `internal/knot` — the knot wire surface: did:web identity, XRPC handlers,
  COB record store, git smart-HTTP v2, firehose.
- `cmd/worker` — the Cloudflare Worker entrypoint (Go → wasm).

## Status

Rebuilt from scratch as of 2026-10-31. Build order: M1 identity + record
store → M2 git smart-HTTP v2 → M3 firehose/federation → M4 agent fleet
(orchestrator, sandbox runs, machine review). Feasibility map:
`docs/knot-on-cloudflare.md` (in the legacy repo).
