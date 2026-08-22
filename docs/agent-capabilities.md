# Engine capabilities and parity

What ctxloom actually wires per **engine**. Vocabulary is GLOSSARY.md's: an
**engine** is the thing a runner drives (claude-code, codex, kiro, antigravity,
or a generic ACP client); an **agent** is a ctxloom actor (a profile in action);
a **surface** is one managed deliverable (context, MCP, hooks, commands,
settings), and the composed set is a **loadout**.

Engines are registered as descriptors in `internal/lm/backends/registry.go` —
that file is the source of truth for this document. `mock` also registers there,
for tests.

Claude Code has the largest CLI surface, so it is the reference: where a
capability exists everywhere, ctxloom implements it everywhere; where only one
engine's CLI supports it, the rest are N/A by CLI limitation and are listed
under "Documented divergences" below.

> Status (GLOSSARY.md): `codex` and `kiro` are implemented and hermetically
> tested, but live operation is untested — no codex or kiro account exists on
> any dev host. Treat their rows as derived from vendor docs plus hermetic
> tests, not from a live run.

## Surfaces per engine

Each engine's writer materializes the loadout into that engine's own native
config. Paths are relative to the runner's working directory unless marked
global.

| Surface | claude-code | antigravity | codex | kiro | acp |
|---|---|---|---|---|---|
| Context | `CLAUDE.md` | `.agents/AGENTS.md` | context file + SessionStart hook | `.kiro/steering/ctxloom-context.md` (auto-loaded) | in-band (lead fragment) |
| MCP | `.mcp.json` | `.agents/mcp_config.json` | `.codex/config.toml` | `.kiro/settings/mcp.json` | — |
| Hooks | `.claude/settings.json` | `.agents/hooks.json` | `.codex/config.toml` | `.kiro/agents/<name>.json` | — |
| Commands (slash commands) | `.claude/commands/` | `.agents/skills/` | `~/.codex/prompts/` (**global**) | `.kiro/skills/<name>/SKILL.md` | — |
| Settings writer | ✓ | ✓ | ✓ | ✓ | **none** |
| Out-of-cwd surface placement (concurrency-safe in a shared cwd) | ✓ `--append-system-prompt-file`, `--mcp-config`, `--settings` (commands: **no**) | **N/A** (no flag) | **N/A** (no flag) | **N/A** (no flag) | n/a (no surfaces) |
| Command metadata accepted | description, argument-hint, allowed-tools, model | description | description, argument-hint | description | — |
| Read-only plan mode enforced by the CLI | ✓ `--permission-mode plan` | — | ✓ `exec --sandbox read-only --ask-for-approval never` | — | — |
| Statusline / HUD | ✓ (`ctxloom hook hud`) | **N/A** | **N/A** | **N/A** | **N/A** |
| Resolved-model provenance | ✓ (real model from `--output-format json`) | **N/A** | **N/A** | **N/A** | **N/A** |

Hooks and settings fold into one surface wherever the engine keeps its hooks
inside its settings file: claude (`.claude/settings.json`), codex
(`.codex/config.toml`), kiro (the agent JSON).

Only claude accepts every surface at a path ctxloom chooses. Codex, kiro, and
antigravity expose no out-of-cwd redirect, so each of their surfaces is a
well-known write into the working directory. Concurrent per-agent isolation on
those engines therefore needs a private cwd — a worktree or a container cell,
which is what the isolation axes below provide.

The generic `acp` engine deliberately registers no settings writer and no
command exports (`registry.go`, the `acp` descriptor). A generic ACP client has
no known native config format to materialize, so it opts out with an empty
surface set, and its context rides in-band. It offers structured chat and
headless oneshot, never a TUI.

Structured chat is ACP everywhere it exists. claude-code, codex, and kiro each
implement `agent.StructuredChat` by delegating to `acp.NewChatDriver` from their
own backend (`internal/{claude,codex,kiro}/chat.go`), so materialization stays
with the engine's own writer while the chat transport is shared. Antigravity has
no structured-chat path.

## Hook translation

ctxloom emits seven engine-agnostic hook events (`wire.UnifiedHooks`). Each
engine's writer translates them into that engine's native events. opencode is
absent from the table on purpose: it has no hook mechanism at all, which its
registry descriptor declares (`noHooksReason`) rather than leaving the silence
to be discovered.

| Unified event | claude-code | codex | kiro |
|---|---|---|---|
| `session_start` | `SessionStart` | `SessionStart` | `agentSpawn` |
| `session_end` | `SessionEnd` | **dropped, with a warning** (no such event) | **dropped, with a warning** (no such event) |
| `turn_end` | `Stop`, no matcher | `Stop`, matcher dropped | `stop`, matcher dropped |
| `pre_tool` | `PreToolUse` | `PreToolUse` | `preToolUse` |
| `post_tool` | `PostToolUse` | `PostToolUse` | `postToolUse` |
| `pre_shell` | `PreToolUse` matcher `Bash` | `PreToolUse` matcher `Bash` | `preToolUse` matcher `execute_bash` |
| `post_file_edit` | `PostToolUse` matcher `Edit\|Write` | `PostToolUse` matcher `Edit\|Write` | `postToolUse` matcher `fs_write` |

`session_end` and `turn_end` are not interchangeable, and the difference is why
`turn_end` exists. `session_end` fires ONCE, at teardown; `turn_end` fires every
time the agent finishes a response, which is the only point at which a close-out
contract can still be acted on. kiro's `stop` used to be fed from `session_end`,
which made one config fire per-session on claude-code and per-TURN on kiro with
no warning either way; kiro has no session-end trigger at all, and now says so.

No engine honours a matcher on its turn-end event — there is no tool to match
against at a turn boundary. codex goes further and forces the matcher to `None`
before computing a hook's trust identity, so writing one would produce config
text its own seeded trust record does not cover. Both writers drop the matcher
and warn once.

## Documented divergences (N/A by CLI limitation)

These are deliberate non-features. The underlying CLI cannot support them, so
ctxloom does not pretend to. They are not bugs or TODOs.

### 1. Statusline / HUD — claude-code only
Claude Code runs an external `statusLine` command and pipes session JSON to it;
ctxloom wires `ctxloom hook hud` there. No other engine exposes a
command-backed statusline. The HUD command (`internal/cli/hook_hud.go`) is
written engine-neutrally and is ready the moment another CLI ships one; only
claude's writer wires it today.

### 2. Resolved-model provenance — claude-code only
Claude's `--output-format json` reports the model that actually produced a
result, so ctxloom records it (distill provenance uses this). Every other engine
reports the *requested* model, falling back to the engine name rather than a
fabricated id (`internal/claude/claudecode.go`, `internal/codex/backend.go`,
`internal/antigravity/backend.go`, `internal/kiro/backend.go`).

### 3. SessionEnd — not on codex
Codex's hook set has no SessionEnd-equivalent event, so unified `session_end`
hooks are not emitted for it (`internal/codex/settings.go`). The gap is declared
in codex's route table (`agent.HookRoute.Unsupported`) rather than left as an
absent route, so configuring a `session_end` hook and running codex prints a
warning naming the engine and the kind — the hook is inert, and you are told so
instead of finding out by its never firing. Antigravity accepts the entry but
never fires it, which costs nothing and lights up if a future agy adds the
event.

### 4. Command-metadata ceilings
`CommandExport` carries description, argument-hint, allowed-tools, and model.
Each CLI accepts only a subset (see the table above). Unsupported fields are not
emitted for that engine (`internal/lm/backends/commandfiles.go`).

### 5. Codex prompts are global
Codex discovers custom prompts only in the global `~/.codex/prompts`, so codex
slash commands are inherently cross-project — unlike the workspace-scoped
`.claude/commands` or `.agents/skills`. ctxloom writes into a cell-scoped
`CODEX_HOME` so an isolated run does not fight the host's prompts, and a
manifest scopes its own cleanup.

### 6. Out-of-cwd placement — claude-code only
Claude takes each surface from a path ctxloom chooses
(`--append-system-prompt-file`, `--mcp-config`, `--settings`), so concurrent
runs can share one working directory without fighting over config files. Its
commands are the exception even there: `.claude/commands/` has no redirect flag.
Antigravity, codex, and kiro expose no such flag for any surface
(`internal/{antigravity,codex,kiro}/surfaces.go`), so concurrent per-agent runs
on those engines need a private cwd.

## Isolation axes

Engine choice is independent of *where* the engine runs. Two axes meet only at
launch (`isolation.Axes`), and both are defined in `internal/config/config.go`.

| Axis | Level | Values | Set by | Governs |
|---|---|---|---|---|
| `workspace` | session | `none` \| `worktree` | `run`/`acp --workspace`, an `agent_run` spawn's workspace field, or the `workspace` config key | where a session's working directory lives |
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

## Sources

- Claude Code: <https://code.claude.com/docs>
- OpenAI Codex: <https://developers.openai.com/codex>
- In-repo: [GLOSSARY.md](../GLOSSARY.md) (vocabulary),
  `internal/lm/backends/registry.go` (the engine set),
  [adr/0031-agent-equity-documented-divergences.md](./adr/0031-agent-equity-documented-divergences.md)
