---
title: "Sessions and Tasks"
---

`/clear` the window, or come back after a break, and you can lose more than the
conversation — you can lose track of what you'd agreed still needed doing. ctxloom
keeps both: a session so the conversation itself survives, a task so a work item
does too, even once the session it came from is gone.

A **session** is one working conversation, recorded by ctxloom so you can find
it and resume it later (see [Session History](/getting-started/memory/)). **Tasks** are durable work items
attached to your project that outlive any single session.

## Tasks

Tasks are tracked by the standalone `taskloom` binary. You and your agents use
the same CLI; taskloom serves no MCP, and an agent runs it in its shell (output
is JSON whenever stdout is not a terminal, or ask for it with `--json`):

```
taskloom add <text>                     # add a task
taskloom list                           # list tasks (--status, postfix --tag-query)
taskloom status <harp> <status>         # move a task between statuses
taskloom status <harp> --trigger <cond> # defer a task on a revive condition
taskloom edit <harp> <text>             # replace a task's text
taskloom tag <harp> --add/--remove <t>  # add or remove a task's tags
taskloom show <harp>                    # show a task whole
```

ctxloom does not carry a copy of taskloom's wiring. It finds taskloom the same
way it finds any **companion** — a standalone tool that describes itself. At
startup ctxloom resolves each companion you registered (`ctxloom companion add
taskloom`) on your `PATH` and runs `<bin> loadout --format yaml`; nothing on
`PATH` is run unless its name is registered (`ctxloom companion list` shows
what is registered and whether each resolves). The loadout the binary prints
carries the bundle it contributes (its guidance, commands and hooks), and that content is
taken as the companion's own, the same way your project's authored content is.

The practical consequence: install `taskloom` and register it (the installer
does both) and it wires itself in; `ctxloom companion remove taskloom --yes`
takes it out of the session.

The read-only commands round it out: `taskloom summary`, `taskloom statuses`,
`taskloom tags`, `taskloom permits` and `taskloom schema`.

They live in a per-project task log, and each task is attributed to the session
that created it. A task has a status — `To Do`, `In Progress`, `Done`,
`Archived`, or `Deferred` — and a deferred task carries a **revive trigger**: a
concrete condition that should bring it back onto the active list.

Because tasks are stored on disk rather than in the conversation, they **persist
across `/clear`** and across resumes. Nothing carries them into a run: they stay
in the project's task log, and any session reads them from there with
`taskloom list`.

When you *resume* a session with `ctxloom run --session <harp>`, its full recorded
transcript is folded into the new run's context.

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
The agent's TodoWrite checklist lives in the conversation: your engine's own
compaction may summarise it away, and it dies with the conversation, which is
what ephemeral means. A session's plan documents (the `.plan.md` files in its
output dir) are files, so they survive both.

Tasks need no extraction at all. They are already on disk in the per-project task
log, so they outlive the session that created them.
