---
title: "taskloom"
description: "Per-project task tracking for you and your agents: one append-only log, a CLI, and an MCP server over the same store."
---

An agent notices something worth doing later and mentions it once, in passing — then the
session ends, or the context window turns over, and that follow-up is gone. taskloom's shared
store is what fixes this: an agent that files a follow-up with `task_add` files it where you
will see it with `taskloom list`; a task you defer on the command line disappears from the
agent's active list too.

**taskloom** is a per-project task store that ships from the ctxloom repo. It keeps one
append-only log per project and puts two front doors on it: the `taskloom` CLI for you, and
an MCP server (`taskloom mcp`) for your agents — both reading and writing the *same* tasks.

Tasks are keyed by **harp IDs** (`swift-amber-falcon`) rather than numbers, so an ID stays
stable and unambiguous when a model echoes it back in a later call.

The store is an append-only JSONL log at `~/.ctxloom/tasks/<project-id>.jsonl`. Current state
is the fold of its events, so nothing is destructive: a task's history survives every status
change, and `Archived` drops a task from view without erasing it.

## The status model

Five statuses, in the order the tools display them:

| Status | Meaning |
|---|---|
| `In Progress` | Being worked now. |
| `To Do` | Open, unclaimed. The default for a new task. |
| `Deferred` | Parked on a named revive condition. Requires a trigger. |
| `Done` | Completed. |
| `Archived` | Dropped from the active list without losing the history. |

`Done` and `Archived` are terminal. `taskloom list` hides both by default, along with
`Deferred`, so the default listing shows only live work — `To Do` and `In Progress`. Pass
`--all`, or name a status explicitly with `--status Deferred`, to see the rest. Your code
never has to hardcode this taxonomy: `taskloom statuses --json` emits it, marking which
statuses are terminal and which require a trigger.

## Deferred, and what a trigger is for

A deferred task is hidden from the default list. That is the whole reason the trigger exists.

Without one, "I'll get to this when the v2 API ships" is indistinguishable from abandonment.
The task drops out of the listing, nobody sees it again, and the work is silently gone — the
failure mode that makes most people refuse to defer anything and let their active list rot
into noise instead. A **trigger** is the condition that should bring the task back. It is a
free-text description of what has to become true: "the v2 API ships", or "a second user asks
for it".

So `Deferred` is the one status that cannot be set without an argument. `taskloom status
<harp-id> Deferred` fails unless a `--trigger` is supplied or the task already carries one
from a previous deferral. The store enforces this once, so the CLI and the MCP tools obey the
same rule.

Triggers are declarative free text, and nothing evaluates them mechanically. Revival is a
review step, not a mechanism: list the deferred tasks (`taskloom list --status Deferred`),
reason about which triggers have actually fired, and move the ones that have back with
`taskloom status <harp-id> "To Do"`. There is no automatic transition — an LLM judges, a
human confirms. (A `check-triggers` command that automates this review ships as one of
`ctxloom`'s own built-in slash commands, embedded in the `ctxloom` binary and available in
any session `ctxloom run` launches — it is not part of taskloom's loadout, so a bare
`taskloom` install with no `ctxloom` binary in the picture won't have it.) This mirrors the
revive-trigger convention the project's ADRs already use, so deferring a task and deferring
a decision work the same way.

Deferring with a trigger is therefore a real alternative to closing something. Reach for it
when the work is still worth doing but blocked on a condition you can name. Reach for `Done`
or `Archived` when it isn't.

## Which project you are writing to

Every task belongs to exactly one project, and a task landing in a store you didn't expect is
the most common confusion with the tool. The project id is resolved in this order:

1. `--project`, which wins over everything.
2. `CTXLOOM_PROJECT_ID`, exported into the environment by `ctxloom run`.
3. The working directory's identity, via its in-tree marker and the project registry.

The second rule is the one that surprises people. Inside a `ctxloom run` session the project
is *pinned*: `cd` into an unrelated repo, add a task, and it still lands in the session's
project, not the one you're standing in. That is deliberate, since an agent that wanders the
filesystem shouldn't scatter its follow-ups across every repo it visits, but it means the
working directory is not always the answer. Every mutating command prints the store it wrote
to on stderr for this reason, and `taskloom list` names the project above the table. When a
task seems to vanish, read that line first.

Directory resolution has two more outcomes worth knowing. If a project tree has *moved*,
taskloom re-points the registry and keeps the same id, so the history follows the code. If
the tree looks *copied* (the original is still sitting there with its marker intact) taskloom
mints a fresh identity for the copy rather than let two trees write to one log. Both cases
print a warning.

## Working with agents

Agents reach the same store through `taskloom mcp`, which serves five tools: `task_list`,
`task_add`, `task_tag`, `task_set_status`, and `task_edit`. The intended shape of a coordinator
session is straightforward. It opens by calling `task_list` to see what is already outstanding
rather than re-deriving the work from scratch. It files anything it discovers but won't do now
with `task_add` — the follow-up it would otherwise mention once in prose and lose when the
context window turns over. It moves work with `task_set_status` as it goes, and echoes a task's
`harp_id` back whenever it refers to that task later, which is what the stable ids are for.
Every task can also carry [tags](/taskloom/tags/) — flat markers like `urgent`, or
namespace-scoped facts like `triage:kind=defect` — set with `task_add`'s `tags` parameter or
added/removed later with `task_tag`, and filtered on with `task_list`'s `tag_query`.

The discipline that makes this pay off is the deferral rule. An agent that finishes a job and
notices three things it deliberately did not do should file them, not bury them in a summary.
If one of them is blocked, it defers with the trigger rather than dropping it. Because the CLI
and MCP write the same log, everything the agent files is waiting in `taskloom list` when you
get back.

## plan, run, and watch

Three commands sit alongside the task store.

`taskloom plan` browses **session plans** — the `*.plan.md` documents an agent writes under
`~/.ctxloom/sessions/<harp>/`. `plan list` enumerates them across sessions and `plan show
<path>` prints one. These are plan documents, not tasks; the two are separate stores, and
`plan` is a reader with no write side.

`taskloom run` turns a task into a working agent session. With no argument it shows a picker
of open tasks; given a harp id it launches that one directly. The chosen task is marked
`In Progress`, and taskloom shells out to `ctxloom run` with the task text as the prompt,
continuing the session that originally filed the task so the agent picks up the context that
produced it. `--no-start` leaves the task at `To Do` instead. This is taskloom's only command
that depends on ctxloom — it needs `ctxloom` on `PATH`, and everything else works standalone.

`taskloom watch` streams task-store changes as JSONL, one line per change, running until
interrupted. It exists for GUIs: the VS Code client subscribes and re-queries on each event
instead of polling. It is hidden from `--help` and there is little reason to run it by hand.

## Reference

- **[Tags](/taskloom/tags/)** — flat tags, `tag_query`'s postfix boolean grammar, scalar
  targets, and priority ranking.
- **[CLI reference](/taskloom/reference/cli/)** — every command, generated from the binary.
- **[MCP tools reference](/taskloom/reference/mcp-tools/)** — `task_list`, `task_add`,
  `task_set_status`, `task_edit`, generated from the tool registrations.

## Install

```sh
go install github.com/ctxloom/ctxloom/cmd/taskloom@latest
# or build from source in the ctxloom repo: just build-taskloom
```
