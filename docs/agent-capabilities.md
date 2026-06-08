# Agent capabilities & parity

ctxloom drives three coding-agent CLIs as first-class backends — **claude-code**
(Anthropic), **gemini** (Google), **codex** (OpenAI) — plus a `mock` used in
tests. All three are built on the shared `agent.LaunchBackend` core
(`github.com/ctxloom/shared/agent`) and live in their own modules
(`github.com/ctxloom/{claude,gemini,codex}`).

Claude Code has the largest CLI surface, so it's the **reference**: where a
capability exists everywhere we implement it everywhere; where only Claude's CLI
supports it, the others are **N/A by CLI limitation**, documented below. This is
the companion to [hooks-comparison.md](./hooks-comparison.md) (which compares the
raw hook-event sets) — this doc is about what ctxloom actually wires per agent.

> ⚠️ **Codex is implemented against docs and is untested.** Every codex-specific
> format/flag here is derived from the published OpenAI Codex CLI documentation
> and has not been run against a real codex binary (the maintainer's Linux dev
> platform has no codex access). Treat codex rows as provisional. See
> `codex/main/README.md`.

## Capability matrix

| Capability | claude-code | gemini | codex |
|---|---|---|---|
| Context injection (context file + SessionStart hook) | ✓ | ✓ | ✓ |
| Hooks: SessionStart, Pre/PostTool | ✓ | ✓ | ✓ |
| Hooks: PreShell→Bash, PostFileEdit→Edit\|Write | ✓ | ✓ | ✓ |
| Hooks: SessionEnd | ✓ | ✓ | **N/A** (no such event) |
| MCP servers + ctxloom auto-register | ✓ `.mcp.json` | ✓ `.gemini/settings.json` | ✓ `.codex/config.toml` |
| Fault-tolerant settings load + atomic write/backup | ✓ | ✓ | ✓ |
| Slash commands / custom prompts | ✓ `.claude/commands` (project) | ✓ `.gemini/commands` (project) | ✓ `~/.codex/prompts` (**global**) |
| Command metadata | description, argument-hint, allowed-tools, model | description only | description, argument-hint |
| Model selection flag → CLI | ✓ `--model` | ✓ `-m` | ✓ `--model` |
| Distill / minimal-mode isolation | full (no tools/MCP/system-prompt) | `--approval-mode plan` | `exec --sandbox read-only --ask-for-approval never` |
| Session registration (TranscriptPathFromHook) | ✓ (computed) | ✓ (from hook) | ✓ (`transcript_path` from hook) |
| Session-history parsing | ✓ JSONL | ✓ JSONL | ✓ rollout JSONL |
| Resolved-model provenance (real model from output) | ✓ | **N/A** | **N/A** |
| Statusline / HUD | ✓ | **N/A** | **N/A** |

## Documented divergences (N/A by CLI limitation)

These are deliberate non-features: the underlying CLI can't support them, so
ctxloom doesn't pretend to. They are not bugs or TODOs.

### 1. Statusline / HUD — Claude only
Claude Code runs an external `statusLine` command and pipes session JSON to it;
ctxloom wires `ctxloom hook hud` there. Gemini has no `statusLine` setting at all.
Codex only has a built-in `[tui].status_line` with a fixed item list (no
command-backed statusline) — that's an open feature request
([openai/codex#20043](https://github.com/openai/codex/issues/20043),
[#20140](https://github.com/openai/codex/issues/20140),
[#16921](https://github.com/openai/codex/issues/16921)). The HUD command
(`cmd/hook_hud.go`) is written agent-neutrally and is ready the moment another CLI
ships a command statusline; only Claude's writer wires it today.

### 2. Resolved-model provenance — Claude only
Claude's `--output-format json` reports per-model usage, so ctxloom records the
model that actually produced a result (e.g. for distill provenance). Gemini's
`--output-format json` `stats.model` is *turn counts*, not a model name; codex's
`exec --json` omits the model entirely
([openai/codex#14736](https://github.com/openai/codex/issues/14736)). Gemini and
codex therefore report the requested/default model.

### 3. SessionEnd hook — not on codex
Codex's hook set has no SessionEnd-equivalent event (SessionStart,
UserPromptSubmit, Pre/PostToolUse, Stop). Unified `SessionEnd` hooks are emitted
for claude/gemini and dropped for codex.

### 4. Command-metadata ceilings
The host's `CommandExport` carries description, argument-hint, allowed-tools, and
model. Each CLI accepts only a subset: claude (all four), codex (description +
argument-hint), gemini (description only). Unsupported fields are simply not
emitted for that agent.

### 5. Codex prompts are global, and the mechanism is deprecated
Codex discovers custom prompts only in the **global** `~/.codex/prompts` (top
level), so codex slash commands are inherently global/cross-project — unlike the
project-scoped `.claude/commands` / `.gemini/commands`. ctxloom writes there with
a manifest scoping its own cleanup. OpenAI also marks custom prompts deprecated in
favor of "skills"; migrating codex command export to skills is a future task.

## Sources
- Claude Code: <https://code.claude.com/docs>
- Gemini CLI: <https://geminicli.com/docs> (custom-commands, headless)
- OpenAI Codex: <https://developers.openai.com/codex> (config-reference, hooks,
  mcp, custom-prompts, cli/reference)
- In-repo: [hooks-comparison.md](./hooks-comparison.md),
  [adr/0031-agent-equity-documented-divergences.md](./adr/0031-agent-equity-documented-divergences.md)
