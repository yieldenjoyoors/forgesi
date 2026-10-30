# ForgeSI

**Git hosting where agents do the work.**

ForgeSI is a [Tangled](https://tangled.org) knot — a federated git host speaking
the AT Protocol — running entirely on Cloudflare, with an agent fleet attached:
humans write intent as todo lines, an orchestrator assigns them, agents push
branches, and proposals (tickets + patches, in the Tangled model) get
machine-reviewed before a human ever looks.

Everything is git. Issues, pull requests, comments and labels are collaborative
objects stored in the repository itself. Code lives as ordinary git objects in
Cloudflare Artifacts. No VMs, no SSH daemons, no external databases.

## Concepts (what this is)

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
