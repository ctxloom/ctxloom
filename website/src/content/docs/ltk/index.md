---
title: "llm-tool-killer (ltk)"
description: "A companion tool for ctxloom: redirect the commands your AI coding agent runs."
---

An agent runs `go test ./...` directly instead of `just test`, bypassing whatever
your task runner wraps around it — and nothing complains until CI catches what the
direct command missed. `ltk` catches commands like this before they run and turns
them into the right one:

```
go test ./...   ⟶   ✗ "Run tests through the task runner."   →   the agent retries with `just test`
```

**llm-tool-killer** (`ltk`) is a companion tool that ships from the ctxloom repo.
Where ctxloom shapes the **context** an AI coding agent sees, `ltk` guides the
**commands** it runs.

`ltk` is a guardrail against reflexive mistakes, not a security boundary. It stops
an agent from *accidentally* running the wrong command; it does not stop one that's
trying to get around a rule. This is a **cooperative redirect** — an agent
explicitly told to route around a rule can rename the binary or recompile it under
another name. A command `ltk` can't parse is allowed by default, not blocked, and
command matching is approximate enough that a determined-enough wrapper can still
slip past a rule. Treat it as a seatbelt, not a vault: for real isolation, run the
agent in a container.

It's a small static binary the agent calls as a pre-tool hook. It parses the shell
command the agent is about to run and, if one of your rules matches, turns it away
and tells the model what to do instead, so the agent retries the right way rather
than hitting a silent failure or an opaque block.

## Why it pairs with ctxloom

ctxloom weaves the right context into the session: fragments, profiles, skills,
memory. `ltk` keeps the agent on your rails once it starts acting, steering it
toward your task runner and away from commands you'd rather it not run, with the
suggestion you wrote. The agent gets the right context and runs the right
commands.

## What it does, and doesn't

`ltk` parses and understands the command, resolving known variables and re-parsing
trivial wrappers, then matches rules against the real command however it's dressed
up. A substring or regex denylist misfires on quoting and gets bypassed by a
sub-shell or `eval`; this doesn't.

It is a **cooperative redirect, not a sandbox.** An agent instructed to work around
a rule can rename the binary or recompile it under another name. For hard "never,
under any circumstances" isolation, run the agent in a container. What `ltk` does
is make the easy, accidental path the right one, which is where nearly all of the
damage actually comes from.

## A broken config denies everything

This is the one behavior to internalize before you write a rule. If `ltk` cannot
load its config (an unreadable file, a YAML syntax error, an unknown key, a
duplicate rule id, or a typo in the installed hook's `--engine` or `--shell`) it
**denies every tool call it guards**: Bash, PowerShell, Edit, Write, MultiEdit,
NotebookEdit. The agent stops working and relays the parse error to you.

That is deliberate. Both hook hosts treat a hook that exits non-zero as
non-blocking, so an `ltk` that failed loudly on the hook path would silently
disable every rule you wrote and leave you believing you were still guarded. A
guard that fails open is worse than no guard, so `ltk` fails closed and makes the
breakage impossible to miss. If your agent suddenly can't run anything, read the
denial message — it carries the config error.

Two things are deliberately not fail-closed. A missing config is not an error:
with no rules file anywhere, `ltk` allows everything, so installing the binary
without configuring it changes nothing. And when a command is found but cannot be
*parsed* at all, `defaults.on_parse_error` decides, defaulting to `allow`.

## Where the config lives

Rules live in `.ltk/config.yaml`. `ltk` searches the working directory and then
each ancestor, probing five names per directory in order — `.ltk/config.yaml`,
the legacy flat `.ltk.yaml`, `llm-tool-killer.yaml`, `.llm-tool-killer.yaml`, and
`.config/llm-tool-killer.yaml` — and loads the first one it finds. There is no
layering — exactly one config is ever in effect, so a subdirectory can override a
repo's rules wholesale but can never add to them.

The ancestor walk exists because hook hosts disagree about the working directory
they hand a hook. Claude Code runs them at the project root; Antigravity runs them
inside `<workspace>/.agents`. A search of the working directory alone would miss
your rules under Antigravity and quietly fall back to allowing everything.

The walk stops at the first ancestor containing a `.git` *directory*. A `.git`
*file* — the gitfile pointer a submodule or a linked worktree gets instead of a
real `.git` directory — is deliberately **not** a boundary: stopping there would
make a superproject's rules silently vanish inside its submodules, and "silently
allow everything" is the wrong failure direction for a guard. So in a worktree or
a submodule, the search keeps climbing past it. This still finds your rules
first if the worktree or submodule carries its own `.ltk/config.yaml` — the walk
takes the nearest config — but if it doesn't, an ancestor config *will* apply
even though it lives outside what looks like the repository root.

## Install

```sh
go install github.com/ctxloom/ctxloom/cmd/ltk@latest
# or build from source in the ctxloom repo: just build-ltk
```

`manage install` auto-detects your agent (via a `.claude/` directory, for
instance), merges the hook into its settings without disturbing the rest of the
file, and scaffolds a starter `.ltk/config.yaml`:

```sh
ltk manage install                     # write the agent hook + .ltk/config.yaml
ltk manage install --no-default-rules  # scaffold an empty rules file instead
ltk manage uninstall                   # cleanly remove the hook again
```

An existing rules file is never clobbered. `manage install` keeps it and says so,
unless you pass `--force`, which backs the old one up to `.ltk/config.yaml.bak`
first.

`ltk manage install --print` is a dry run. It prints the merged settings to stdout
and writes nothing at all, including no rules file. Use it to preview the hook,
not to set a project up.

Commit `.ltk/config.yaml` alongside your code.

## Next

- **[Writing rules](/ltk/rules/)** — command matching, file rules, modes, and how
  to test a rule before you trust it.
- **[CLI reference](/ltk/reference/cli/)** — every command, generated from the binary.

:::note
Claude Code and Antigravity CLI (`agy`) are the agents `ltk` can install a hook
for today.
:::
