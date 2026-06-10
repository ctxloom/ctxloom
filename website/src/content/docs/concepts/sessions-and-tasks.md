---
title: "Sessions and Tasks"
---

A **session** is one working conversation, recorded by ctxloom so it survives
`/clear` and can be recovered or distilled later (see
[Session Memory](/getting-started/memory/)). **Tasks** are durable work items
attached to your project that outlive any single session.

## Tasks

Tasks are tracked by the standalone [`taskloom` binary](https://github.com/ctxloom/taskloom),
which ships an MCP server (`taskloom mcp`) registered for agents by ctxloom's
built-in tasks bundle:

```
task_add          # add a task
task_list         # list tasks (optionally filtered by status)
task_set_status   # move a task between statuses
task_edit         # replace a task's text
```

The same store is scriptable from your shell (`tasks add`, `tasks list`,
`tasks status`, `tasks summary`, `tasks run`).

They live in a per-project task log, and each task is attributed to the session
that created it. A task has a status — `To Do`, `In Progress`, `Done`,
`Archived`, or `Deferred` — and a deferred task carries a **revive trigger**: a
concrete condition that should bring it back onto the active list.

Because tasks are stored on disk rather than in the conversation, they **persist
across `/clear`** and across resumes. Carry a prior session's tasks into a new run
with `--tasks-from <session>`, or start clean with `--no-tasks`.

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

When a session is distilled for memory, both the agent's TodoWrite blocks and any
`task_add` calls are extracted into the essence, so the next session inherits an
accurate picture of what was planned and what is still outstanding.
