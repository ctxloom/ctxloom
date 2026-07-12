---
title: "CLI Reference"
sidebar:
  order: 0
---

Complete reference for all `taskloom` commands.

The per-command pages in this section are **generated** from the command definitions in `cmd/taskloom` (`just gen-docs`) — the same text as `taskloom <command> --help` and `man taskloom`, so they always match the binary. This page keeps the narrative that doesn't fit a `--help` screen.

Every command takes `--project` to act on a store other than the one resolved from the session or the working directory.

## Command groups

- **Reading** — [`list`](/taskloom/reference/cli/taskloom_list/), [`summary`](/taskloom/reference/cli/taskloom_summary/), [`statuses`](/taskloom/reference/cli/taskloom_statuses/)
- **Writing** — [`add`](/taskloom/reference/cli/taskloom_add/), [`status`](/taskloom/reference/cli/taskloom_status/), [`edit`](/taskloom/reference/cli/taskloom_edit/)
- **Plans and sessions** — [`plan`](/taskloom/reference/cli/taskloom_plan/), [`run`](/taskloom/reference/cli/taskloom_run/), and `watch` (a JSONL change stream for GUIs, hidden from `--help`)
- **Agents** — [`mcp`](/taskloom/reference/cli/taskloom_mcp/) serves the same store over MCP; see the [MCP tools reference](/taskloom/reference/mcp-tools/).
- **Utilities** — [`manage`](/taskloom/reference/cli/taskloom_manage/), [`version`](/taskloom/reference/cli/taskloom_version/)

## Closing a task, or deferring it

`taskloom status <harp-id> <status>` moves a task. The five statuses and their display order
are covered in the [overview](/taskloom/); what a `--help` screen can't tell you is which one
to reach for.

Use `Done` when the work is finished, and `Archived` when it is not going to happen and you
want it out of the way without erasing the record. Both are terminal, and both drop the task
from the default listing.

Use `Deferred` when the work is still worth doing but is waiting on something you can name —
and then you *must* name it:

```sh
taskloom status swift-amber-falcon Deferred --trigger "the v2 API ships"
```

The command fails without a `--trigger` unless the task already carries one from an earlier
deferral, in which case it is preserved. That refusal is the feature. A deferred task is
hidden from the default list, so a deferral with no revive condition is just a quiet delete;
the trigger is what lets `check-triggers` find the task later and ask whether its condition
has fired. Nothing evaluates a trigger mechanically, so write it as something a reader can
actually judge: "the Postgres upgrade lands", not "later".

To see parked work, name the status or ask for everything:

```sh
taskloom list --status Deferred   # just the parked tasks, with their triggers
taskloom list --all               # every status, including Done and Archived
```

## Knowing which store you wrote to

The project id is resolved as `--project`, then `CTXLOOM_PROJECT_ID` (exported by
`ctxloom run`), then the working directory's identity marker and the registry. `--project`
wins over the session pin; the session pin wins over the working directory.

That middle rule is why a task can land somewhere unexpected. Inside a `ctxloom run` session
the project is pinned, so `cd`-ing into another repo and adding a task still writes to the
session's project. Every mutating command therefore names the store it touched on **stderr**:

```
taskloom: project /home/you/code/ctxloom (swift-amber-falcon)
```

and `taskloom list` prints a `Project:` header above the table. The notice goes to stderr
precisely so it can't corrupt a `--json` pipeline on stdout. If a task seems to have
vanished, read that line before anything else — the usual answer is that it went to the
pinned project, and `taskloom list --project <id>` will find it.

You may also see a warning that a project *moved* (the registry was re-pointed to the new
path, keeping the id and its history) or was *forked* (the tree looked like a copy of a live
project, so a fresh id was minted rather than have two trees share one log).

## Scripting with --json

`list` and `statuses` take `--json`. The output is the task records themselves, with the same
snake_case keys the MCP tools emit, so a script and an agent see identical data.

```sh
# harp ids of everything in progress
taskloom list --status "In Progress" --json | jq -r '.[].harp_id'

# deferred tasks with their revive conditions
taskloom list --status Deferred --json | jq -r '.[] | "\(.harp_id)\t\(.trigger)"'

# is anything still open?
test "$(taskloom list --json | jq 'length')" -eq 0 || echo "work remains"
```

Human-facing chatter (the project notice, move and fork warnings) goes to stderr, and only
the JSON goes to stdout, so these pipelines stay clean without redirection. `taskloom
statuses --json` gives a script the status taxonomy (name, order, `terminal`,
`requires_trigger`) from the source of truth, which beats hardcoding a list of strings that
will drift.

Note that `list` hides `Done`, `Archived`, and `Deferred` unless you pass `--all` or name a
status. A script that counts "open" work gets that for free; a script that expects every task
needs `--all`.
