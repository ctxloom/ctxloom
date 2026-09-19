# Engine capabilities

What ctxloom actually wires per **engine**. Vocabulary is GLOSSARY.md's: an
**engine** is the thing a runner drives; an **agent** is a ctxloom actor (a
profile in action); a **surface** is one managed deliverable (context, MCP,
hooks, commands, settings), and the composed set is a **loadout**.

Engines are registered as descriptors in `internal/lm/backends/registry.go` —
that file is the source of truth for this document. The mock family also
registers there, for tests; the per-engine table below is the registered
production engine. Where a capability is a property of the engine's CLI rather
than of ctxloom, the row says so, so an absence reads as a CLI limitation and
not as a TODO.

## Surfaces per engine

Each engine's writer materializes the loadout into that engine's own native
config. Paths are relative to the runner's working directory unless marked
global.

| Surface | claude-code |
|---|---|
| Context | `CLAUDE.md` |
| MCP | `.mcp.json` |
| Hooks | `.claude/settings.json` |
| Commands (slash commands) | `.claude/commands/` |
| Settings writer | ✓ |
| Out-of-cwd surface placement (concurrency-safe in a shared cwd) | ✓ `--append-system-prompt-file`, `--mcp-config`, `--settings` (commands: **no** — `.claude/commands/` has no redirect flag) |
| Command metadata accepted | description, argument-hint, allowed-tools, model |
| Read-only plan mode enforced by the CLI | ✓ `--permission-mode plan` |
| Statusline / HUD | ✓ (`ctxloom hook hud`) |
| Resolved-model provenance | ✓ (real model from `--output-format json`) |

Hooks and settings fold into one surface wherever the engine keeps its hooks
inside its settings file, as claude does (`.claude/settings.json`).

An engine that accepts a surface only at a well-known path inside the working
directory cannot share that directory between concurrent runs; per-agent
isolation for such an engine needs a private cwd — a worktree or a container
cell, which is what the isolation axes below provide. claude takes every
surface but commands at a path ctxloom chooses, so it needs none of that for
those surfaces.

The `agent.StructuredChat` interface still exists and the runner still
type-asserts for it (`internal/cli/llm_runner_common.go`), but **no shipped
engine implements it** — the only implementation is the mock backend used by the
conformance suites. Engines are driven through their own CLI instead.

## Hook translation

ctxloom emits engine-agnostic hook events (`wire.UnifiedHooks`), and each
engine's writer translates them into that engine's native events through a
route table (`agent.RouteUnifiedHooks`). A route the engine cannot serve is
declared `agent.HookRoute.Unsupported` rather than left absent, so configuring
such a hook prints a warning naming the engine and the kind — the hook is
inert, and you are told so instead of finding out by its never firing. An
engine with no hook mechanism at all declares that on its registry descriptor
(`noHooksReason`) rather than leaving the silence to be discovered.

| Unified event | claude-code |
|---|---|
| `session_start` | `SessionStart` |
| `session_end` | `SessionEnd` |
| `turn_end` | `Stop`, no matcher |
| `pre_tool` | `PreToolUse` |
| `post_tool` | `PostToolUse` |
| `pre_shell` | `PreToolUse` matcher `Bash` |
| `post_file_edit` | `PostToolUse` matcher `Edit\|Write` |

`session_end` and `turn_end` are not interchangeable, and the difference is why
`turn_end` exists. `session_end` fires ONCE, at teardown; `turn_end` fires every
time the agent finishes a response, which is the only point at which a close-out
contract can still be acted on.

No engine honours a matcher on its turn-end event — there is no tool to match
against at a turn boundary. The route declares none, and a hook that carried
one is emitted without it.

## Capabilities that are properties of the CLI

These are wired engine-neutrally on ctxloom's side and lit only where the
engine's CLI offers the hook. They are not bugs or TODOs.

### Statusline / HUD
Claude Code runs an external `statusLine` command and pipes session JSON to it;
ctxloom wires `ctxloom hook hud` there. The HUD command
(`internal/cli/hook_hud.go`) is written engine-neutrally and is ready the
moment another CLI ships a command-backed statusline.

### Resolved-model provenance
Claude's `--output-format json` reports the model that actually produced a
result, so ctxloom records it (distill provenance uses this). An engine that
reports only the *requested* model falls back to the engine name rather than a
fabricated id.

### Command-metadata ceilings
`CommandExport` carries description, argument-hint, allowed-tools, and model.
A CLI that accepts only a subset gets only that subset; unsupported fields are
not emitted for that engine (`internal/lm/backends/commandfiles.go`).

### Out-of-cwd placement
Claude takes each surface from a path ctxloom chooses
(`--append-system-prompt-file`, `--mcp-config`, `--settings`), so concurrent
runs can share one working directory without fighting over config files. Its
commands are the exception even there: `.claude/commands/` has no redirect flag.

## Isolation axes

Engine choice is independent of *where* the engine runs. Two axes meet only at
launch (`isolation.Axes`), and both are defined in `internal/core/config/config.go`.

| Axis | Level | Values | Set by | Governs |
|---|---|---|---|---|
| `workspace` | session | `none` \| `worktree` | `run --workspace`, an `agent_run` spawn's workspace field, or the `workspace` config key | where a session's working directory lives |
| `runtime` | agent | `host` \| `container-rootless` \| `container-rootful` | an agent binding's `runtime:`, or the `runtime` config key | where an agent's engine process executes |

They are two axes rather than one "isolation" setting because they belong to
different things. Needing a private working directory is a property of how a
session is launched; needing a container is a property of the agent. There is
deliberately no "any container" runtime value: rootless and rootful differ in
UID mapping, so a workload can genuinely require one, and an ownership
mismatch is a fatal finding rather than a silent substitution of the other
mode.

`host` is a value on the runtime axis, not a security boundary: a host-runtime
agent's coordinator credential is readable by any other same-uid process
(`/proc/<pid>/environ`), and that credential is identity. Containers are the
actual boundary — see [Isolation](architecture/engines/isolation.md) and
[the trust model](trust-model.md).

## Symbol tools

Symbol intelligence — go-to-definition, find-references, workspace symbol
search — is not a property of the agent. It is a property of the CHECKOUT a
language server rooted itself at, and a server roots once, at start, from the
launching session's working directory.

That single fact is why symbol tools are currently off rather than merely
unconfigured, and it bites in a specific direction worth stating plainly:

**A symbol tool that is rooted at the wrong checkout does not report an error.
It reports that your symbol does not exist.** For a worktree-isolated agent
this is the default discovery verb answering confidently and wrongly, and
neither the agent nor its coordinator can tell from the answer.

### What was measured

Against Claude's own Go LSP plugin, with a canary symbol planted in a git
worktree and absent from the main checkout:

| Operation | Rooted at the launching checkout | Same repo, different worktree |
|---|---|---|
| workspace symbol search | resolves correctly | **empty result — silently wrong** |
| hover / definition | full signature and docs | errors, naming the missing package metadata |

The loud arm is survivable; a caller sees a failure. The silent arm is the
hazard, because "no symbols found" and "that symbol does not exist" are the
same response.

The language server itself names the remedy in its diagnostic — a `go.work`
spanning the other directory. Treat that as unmeasured: it is Go-specific, and
a tracked `go.work` naming machine-specific worktree paths carries the same
portability defect as any generated file that hard-codes one developer's
absolute paths.

Serena was evaluated for the same job and is deliberately UNLINKED for the same
underlying reason. The rationale lives next to the decision, in the `serena`
comments in the profiles under `.ctxloom/profiles/` — that is the authority,
not this page.

### Why a delegated child has none

A ctxloom `agent_run` child is a separate session with its own engine process,
and it is worth being precise about what it does and does not receive, because
the two halves have different answers:

- **MCP reaches it.** Its tools arrive over the runner-terminated MCP surface
  rather than from a config file, so the absence of an `.mcp.json` on its argv
  is not a gap.
- **Settings do not.** Plugin enablement lives in settings, nothing passes a
  settings path on the child's launch, and so no plugin — and therefore no
  symbol tool — reaches it.

This is an unwired seam rather than a limitation of the engine: ctxloom
constructs that launch, and an agent already carries the profile(s) that would
declare what it needs. Delivering it is `agent.SurfaceKind` work aimed at the
delegated-child launch.

Distinguish this from an engine's own IN-PROCESS subagent (Claude Code's task
tool), which launches no MCP server and inherits its parent's connections. That
one genuinely cannot be given its own correctly-rooted server, and it is why
per-agent rooting cannot be fixed at that layer.

### What to trust today

On the `runtime` axis, a container is the only configuration where correct
rooting is structural rather than incidental: the workspace is mounted at one
path and there is no second checkout for a server to prefer. That guarantee is
about ambiguity, not availability — the language server must still be present
in the image, and its cost there is real enough to have been weighed and
rejected once already.

Until then, an agent working in an isolated workspace should treat a symbol
tool's silence as unproven rather than as absence, and reach for text search.

## Sources

- Claude Code: <https://code.claude.com/docs>
- In-repo: [GLOSSARY.md](../GLOSSARY.md) (vocabulary),
  `internal/lm/backends/registry.go` (the engine set),
  [adr/0031-agent-equity-documented-divergences.md](./adr/0031-agent-equity-documented-divergences.md)
