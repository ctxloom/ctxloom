# 0031 — Agent equity: documented divergences are N/A by CLI limitation

**Date:** 2026-06-07.

## Status

Accepted.

## Context

After extracting claude/gemini/codex into their own modules on the shared
`agent.LaunchBackend` core, we drove gemini and codex toward feature parity with
claude (the most complete agent CLI). Most gaps closed — fault-tolerant settings
writes, atomic write+backup, full hook-event coverage, MCP, model-selection
flags, distill isolation, session registration, command frontmatter where
supported.

A handful of claude features cannot be matched because the gemini/codex CLIs
don't expose the underlying mechanism. Left undocumented, these read as bugs or
unfinished work and invite repeated re-investigation.

## Decision

Treat the following as **N/A by CLI limitation**, not as TODOs. Do not build
shims that fake them. They are recorded in
[docs/agent-capabilities.md](../agent-capabilities.md):

1. **Statusline/HUD — Claude only.** Gemini has no `statusLine` command setting;
   codex has only a fixed built-in status line (command-backed is an open FR). The
   HUD command was generalized to be agent-neutral and stays wired to Claude only.
2. **Resolved-model provenance — Claude only.** Gemini's JSON `stats.model` is
   turn counts; codex's `exec --json` omits the model (openai/codex#14736). The
   others report the requested/default model.
3. **Codex SessionEnd** — codex has no such hook event; unified SessionEnd is
   dropped for codex.
4. **Command-metadata ceilings** — gemini commands carry description only; codex
   prompts carry description + argument-hint; only claude carries the full
   allowed-tools/model set. Unsupported fields aren't emitted.
5. **Codex prompts are global** (`~/.codex/prompts`, no project scope) and the
   custom-prompt mechanism is deprecated in favor of codex "skills".

Related: the codex module is implemented entirely against OpenAI's published docs
and is **untested against a real codex binary** (no codex on the Linux dev
platform) — see `codex/main/README.md`.

## Consequences

Parity is "as close as each CLI allows," not identical. A distill run on
gemini/codex records the requested model rather than the resolved one; codex users
get no ctxloom HUD and global (cross-project) slash commands; codex behavior is
unverified until smoke-tested on a machine with codex.

Equity is locked in by the tag-gated conformance suite
(`just test-conformance`, `internal/lm/conformance/`), which asserts the shared
`agent.SettingsWriter` contract across all three agents so the *supported*
capabilities can't silently drift.

**Revive triggers:**
- gemini or codex ships a command-backed statusline → wire `ctxloom hook hud`
  (the HUD is already agent-neutral).
- codex `exec --json` adds the model (openai/codex#14736) → implement codex
  resolved-model provenance.
- a live codex smoke test passes → drop the "untested" caveat.
- OpenAI removes custom prompts → migrate codex command export to skills.
