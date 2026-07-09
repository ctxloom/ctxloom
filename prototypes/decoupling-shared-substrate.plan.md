# Decoupling ctxloom / ltk / ctxtask over a shared `ctxloom/*` substrate

> **Status: SUPERSEDED by the monorepo consolidation.** The agent substrate lives at `internal/shared/agent` and per-engine packages at `internal/{claude,codex,antigravity}` in a single module. The extract-to-separate-modules and lazy-session-binding ideas were NOT adopted (the session-bind hook is retained), and the gemini-CLI engine was dropped in favor of antigravity + codex. Historical body retained below.

Status: draft · 2026-06-05 · session `tiny-loud-lark`
Graduated into `internal/agent` (P0 steps 1–3 shipped): `CanonicalJSON`, `IsManaged`, `SettingsWriter`/`SettingsStatus`. (The owner-predicate prototype has been deleted as superseded.)

## Goal

Three tools that are **independently usable** but **interoperate well**, built on a
thin shared substrate. Invert the earlier "ctxloom absorbs ltk" idea: no tool
depends on another at compile time; they meet only at data + protocol seams (the
agent settings file, the `.ctxloom` workspace dir, and MCP).

- **ctxloom** — context assembly (bundles/profiles/fragments/prompts, remotes, MCP context tools, hook application).
- **ltk** — pre-tool command/edit gating.
- **ctxtask** — the task tracker, carved out of ctxloom (module `github.com/ctxloom/ctxtask`).

Everything namespaces under the `ctxloom` GitHub org as independently-usable Go
modules (`github.com/ctxloom/<name>`), imported by module path + semver tags — a
tool depends on a lib exactly like any third-party dependency.

```
tools:        ctxloom         ltk         ctxtask
                  \            |            /
core libs:        ctxloom/agent      ctxloom/sessions
                  (interfaces +       (project root +
                   predicate +         session identity,
                   reconcile)          IDs via ctxloom/harp)
                   /          \
agents:   ctxloom/claude   ctxloom/gemini   (implement ctxloom/agent)
```

Session *identity* is substrate (a lib, not a tool); so is the **agent contract**.
Tools stay engine-agnostic — they write hooks through `ctxloom/agent` and the
right concrete agent is selected at runtime. Independence: each tool works with
zero others installed. Interop: when several are present they coexist in one
`.claude/settings.json` and one `.ctxloom/`, each reconciling only its own state.

## Resolved decisions

- **Pull the agents out, not just the harness.** ctxloom already has the polymorphism: `Backend` (launch/run, `interfaces.go`), `SettingsWriter` (hooks/settings, `hooks.go`), per-engine impls (`claudecode.go`/`gemini.go`, `ClaudeCodeHookWriter`/`GeminiHookWriter`, `*_capabilities.go`), and the normalized `UnifiedHooks`. A standalone "harness-config" would be an artificial settings-only slice across that. Instead: an engine-agnostic core `ctxloom/agent` + full per-agent packages `ctxloom/claude` / `ctxloom/gemini`. The settings facet (`SettingsWriter`) stays separable from the launch facet (`Backend`) so ltk uses the former without dragging in the latter.
- **Shared `.ctxloom`** — `ctxloom/sessions` owns the layout (below). The only per-tool dir is ltk's rules (`.ctxloom/ltk/`, else `.ltk/` standalone). **ctxtask gets no new dir** — its store is already `~/.ctxloom/tasks/<project-id>.jsonl` (+ `sessions/<harp>/tasks.md`), resolved by `ctxloom/sessions`; ctxtask asks the lib for its path.
- **Packaging** — repo + Go module per package under the org; semver tags (versionator). Cross-repo local dev via `go.work`; private repos need `GOPRIVATE=github.com/ctxloom/*`.
- **Owner predicate** — prototyped, lives in `ctxloom/agent` (engine-agnostic; see below).
- **Hook ownership** — `inject-context` (SessionStart) + `hud` (statusLine) → ctxloom; `stamp-plan` (PostFileEdit) → ctxtask. **No `session-bind` hook**: `ctxloom/sessions` binds lazily (resolve-or-create keyed by the Claude Code session id every hook payload carries).
- **Two identity schemes** — **exec-token** for settings hooks + statusLine; **server-name + `_ctxloom` marker** for `.mcp.json` (not strict-schema'd). Both owned by `ctxloom/agent` (per-engine shape supplied by the agent package).
- **Dropped: `mcpkit`.** MCP already supports many servers per agent (today's `.mcp.json` lists `ctxloom` + `sequential-thinking`). ctxtask just registers its own server alongside ctxloom's — no aggregation lib, no composition phase.
- **Migrations go through the migrations layer, never core.** ctxloom's `internal/upgrade` (`Upgrader`/`Pipeline` — upgrade older on-disk form in memory on load, prompt before persist, idempotent, version-aware; used today by `config` + `sessions`) plus the location-move precedent (`tasks.MigrateLegacyIfNeeded`) is the home for every migration this plan introduces. Each data-owner registers its own Upgraders; `ctxloom/sessions`, `ctxloom/agent`, and the load/resolution paths carry **no** one-off migration logic. The framework travels as a small shared piece (within `ctxloom/sessions`, or a tiny `ctxloom/upgrade`).

## Shared libraries

### `ctxloom/agent` (the core)
The engine-agnostic agent contract + the settings-writing machinery:
- Interfaces `Backend` (launch/run) and `SettingsWriter` (hooks/statusline/MCP), kept separate so consumers take only the facet they need.
- The normalized `UnifiedHooks`/settings types each agent converts to/from its wire format (same pattern as the `Backend`'s normalized Session types: adapters own conversions, no type-switching in shared code).
- The **owner predicate + `Reconcile` + canonical marshaller** (sorted keys + trailing newline — kills the ltk↔ctxloom key-order churn). Command parsing reuses **ltk's** parser (vars, `sh -c` unwrapping).
- Engine detection / agent registry.

### `ctxloom/claude`, `ctxloom/gemini` (full agents)
Each implements `ctxloom/agent`: launch/run, the engine's settings.json/.mcp.json
shape + hook events + statusline + strict-schema marker rules, capabilities,
session/transcript wire-format. ALL engine-specific knowledge lives here. The
`feat/gemini-parity` work is the `ctxloom/gemini` package.

### `ctxloom/sessions`
Project-root + per-project state dir + session identity, IDs from `ctxloom/harp`.
Pieces already exist: `internal/projectroot` (CTXLOOM_ROOT) + the session
machinery + the task-store path resolution (`paths.TasksLogPath`,
`HarpStorePath`). Resolves "where is my state for THIS project" with fallback
when no `.ctxloom` exists. ctxloom, ltk (project-root only), and ctxtask consume
it — layer session machinery above a plain root-resolver so ltk takes only what
it needs. Session binding is **lazy** (resolve-or-create by CC session id),
replacing today's `ctxloom hook session-bind` SessionStart hook.

(`ctxloom/harp` = the tiny-loud-lark ID-naming algorithm, used by sessions + ctxtask.)

## Owner predicate (in `ctxloom/agent` — prototyped, exec-token only)

Problem: Claude Code's strict (`.strict()`) schema rejects unknown fields, so a
tool **cannot** persist an `_owner` marker on its entries. With N tools writing
one settings file, each must recognize **only its own** entries to reconcile
idempotently without clobbering the user's or other tools' entries.

Mechanism: **executable token**. An entry is owned if its command's executable
token resolves to the tool's `Bin` (`ctxloom` / `ltk` / `ctxtask`) — path-,
quote-, and verb-agnostic. That suffices because each tool owns its own
executable namespace and never writes a foreign command. `Reconcile` = remove
every entry the owner owns, append desired, prune emptied groups. Idempotency and
verb-drift migration (`ctxloom meta hud` → `ctxloom hook hud`) fall out of
exec-token identity — no bookkeeping, no sidecar.

Graduated to `internal/agent.IsManaged(command, bin)` (P0 step 2), tested for
exec-token edge cases (quoted/absolute/Windows/.exe) + cross-tool isolation.

Deferred (only if a real case appears): owning a hook that runs a **foreign**
binary — a declared-command set + a sidecar manifest + a conflict check. Not
built: foreign commands live in `.mcp.json` (marker-based, not this predicate).
Known limit: env-prefix / `sh -c` — production delegates to ltk's parser.

## Shared `.ctxloom` layout

```
.ctxloom/
  config.yaml            # ctxloom context config (existing)
  sessions/              # session state + per-harp task.md (ctxloom/sessions)
  tasks/                 # ~/.ctxloom/tasks/<project-id>.jsonl, owned by ctxtask, path via ctxloom/sessions
  ltk/                   # ltk rules inside a ctxloom workspace (else .ltk/)
```

## Sequence (strangler — each phase ships independently)

- **P0 — extract `ctxloom/agent` + the agent packages.** Move `internal/lm/backends` into `ctxloom/agent` (interfaces, `UnifiedHooks`, owner predicate, `Reconcile`, canonical marshaller, detection) + `ctxloom/claude` (Backend + SettingsWriter + capabilities). `ctxloom/gemini` follows, riding `feat/gemini-parity`. ltk writes its hook via `ctxloom/agent`'s `SettingsWriter` (claude agent). *Exit:* ctxloom launches + writes settings via the agent packages; ltk uses the same writer; serializer churn gone; existing hook/backend tests pass; user keys preserved.
- **P1 — `ctxloom/sessions` extraction.** Pull `projectroot` + state-dir + session + task-store path resolution into `ctxloom/sessions`; replace the `session-bind` hook with lazy binding; adopt the `.ctxloom` layout (any path/dir moves done as registered migrations in the migrations layer, not inline in the resolver). *Exit:* all three tools resolve state through one lib; the session-bind hook is gone; standalone mode still works.
- **P2 — carve `ctxtask` out of ctxloom.** Move `internal/tasks` + `internal/operations/tasks.go` + `cmd/tasks_cmd.go` + `cmd/mcp_tools_tasks.go` into `ctxloom/ctxtask`: own CLI + MCP server, consuming `ctxloom/sessions` for paths; **owns the `stamp-plan` hook** (`ctxloom hook stamp-plan` → `ctxtask hook stamp-plan`). *Exit:* ctxtask runs standalone; ctxloom no longer ships task code or the stamp-plan hook. (No data migration — the `tasks/` path is unchanged.)
- **P3 — slim ctxloom + ltk onto the libs.** Remove the now-duplicated writer/projectroot code from both. *Exit:* no settings-writing or root-resolution logic outside the shared libs.

Depends: P0 → P1 → P2 → P3.

**Cross-tool integration gate** (every phase that touches the writer): install
ctxloom + ltk + ctxtask into one `.claude/settings.json`, re-apply each, assert
coexistence + idempotence + preservation of the user's own entries.

## Risks

- **Agent extraction (P0) is the biggest lift** — it moves launch + settings + capabilities per engine and must keep the `SettingsWriter`/`Backend` facets separable so ltk doesn't pull in launch code. Land the canonical marshaller first so both writers agree on bytes before either is swapped.
- **Owner-predicate identity model** — core risk, much reduced by exec-token-only; the tokenizer must be solid (reuse ltk's parser).
- **State-dir backward compatibility** — existing `.ctxloom/` users keep working via the **migrations layer**: the `.ltk/` → `.ctxloom/ltk/` move is a registered migration (location-move variant of `internal/upgrade`), owned by ltk, NOT inline in `ctxloom/sessions`'s resolution path. (Task data stays put.)
```
