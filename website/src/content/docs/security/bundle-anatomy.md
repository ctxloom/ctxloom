---
title: "What a bundle can do to you"
---

Before you trust a bundle, you should know exactly what a bundle is allowed to contain. Not
the friendly summary — the field list. This page is that list, and for each field, what it
can execute.

The short version: a bundle can put a **shell command** in your harness's settings file, a
**server binary** on your process table, and **arbitrary instructions** into an agent that
already holds your credentials. Everything else is metadata.

## The top level

A bundle is one YAML document. These are all the keys it may carry:

| Key | What it is | What it can execute |
|---|---|---|
| `version` | Version string shared by every item | Nothing |
| `tags` | Tags, merged into item tags | Nothing |
| `author`, `description`, `notes` | Metadata. `notes` is human-only | Nothing |
| `installation` | Setup instructions, shown to a human on install | Nothing on its own |
| `fragments` | Prose injected into agent context | **Tier 3** — instructions to an LLM holding your shell |
| `commands` | Prose invoked on demand; exportable as slash commands | **Tier 3** |
| `mcp` | MCP server declarations | **Tier 2** — a binary is launched, or a network endpoint is dialed |
| `hooks` | Lifecycle hooks | **Tier 1** — a shell command line the harness runs |
| `skills` | Agent Skill packages (a directory of files, not inline text) | **Tier 3, with real files on disk** — see below |
| `profiles` | Composition units (which items load together) | Nothing on its own — see below |

There is one more field, and it is the one that matters most: a bundle's **verified
publisher identity**. You cannot write it. It is not a YAML key. Putting
`signer: releases@ctxloom.dev` into a bundle file does exactly nothing — the field is
unexported and explicitly excluded from deserialization, and the only thing that can set it
is a load path that has already cryptographically verified a signature against your trust
root. A bundle cannot name its own signer. Anyone can write a string into a file; nobody can
forge a signature.

## `hooks` — the shell command line

The tier-1 surface. Each hook may declare:

| Field | Meaning |
|---|---|
| `command` | **A shell command string. The harness executes it.** With `args`, the executable to run. |
| `args` | Exec form: `command` is spawned directly with these arguments, and no shell parses either |
| `matcher` | Regex over tool names — which tool calls it fires on |
| `type` | `command`, `prompt`, or `agent` |
| `prompt` | Prompt text, for the non-command types |
| `timeout` | Seconds |
| `async` | Run in the background |
| `pre_tool_fallback` | For `session_start` hooks: may fire on pre-tool instead, on harnesses with no session-start event |

Events: `pre_tool`, `post_tool`, `session_start`, `session_end`, `turn_end`,
`pre_shell`, `post_file_edit`, `turn_start`.

A first pin, and every pin `deps upgrade` would move, shows each hook's command line (an
exec-form hook's `command` and every one of its `args`) before and after, so a change to what
a hook runs is visible before you apply it.

A hook's identity is positional — `<bundle>#hooks/<event>/<index>`.

## `mcp` — the server binary

| Field | Meaning |
|---|---|
| `command` | **The binary that is launched** |
| `args` | Arguments, in order |
| `env` | Environment handed to it |
| `url` | **The endpoint of a network-hosted server**; its scheme names the transport |
| `headers` | HTTP headers sent when dialing `url`, such as `Authorization` |
| `installation` | Setup instructions |
| `tags`, `served_by` | Routing, evaluated by ctxloom; never executed |
| `notes` | Human-only |

The pin disclosure shows `command`, `args` and `url` in clear, and the names of `env` and
`headers` with each value only as a fingerprint. `notes` is never executed and never sent to
the agent. Argument order is significant (reordering `args` is a different server).

## `fragments` and `commands` — the prose

| Field | Meaning |
|---|---|
| `content` | The raw authored text |
| `distilled` | An LLM-compressed rewrite of `content` |
| `distilled_by` | Which model produced it |
| `no_distill` | Suppress distillation for this item |
| `content_hash` | Author-supplied. See below |
| `tags`, `notes`, `installation` | Metadata; `notes` is human-only |
| `description` (commands) | One-line summary |
| `exports` (commands) | Per-engine blocks, opaque to ctxloom — each engine decodes its own to decide how the command becomes a slash command |

**Distillation matters here.** `content` and `distilled` are *different bytes*, and the
distilled form is bytes an LLM wrote that no human read.

### The `content_hash` field is not a security field

Bundles carry a `content_hash`. It is author-supplied, it drives re-distillation staleness
checks, and **nothing that decides delivery reads it**. An author-written hash is a claim; a
signature over bytes is a proof. Do not mistake the former for the latter.

## `skills` — the package on disk

| Field | Meaning |
|---|---|
| `path` | Directory relative to the bundle, default `skills/<name>` |
| `files` | **Generated manifest**: every sibling file's path, sha256, and POSIX mode |
| `exports` | Per-engine blocks, opaque to ctxloom; the name and description live in `SKILL.md` |
| `tags`, `notes` | Metadata; `notes` is human-only |

A skill is not inline text like a fragment or command — it is a directory: a required
`SKILL.md` (frontmatter + instructions, the part a model reads first) plus arbitrary sibling
files, commonly a `scripts/` folder. Those sibling files are not decorative attachments: the
`files` manifest records each one's POSIX permission mode, and an executable bit that was set
in the authored tree is preserved through signing, transfer, and materialization onto your
disk. A skill that ships `scripts/setup.sh` with the executable bit set puts a real,
runnable shell script on your machine, at a path the agent can invoke by name — not a
metaphor, an actual file with `0755` permissions.

`SKILL.md`'s description has no `content:` field to distill — the frontmatter description
*is* the progressive-disclosure mechanism a model reads before deciding to pull in the rest of
the package. A skill is addressed as `<bundle>#skills/<name>`, and a changed script is shown
as a diff when `deps upgrade` would move its pin.

## `profiles` — composition, not content

A bundle can ship profiles: units that say which fragments, commands, MCP servers and hooks
load together.

A profile definition is orchestration — a list of what to compose. A remote profile may refer
only to bundles in its own repository, so a profile cannot pull content from a repository you
did not add.

## What this adds up to

Adding a repository is the decision that lets its bundles reach your agent. After that, what
changes is shown before it lands: a first pin lists everything the bundle carries, and
`deps upgrade` shows every item, hook command and MCP server a move would change, and a diff of
every changed script, before `--yes` applies it. It tells you *what* you are about to run.
Deciding whether to run it is still yours.

Next: [Threat model](/security/threat-model/).
