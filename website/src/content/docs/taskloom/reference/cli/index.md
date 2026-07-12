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
- **Plans** — [`plan`](/taskloom/reference/cli/taskloom_plan/), [`run`](/taskloom/reference/cli/taskloom_run/), [`watch`](/taskloom/reference/cli/taskloom_watch/)
- **Agents** — [`mcp`](/taskloom/reference/cli/taskloom_mcp/) serves the same store over MCP; see the [MCP tools reference](/taskloom/reference/mcp-tools/).
- **Utilities** — [`manage`](/taskloom/reference/cli/taskloom_manage/), [`version`](/taskloom/reference/cli/taskloom_version/)

<!-- PROSE PLACEHOLDER (hand-written, separate workstream):
     This page should also carry the narrative that no --help screen can:
       - the status model and when to Defer-with-a-trigger instead of closing
       - project resolution precedence, and how to tell which store you just wrote to
       - piping `--json` into scripts / other tools -->
