---
title: "CLI Reference"
sidebar:
  order: 0
---

Complete reference for all `ltk` commands.

The per-command pages in this section are **generated** from the command definitions in `cmd/ltk` (`just gen-docs`) — the same text as `ltk <command> --help` and `man ltk`, so they always match the binary. This page keeps the narrative that doesn't fit a `--help` screen.

## Command groups

- **The hook** — [`evaluate`](/ltk/reference/cli/ltk_evaluate/) reads a tool-call payload on stdin and emits an allow/deny decision. Agents invoke it; you rarely do.
- **Authoring rules** — [`check`](/ltk/reference/cli/ltk_check/) validates a rules file and dry-runs a command against it.
- **Wiring it up** — [`manage install`](/ltk/reference/cli/ltk_manage_install/) / [`manage uninstall`](/ltk/reference/cli/ltk_manage_uninstall/) add and remove the hook from your agent's config.
- **Utilities** — [`version`](/ltk/reference/cli/ltk_version/).

<!-- PROSE PLACEHOLDER (hand-written, separate workstream):
     This page should also carry the narrative that no --help screen can:
       - the rule model (deny-first-match, message/suggest round-trip to the model)
       - how command rules differ from file (match.path) rules
       - the authoring loop: write a rule, `ltk check` it, commit it
     The full rule syntax currently lives only in docs/RULES.md in the repo and
     has never been published to the site — porting it is a prose task, not a
     generated one. -->
