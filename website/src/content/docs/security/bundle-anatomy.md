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
| `mcp` | MCP server declarations | **Tier 2** — a binary is launched |
| `hooks` | Lifecycle hooks | **Tier 1** — a shell command line the harness runs |
| `skills` | Agent Skill packages (a directory of files, not inline text) | **Tier 3, with real files on disk** — see below |
| `profiles` | Composition units (which items load together) | Not gated as a definition — see below |

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
| `command` | **A shell command string. The harness executes it.** |
| `matcher` | Regex over tool names — which tool calls it fires on |
| `type` | `command`, `prompt`, or `agent` |
| `prompt` | Prompt text, for the non-command types |
| `timeout` | Seconds |
| `async` | Run in the background |
| `pre_tool_fallback` | For `session_start` hooks: may fire on pre-tool instead, on harnesses with no session-start event |

Events: `pre_tool`, `post_tool`, `session_start`, `session_end`, `turn_end`,
`pre_shell`, `post_file_edit`.

When you approve a hook, your signature covers its **executable surface**: matcher, type,
command, prompt, and the pre-tool-fallback flag. Change any of those and the approval no
longer verifies, so the hook returns to pending and is withheld.

Two exclusions, stated plainly because they are real:

- `timeout` and `async` are **not** covered by your approval. An update that changes only
  those two fields does not re-gate the hook. The command it runs is unchanged, which is why
  this is considered acceptable — but "approving a hook" does not mean "pinning every byte
  of it".
- The **event** is not part of the signed payload either. It is carried by the hook's
  identity instead. This is deliberate: it makes a *rejection* event-agnostic, so rejecting a
  malicious command blocks it wherever it is wired.

A hook's identity is positional — `<bundle>#hooks/<event>/<index>`. Inserting or reordering
hooks shifts later hooks' identities, which drops their approvals back to pending. That
fails safe (more review, never more exposure), but it is a known coarseness; see the
[threat model](/security/threat-model/#what-we-do-not-defend).

## `mcp` — the server binary

| Field | Meaning |
|---|---|
| `command` | **The binary that is launched** |
| `args` | Arguments, in order |
| `env` | Environment handed to it |
| `installation` | Setup instructions |
| `notes` | Human-only |
| `content_hash` | Author-supplied. See below |

Your approval covers `command`, `args`, `env`, and `installation`. `notes` is excluded — it
is never executed and never sent to the agent. Argument order is significant (reordering
`args` is a different server); environment key order is not (the encoding sorts keys).

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
| `llm` (commands) | Per-harness export settings — how the command becomes a slash command |

A command that is exported as a slash command passes the same trust gate as a hook or an MCP
server, at its own choke: a pending or rejected command is not written out as a command at all.

**Distillation matters here.** `content` and `distilled` are *different bytes*, and the
distilled form is bytes an LLM wrote that no human read. So they are approved **separately**:
your signature covers the exact form being exposed. Approving the raw fragment does not
approve its distilled rewrite, and flipping the effective form (via `use_distilled`) re-gates
the item to pending. An approved item cannot be silently replaced by machine-written text.

### The `content_hash` field is not a security field

Bundles carry a `content_hash`. It is author-supplied, it drives re-distillation staleness
checks, and **the trust gate never reads it**. The gate hashes the bytes it is actually about
to expose. An author-written hash is a claim; a signature over bytes is a proof. Do not
mistake the former for the latter.

## `skills` — the package on disk

| Field | Meaning |
|---|---|
| `path` | Directory relative to the bundle, default `skills/<name>` |
| `files` | **Generated manifest**: every sibling file's path, sha256, and POSIX mode |
| `llm` | Per-engine enablement only |
| `tags`, `notes` | Metadata; `notes` is human-only |

A skill is not inline text like a fragment or command — it is a directory: a required
`SKILL.md` (frontmatter + instructions, the part a model reads first) plus arbitrary sibling
files, commonly a `scripts/` folder. Those sibling files are not decorative attachments: the
`files` manifest records each one's POSIX permission mode, and an executable bit that was set
in the authored tree is preserved through signing, transfer, and materialization onto your
disk. A skill that ships `scripts/setup.sh` with the executable bit set puts a real,
runnable shell script on your machine, at a path the agent can invoke by name — not a
metaphor, an actual file with `0755` permissions.

Like every other bundle surface, a skill is trust-gated before it ever reaches disk: a
pending or rejected skill is not materialized. `SKILL.md`'s description has no `content:`
field to distill — the frontmatter description *is* the progressive-disclosure mechanism a
model reads before deciding to pull in the rest of the package — but the package as a whole
still gates the same way everything else does, addressed as `<bundle>#skills/<name>`.

## `profiles` — composition, not content

A bundle can ship profiles: units that say which fragments, commands, MCP servers and hooks
load together.

A **profile definition is not trust-gated**. It is orchestration — a list of what to compose
— and gating a list of names would gate nothing useful. What matters is that every item a
profile pulls in still gates at its own choke: its fragments gate at content assembly, its
MCP servers and hooks gate at the executable choke. A profile cannot launder an item past
review by naming it.

Executables a profile declares *directly* (an inline `hooks:` or `mcp:` block, rather than a
reference to a bundle item) pass that same executable gate before they reach your settings
file.

## What this adds up to

Every executable surface a bundle carries — hooks, MCP servers, exported slash-commands —
and every text surface — fragments, commands — is routed through one decision function, per
item, on the exact bytes about to be exposed. If that decision cannot justify exposure, the
item is silently absent from the agent and counted in a stderr advisory.

That is the defense. It is a review gate, not a safety oracle. It tells you *what* you are
about to run and *who* it came from. Deciding whether to run it is still yours.

Next: [Threat model](/security/threat-model/).
