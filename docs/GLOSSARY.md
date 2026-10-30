# ForgeSI Glossary

The product vocabulary. Use these words exactly, in UI copy, docs,
commit messages, and code comments. When in doubt, this file wins.

## The core frame

**Humans work on branches. Agents work in proposals.**
Both land in git; only one needs your review.

## People

| Term | Meaning |
| --- | --- |
| Human | A person with a passkey. The trust root. Owners and reviewers are humans. |
| Owner | The human who controls instance config, invites, and agent setup. |
| Agent | A non-human participant with a minted token, a role, and optional model overrides. Never "bot", never "AI assistant". |
| Fleet | The agents assigned to one project. Rendered on the deck. |
| Orchestrator | The agent that assigns unassigned todos. Does not implement. |
| Architect | The agent that writes the plan a coder follows, before the first run. |
| Coder | The agent that implements an assigned todo and opens a proposal. |
| Reviewer agent | The agent that runs automated review verdicts. A human still holds the default gate. |
| Reviewer | A human with merge authority on a project. |

## Work

| Term | Meaning |
| --- | --- |
| Todo | One line in `todo.md`, the project's intent. Assigned via `@handle`. The unit of "what should happen". |
| Task | A todo that an agent has been assigned and picked up. A todo becomes a task when work starts. |
| Branch | A git ref a human pushes. Humans work on branches; they browse them directly. |
| Proposal | The reviewable unit of agent work. Created when an agent pushes its task branch. Has a diff, a why, and verdicts. Never "PR", never "pull request". |
| Diff | The unified change a proposal carries. Client-computed at push or server-computed by the diff engine. Always "the diff", never "the change set". |
| Verdict | A human's review decision on a proposal: approve or request changes. Never "comment", never "review comment". |
| Merge | The act of landing an approved proposal into the project's main branch. Serialized per project by the merge coordinator. |

## Surfaces

| Term | Meaning |
| --- | --- |
| Deck | The project's control view: todos, fleet, attention strip. "The deck", lowercase. |
| Console | The whole authenticated UI (deck, projects list, settings). |
| Explore | The anonymous gallery of public projects at `/explore`. |
| Code browser | The tree + file view inside the deck. URLs are first-class: `/project/blob/<ref>/<path>` for files, `/project/tree/<ref>/<path>` for directories. |
| Instance | One Forge deployment (one D1, one Artifacts namespace, its own passkeys). |

## Git mapping

| Forge concept | Git object |
| --- | --- |
| Project | Repository |
| Proposal | Branch + metadata (author, diff, verdicts) |
| Todo board | `todo.md` on the default branch |
| Merge | Fast-forward/squash of the task branch into `main` |

## Language rules

- "Proposal", never "PR" or "pull request".
- "Agent", never "bot" or "assistant".
- "Verdict", never "comment" on a review decision.
- "Fleet", never "agent list".
- "Skill" is a committed directory under `skills/` (project repo) or in
  the global `forge-skills` repo, never a freeform tag: a skill has a
  name that exists in one of those sources. Agent skills reference
  these names; the settings picker autocompletes from them.
- Branch names and proposal titles are data, not prose: keep them exact.
- No em dashes anywhere (UI copy, comments, docs, commit messages).
