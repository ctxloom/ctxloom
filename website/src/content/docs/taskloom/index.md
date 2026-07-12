---
title: "taskloom"
description: "Per-project task tracking for you and your agents: one append-only log, a CLI, and an MCP server over the same store."
---

**taskloom** is a per-project task store that ships from the ctxloom repo. It keeps one
append-only log per project and puts two front doors on it: the `taskloom` CLI for you, and
an MCP server (`taskloom mcp`) for your agents — both reading and writing the *same* tasks.

That shared store is the point. An agent that files a follow-up with `task_add` files it
where you will see it with `taskloom list`; a task you defer on the command line disappears
from the agent's active list too.

Tasks are keyed by **harp IDs** (`swift-amber-falcon`) rather than numbers, so an ID stays
stable and unambiguous when a model echoes it back in a later call.

## Reference

- **[CLI reference](/taskloom/reference/cli/)** — every command, generated from the binary.
- **[MCP tools reference](/taskloom/reference/mcp-tools/)** — `task_list`, `task_add`,
  `task_set_status`, `task_edit`, generated from the tool registrations.

## Install

```sh
go install github.com/ctxloom/ctxloom/cmd/taskloom@latest
# or build from source in the ctxloom repo: just build-taskloom
```

<!-- PROSE PLACEHOLDER (hand-written, separate workstream):
     This overview is deliberately thin. What belongs here and has to be written by hand:
       - the task lifecycle and the status model (Todo / In Progress / Done / Deferred /
         Archived), and what a Deferred *trigger* is for
       - project resolution: CTXLOOM_PROJECT_ID (exported by `ctxloom run`), --project, and
         cwd/registry fallback — and why a task can land in a store you did not expect
       - the coordinator workflow: how an agent session is expected to use task_list/task_add
       - `taskloom plan` / `taskloom run` / `taskloom watch`: the plan-file workflow, which
         has no prose anywhere today
     Concepts pages (a "Tasks" concept, guides) would hang off this section. -->
