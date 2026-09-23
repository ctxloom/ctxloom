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
task_tag          # add or remove a task's tags
```

ctxloom does not carry a copy of taskloom's wiring. It finds taskloom the same
way it finds any **companion** — a standalone tool that describes itself. At
startup ctxloom looks for companion binaries on your `PATH` (the first-party
names, plus anything called `ctxloom-companion-*`) and runs
`<bin> loadout --format json`. It executes a binary only when a detached
`<binary>.sig` beside it verifies against a key you trust; anything else is
skipped with a warning (`ctxloom companion list` shows which would run, and
why). The loadout the binary prints carries the bundle it contributes, MCP
server included, and that content is admitted as the companion's own, the same
way your project's authored content is. A rejection you record still withholds
any item of it.

The practical consequence: install a signed `taskloom` and it wires itself in;
remove it from `PATH` and it quietly disappears from the session.

The same store is scriptable from your shell (`taskloom add`, `taskloom list`,
`taskloom status`, `taskloom edit`, `taskloom summary`, `taskloom statuses`).

They live in a per-project task log, and each task is attributed to the session
that created it. A task has a status — `To Do`, `In Progress`, `Done`,
`Archived`, or `Deferred` — and a deferred task carries a **revive trigger**: a
concrete condition that should bring it back onto the active list.

Because tasks are stored on disk rather than in the conversation, they **persist
across `/clear`** and across resumes. Nothing carries them into a run: they stay
in the project's task log, and any session reads them from there with
`task_list`.

When you *resume* a session with `ctxloom run --session <harp>`, its full recorded
transcript is folded into the new run's context. Add `--distill` to resume from
its distilled essence instead.

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
