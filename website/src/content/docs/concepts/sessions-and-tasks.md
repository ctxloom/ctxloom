---
title: "Sessions and Tasks"
---

`/clear` the window, or come back after a break, and you can lose more than the
conversation — you can lose track of what you'd agreed still needed doing. ctxloom
keeps both: a session so the conversation itself survives, a task so a work item
does too, even once the session it came from is gone.

A **session** is one working conversation, recorded by ctxloom so it survives
`/clear` and can be recovered or distilled later (see
[Session Memory](/getting-started/memory/)). **Tasks** are durable work items
attached to your project that outlive any single session.

## Tasks

Tasks are tracked by the standalone `taskloom` binary, which ships an MCP server
(`taskloom mcp`):

```
task_add          # add a task
task_list         # list tasks (optionally filtered by status)
task_set_status   # move a task between statuses
task_edit         # replace a task's text
```

ctxloom does not carry a copy of taskloom's wiring. It finds taskloom the same
way it finds any **companion** — a standalone tool that describes itself. At
startup ctxloom probes the companion binaries on your `PATH` (the first-party
names, plus anything called `ctxloom-companion-*`) by running
`<bin> loadout --format json`. The binary emits its own signed bundle, and that
bundle flows through exactly the same signature-verification and trust gate as
any remote bundle before its MCP server is registered for your agents.

The practical consequence: install `taskloom` and it wires itself in; remove it
from `PATH` and it quietly disappears from the session. Nothing to configure.

The same store is scriptable from your shell (`taskloom add`, `taskloom list`,
`taskloom status`, `taskloom edit`, `taskloom summary`, `taskloom statuses`).

They live in a per-project task log, and each task is attributed to the session
that created it. A task has a status — `To Do`, `In Progress`, `Done`,
`Archived`, or `Deferred` — and a deferred task carries a **revive trigger**: a
concrete condition that should bring it back onto the active list.

Because tasks are stored on disk rather than in the conversation, they **persist
across `/clear`** and across resumes. Carry a prior session's tasks into a new run
with `ctxloom run --tasks-from <session>`.

When you *resume* a session with `--session <harp>`, its tasks come back along
with its essence. `--no-tasks` modifies that resume: `--session <harp> --no-tasks`
restores the essence only, leaving the tasks behind. It does nothing on a fresh
run — there is no prior session to withhold tasks from — and it cannot be
combined with `--tasks-from`.

## Tasks vs. the agent's to-dos

The agent (for example, Claude Code) also keeps its own **TodoWrite** checklist.
These are *not* the same thing, and ctxloom intentionally keeps them separate:

| | Agent to-dos (TodoWrite) | tasks |
|---|---|---|
| Scope | the current turn / flow | the project |
| Lifetime | ephemeral — gone when the conversation moves on | durable — survive `/clear` and resume |
| Stored in | the conversation | a per-project task log on disk |
| Best for | a quick checklist for the step at hand | work you want to track across sessions |

Rule of thumb: reach for the agent's to-dos for the micro-plan of *what I'm doing
right now*; use tasks for *work that should still exist tomorrow*.

The two also survive by different mechanisms, and it is worth knowing which.
When a session is distilled for memory, the essence captures the conversation
plus the session's plan documents (the `.plan.md` files in the session
directory) — verbatim, so nothing is paraphrased away. The agent's TodoWrite
checklist is *not* extracted; it dies with the conversation, which is what
ephemeral means.

Tasks need no extraction at all. They are already on disk in the per-project task
log, so they outlive the essence and the session that created them — a task is
still there whether or not anyone ever distilled the conversation it came from.
