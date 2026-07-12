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

There is no `ltk init`. A project is set up by `manage install`, which registers
the hook and scaffolds the rules file in one step.

## The rule model

Rules are tested in file order against every command in the parsed command graph.
The first matching `deny` wins and the search stops. A matching `allow` also stops
the search, clearing that command, which means an earlier `allow` shadows a later
`deny`. Order is the only precedence there is — there are no priorities, weights,
or specificity scores to reason about, so a rule that never seems to fire is
usually sitting below an `allow` that caught the command first.

When a deny fires, the rule's `message` and `suggest` travel back to the model as
the reason the tool call was refused. That round-trip is the whole point of the
tool. The agent doesn't see an opaque permission error, it sees "Use `just test`."
along with the replacement command, and retries with it. A rule whose `message` is
blank or vague wastes the mechanism — write the message as an instruction to the
model, because that is exactly what it becomes.

## Command rules and file rules

A rule guards one of two things, never both. Combining them is a config error.

A command rule matches with `match.command` (plus the optional `args_any`,
`args_all`, `unless`, and `shells` refinements) and fires on shell tool calls. Its
pattern is a token-classified argv prefix, not a glob and not a regex — a
distinction worth reading [Writing rules](/ltk/rules/) for, since a pattern
written as though it were a glob typically matches nothing at all and fails
silently.

A file rule matches with `match.path` and fires on the agent's editing tools
(Edit, Write, MultiEdit, NotebookEdit). Here the patterns *are* real globs, with
`**` spanning directories, a trailing slash meaning a whole subtree, and the
`@submodules` sentinel standing in for every submodule in `.gitmodules`. File
rules only fire if the installed hook is registered for the editing tools; the
matcher `manage install` writes covers them.

## The authoring loop

Write a rule, check it, commit it.

```sh
# 1. add a rule to .ltk/config.yaml, then ask ltk what it would do
ltk check --command 'go test ./...' --format json
# → {"decision":"deny","message":"Use `just test`.","suggestion":"just test"}

# 2. confirm the form you meant to keep still passes
ltk check --command 'just test'
# → allow

# 3. commit .ltk/config.yaml alongside the code it guards
```

`check` is the authoring surface and `evaluate` is the hook, and they differ in
one way that matters while you're iterating: `check` fails loud. A broken config
exits non-zero and prints the error. On the hook path, the same broken config
instead denies every guarded tool call, because a guard that errors out would be
treated as non-blocking by the host and silently disable itself. So test with
`check` — it will tell you the config is wrong instead of quietly locking your
agent out of its tools.

Always check the allow case too. A rule that matches everything and a rule that
matches nothing both look like success if you only ever check the command you
meant to block.
