# todo.md format

todo.md is the flat task tracker at the top of the Forge automation funnel.
One item per line; the file lives in the project repo at the repo root
(`todo.md`) and is the **source of truth**, the platform's `todo_cache` D1
table is a derived, rebuildable index.

## Grammar

```
file      := (blank | heading | item | detail | prose)*
heading   := '#' ...                       (ignored)
item      := '- [' (' ' | 'x' | 'X') ']' title [tags]
detail    := ('  ' | '\t') text            (attaches to the item above)
tags      := ( ' ' tag )*
tag       := '@' assignee | '#' status | '^' id
```

Note: the parser checks the item shape before indentation, so an INDENTED
checkbox line (`  - [ ] x`) is a new item, not a detail line.

- **title**: free text; tags inside the title text are only recognized as
  tags when they are space-separated tokens matching `@name` or a known
  `#status`.
- **assignee**: `@participant-id` (e.g. `@julien`, `@agt_coder1`). **Optional, and unassigned is the default.** The **orchestrator assigns**: it scans
  the queue and writes the `@assignee` itself as an attributed commit
  (`assigned_by: orchestrator`). Humans never pick agents in the normal flow;
  a human `@handle` written by hand is a **visible override** (`assigned_by:
human`, shown with an override chip in the console). Leaving an item
  unassigned is not an error state, the console renders it as
  `@unassigned · queued for orchestrator`.
- **status**: one of `todo` (default) | `in-progress` | `review` | `blocked`
  | `done`. `- [x]` checkbox implies `done` even without `#done`. `review`
  marks an item whose agent run landed and that waits on a human verdict;
  it never re-enters the pick queue or the agent run phase.

### Examples

```
# Project todo

- [ ] set up CI
- [x] write parser @julien #done
  uses sha256 token hashing
- [ ] ship MVP @agt_coder1 #in-progress
  first: fork the task repo
  then: implement and push
- [ ] waiting on API @julien #blocked
```

`set up CI` carries no `@assignee`, that is the normal state. It sits in the
orchestrator's queue until the orchestrator picks an agent and commits the
`@assignee` line itself.

## Tolerance

The parser never hard-fails. Lines that are not valid items (prose, broken
syntax) are **skipped and reported** in `ParseResult.warnings` with 1-based
line numbers. Detail lines with no parent item are also warnings.

## Ids and roundtrip

Items carry stable ids. The canonical line materializes one per item:

```
- [<x| >] <title> [@assignee] #<status> ^<id>
  <detail>*
```

The `^<id>` token is the item's identity: it survives deletes and
inserts, so renumbering never moves a task's history, and the platform
never hands a used number to a new todo (new ids are `t-<max+1>` across
everything in the file). Lines without a marker (legacy files) get the
first free `t-<n>` (never a number a marker in the same file claims:
duplicate ids corrupt the derived cache) and gain markers on the first
edit through the console.

`parse(render(parse(x)))` preserves items (source line numbers may shift,
which is expected after an edit rewrites the file).

## Funnel semantics (`apply_todo_edit`)

An edit that **adds or changes an assignee** triggers side effects:

- assignee is an agent id (prefix `agt_`) → `needs_fork: true`, the platform
  forks the project repo into `task-<id>` and mints the agent a write token;
- assignee is human → no fork, plain assignment.

Re-assigning the same assignee is a no-op.

### Who assigned it (`assigned_by`)

Every assignee-changing edit is attributed by comparing the todo edit commit
author against the orchestrator's participant id:

- author == orchestrator → `assigned_by: orchestrator` (the flow);
- author is anyone else (a human) → `assigned_by: human`, a **visible
  override**, surfaced with an `override` chip in the console.

The attribution ledger already stores commit authors; this is a derivation,
not new storage. The orchestrator spec lives in the module docs of
orchestrator-assigns loop specification.
