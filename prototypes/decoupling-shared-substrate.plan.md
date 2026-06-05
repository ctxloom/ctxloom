# Decoupling ctxloom / ltk / ctxtask over a shared `ctxloom/*` substrate

Status: draft · 2026-06-05 · session `tiny-loud-lark`
Prototype: `./owner-predicate/` (5 tests passing via `just test`)

## Goal

Three tools that are **independently usable** but **interoperate well**, built on a
thin shared substrate. Invert the earlier "ctxloom absorbs ltk" idea: no tool
depends on another at compile time; they meet only at data + protocol seams (the
agent settings file, the `.ctxloom` workspace dir, and MCP).

- **ctxloom** — context assembly (bundles/profiles/fragments/prompts, remotes, MCP context tools, hook application).
- **ltk** — pre-tool command/edit gating.
- **ctxtask** — the task tracker, carved out of ctxloom (module `github.com/ctxloom/ctxtask`).

Everything namespaces under the `ctxloom` GitHub org as independently-usable
packages — libs `ctxloom/sessions`, `ctxloom/harness-config`, `ctxloom/mcpkit`,
and the `ctxloom/harp` ID algorithm; tools `ctxloom`, `ltk`, `ctxtask`. Each is
its own repo + Go module (`github.com/ctxloom/<name>`), imported by module path
and pinned with semver tags — a tool depends on a lib exactly like any
third-party Go dependency.

Session *identity* is not a tool — it is substrate (ctxloom, ctxtask, and the
hooks all need "which session is this"), so it lives in `ctxloom/sessions`.
`ctxloom/harp` is the ID-naming algorithm (tiny-loud-lark) both it and ctxtask use.

```
tools:        ctxloom              ltk            ctxtask
                  \                  |               /
shared libs:  ctxloom/harness-config  ctxloom/sessions  ctxloom/mcpkit
       (sessions uses ctxloom/harp; tools -> libs only, never tool -> tool)
```

Independence: each tool works with zero others installed. Interop: when several
are present they coexist in one `.claude/settings.json` and one `.ctxloom/`,
each reconciling only its own state.

## Resolved decisions

- **Shared `.ctxloom`** — one workspace root, per-tool subdirs (`.ctxloom/ltk/`, `.ctxloom/ctxtask/`; layout below). A standalone tool falls back to its own dot-dir (`.ltk/`) when no `.ctxloom` exists; `ctxloom/sessions` owns that resolution.
- **Packaging** — `ctxloom` is the GitHub org; each lib/tool is its own repo + Go module (`github.com/ctxloom/<name>`), imported by module path and pinned via semver tags (versionator tags them). Cross-repo local dev via a `go.work`; private repos need `GOPRIVATE=github.com/ctxloom/*` + SSH/token.
- **Names** — lib `harness-config`; tasks tool/binary `ctxtask` (module `github.com/ctxloom/ctxtask`); per-tool state dirs `.ctxloom/<tool>/`.
- **Owner predicate** — prototyped and validated (see below).
- **Hook ownership** — each hook is written by the binary that serves it: `inject-context` (SessionStart) + `hud` (statusLine) → ctxloom; `stamp-plan` (PostFileEdit) → ctxtask. **No `session-bind` hook**: `ctxloom/sessions` binds lazily (resolve-or-create keyed by the Claude Code session id, which every hook payload carries), so the first tool to run in a session binds it idempotently — nothing needs to own a SessionStart bind hook.
- **Identity is two schemes, not one** — **exec-token** for `.claude`/`.gemini` settings hooks + statusLine; **server-name + `_ctxloom` marker** for `.mcp.json` (which is not strict-schema'd). `harness-config` owns both.

## Shared libraries

### `ctxloom/harness-config` (the linchpin)
Reads/merges/writes `.claude/settings.json`, `.gemini/settings.json`, `.mcp.json`
across engine adapters. Lift the union of two existing seams:
- ltk `internal/engine/claudecode.go` — `map[string]any` merge + `SettingsPath`/`Install`/`Uninstall` (engine-agnostic, schema-tolerant, preserves unknown keys).
- ctxloom `internal/lm/backends/hooks.go` — the richer reconciliation (managed-set removal, statusline, MCP split-out) over a typed-known + `Other map[string]json.RawMessage` hybrid that also preserves unknown keys.

Provides: the **Owner predicate** + `Reconcile` (below), a **canonical marshaller**
(sorted keys + trailing newline — kills the ltk↔ctxloom key-order churn observed
on 2026-06-05), and the engine adapters. Command parsing should reuse **ltk's**
parser (it already resolves vars and unwraps `sh -c`/trivial wrappers) — the
synthesis: ltk's parser + ctxloom's reconciler. Non-negotiable: preserve a user's
unknown settings keys (`permissions`, `env`, …).

### `ctxloom/sessions`
Project-root + per-project state dir + session identity, with IDs from
`ctxloom/harp`. Already have the pieces: `internal/projectroot` (CTXLOOM_ROOT
work) and the session machinery. Resolves "where is my state for THIS project"
with fallback when no `.ctxloom` exists. ctxloom, ltk (project-root only), and
ctxtask all consume it — layer the session machinery above a plain root-resolver
so ltk doesn't drag in sessions it doesn't use. Session binding is **lazy**: a
`resolve-or-create` keyed by the Claude Code session id, called by whichever tool
runs first — replacing today's `ctxloom hook session-bind` SessionStart hook, so
no tool needs to own one.

### `ctxloom/mcpkit` (optional for P0–P2, required for P3)
MCP server scaffolding so each tool exposes its own server AND is mountable.
The interop lever: ctxloom's server can aggregate ctxtask's tools, or each runs
standalone. The current MCP instructions already split "context retrieval" from
"task tracking" along this line.

## Owner predicate (linchpin design — prototyped, exec-token only)

Problem: Claude Code's strict (`.strict()`) schema rejects unknown fields, so a
tool **cannot** persist an `_owner` marker on its entries. With N tools writing
one settings file, each must recognize **only its own** entries to reconcile
idempotently without clobbering the user's or other tools' entries.

Mechanism: **executable token**. An entry is owned if its command's executable
token resolves to the tool's `Bin` (`ctxloom` / `ltk` / `ctxtask`) — path-,
quote-, and verb-agnostic. That is all that's needed, because in the decoupled
design **each tool owns its own executable namespace** and never writes a foreign
command. `Reconcile(settings, owner, desired)` = remove every entry the owner
owns, append desired, prune emptied groups. Idempotency and verb-drift migration
(`ctxloom meta hud` → `ctxloom hook hud`) both fall out of exec-token identity —
no bookkeeping, no sidecar.

Prototype `predicate.go` + `reconcile.go` (~145 LOC incl. comments), tests
passing: exec-token edge cases (quoted/absolute/Windows/.exe), verb-drift
migration, idempotency, cross-tool + user isolation, no false positives.

Deferred (build only if a real case appears): owning a hook that runs a
**foreign** binary — would add a declared-command set + a `.ctxloom/<tool>/`
sidecar manifest for cleanup, plus a one-writer-per-command conflict check. Not
built, because the one place foreign commands actually live (MCP servers) is
`.mcp.json`, which is not strict-schema'd and already carries a real `_ctxloom`
marker — so it never needs this predicate.

Known limit: env-prefix (`FOO=bar ctxloom …`) and `sh -c` wrappers — the
prototype tokenizer is naive; production delegates to ltk's parser.

## Shared `.ctxloom` layout

```
.ctxloom/
  config.yaml            # ctxloom context config (existing)
  sessions/              # session state, owned by ctxloom/sessions (existing)
  ctxtask/               # task logs, owned by ctxtask (migrate from existing .ctxloom/tasks/)
  ltk/                   # ltk rules/config inside a ctxloom workspace (else .ltk/)
```

## Sequence (strangler — each phase ships independently)

- **P0 — harness-config extraction.** New module = ltk's `map[string]any` merge ∪ ctxloom's reconciler, the Owner predicate (graduate the prototype), canonical marshaller, engine adapters. Both writers must preserve unknown keys. *Exit:* ctxloom and ltk both write via it; the key-order churn is gone; both tools' existing hook tests pass.
- **P1 — `ctxloom/sessions` extraction.** Pull `projectroot` + state-dir + session resolution into `ctxloom/sessions` (IDs via `ctxloom/harp`); replace the `ctxloom hook session-bind` SessionStart hook with lazy resolve-or-create binding in the lib; adopt the shared `.ctxloom` layout with fallback. *Exit:* all three tools resolve state through one lib; the session-bind hook is gone and binding happens lazily; standalone-mode (no `.ctxloom`) still works.
- **P2 — carve `ctxtask` out of ctxloom.** Move `internal/tasks` + `internal/operations/tasks.go` + `cmd/tasks_cmd.go` + `cmd/mcp_tools_tasks.go` into `ctxloom/ctxtask`: its own CLI + MCP server, consuming `ctxloom/sessions`, and it **owns the `stamp-plan` PostFileEdit hook** (today `ctxloom hook stamp-plan` → `ctxtask hook stamp-plan`; the `ctxtask` basename becomes its owner token). Installed via harness-config; migrate `.ctxloom/tasks/` → `.ctxloom/ctxtask/`. *Exit:* ctxtask runs standalone; ctxloom no longer ships task code or the stamp-plan hook.
- **P3 — MCP composition.** ctxloom's server optionally mounts ctxtask's tools via `ctxloom/mcpkit`; each still runs standalone. *Exit:* one MCP endpoint can serve context + tasks, or two endpoints, by config.
- **P4 — slim ctxloom + ltk onto the libs.** Remove the now-duplicated writer/projectroot code from both. *Exit:* no settings-writing or root-resolution logic outside the shared libs.

Depends: P0 → P1 → P2 → P3 (carving ctxtask needs ctxloom/sessions); P4 after P0/P1.

**Cross-tool integration gate** (every phase that touches the writer): install
ctxloom + ltk + ctxtask into one `.claude/settings.json`, re-apply each, and
assert coexistence + idempotence + preservation of the user's own entries. The
prototype proves this at unit level; this is the production version.

## Open / deferred

- Gemini adapter: claude-first, gemini-follows (the `feat/gemini-parity` work feeds straight into harness-config's engine adapters).
- Foreign-hook ownership (declared-command set + sidecar manifest + conflict check) — deferred per the owner-predicate section; build only if a real case appears.

## Risks

- **Owner-predicate identity model** — the core risk, much reduced by the exec-token-only design; the command tokenizer must be solid (reuse ltk's parser for env-prefix / `sh -c`).
- **Migration churn during extraction** — both writers must agree on canonical bytes before either is swapped (do the marshaller first in P0).
- **State-dir backward compatibility** — existing `.ctxloom/` users must keep working: `ctxloom/sessions` resolution needs fallbacks, plus one-time migrations for `.ctxloom/tasks/` → `.ctxloom/ctxtask/` and ltk's `.ltk/` → `.ctxloom/ltk/`.
```
