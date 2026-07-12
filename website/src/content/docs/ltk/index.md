---
title: "llm-tool-killer (ltk)"
description: "A companion tool for ctxloom: redirect the commands your AI coding agent runs."
---

[**llm-tool-killer**](https://github.com/ctxloom/ctxloom/tree/main/docs/ltk) (`ltk`) is a companion tool that ships from the ctxloom repo. Where ctxloom shapes the **context** an AI coding agent sees, `ltk` guides the **commands** it runs.

It's a small static binary the agent calls as a **pre-tool hook**: it parses the shell command the agent is about to run and, if one of your rules matches, turns it away and tells the model what to do instead — so the agent retries the right way rather than hitting a silent failure or an opaque block.

```
go test ./...   ⟶   ✗ "Run tests through the task runner."   →   the agent retries with `just test`
```

## Why it pairs with ctxloom

- **ctxloom** weaves the right context into the session — fragments, profiles, prompts, memory.
- **ltk** keeps the agent on your rails once it starts acting — steering it toward your task runner and away from commands you'd rather it not run, with the suggestion you provide.

Together, the agent gets the right context *and* runs the right commands.

## What it does (and doesn't)

`ltk` **parses and understands** the command — resolving known variables and re-parsing trivial wrappers — then matches rules against the *real* command, however it's dressed up. That's far more robust than a substring/regex denylist that misfires on quoting or gets bypassed by a sub-shell or `eval`.

It is a **cooperative redirect, not a sandbox.** If you instruct the agent to work around a rule, it can. For hard "never, under any circumstances" isolation, run the agent in a container — `ltk` makes the *easy, accidental* path the right one.

## Reference

- **[CLI reference](/ltk/reference/cli/)** — every command, generated from the binary.

<!-- PROSE PLACEHOLDER (hand-written, separate workstream):
     The rule syntax and behaviour still live only in docs/RULES.md in the repo and
     have never been published to this site. A "Writing rules" page under this
     section is the missing piece — this overview points at GitHub for it today. -->

## Install

```sh
go install github.com/ctxloom/ctxloom/cmd/ltk@latest
# or build from source in the ctxloom repo: just build-ltk
```

Register the hook and scaffold a starter rules file. `manage install` auto-detects your agent (e.g. a `.claude/` directory) and merges the hook non-destructively:

```sh
ltk manage install         # write the agent hook + .ltk/config.yaml
ltk manage install --print # dry run: show the merged config, write nothing
ltk manage uninstall       # cleanly remove the hook again
```

Commit `.ltk/config.yaml` alongside your code. Rules are YAML; the first matching `deny` wins and returns your `message`/`suggest` to the model.

:::note
Claude Code and Antigravity CLI (`agy`) are the supported agents today; Codex is planned. See the [ltk docs](https://github.com/ctxloom/ctxloom/tree/main/docs/ltk) for the full rule syntax and behavior.
:::
