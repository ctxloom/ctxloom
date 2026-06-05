# Decoupling ctxloom / ltk / harp into a shared substrate

Status: draft · 2026-06-05 · session `tiny-loud-lark`
Prototype: `./owner-predicate/` (5 tests passing via `just test`)

## Goal

Three tools that are **independently usable** but **interoperate well**, built on a
thin shared substrate. Invert the earlier "ctxloom absorbs ltk" idea: no tool
depends on another at compile time; they meet only at data + protocol seams (the
agent settings file, the `.ctxloom` workspace dir, and MCP).

- **ctxloom** — context assembly (bundles/profiles/fragments/prompts, remotes, MCP context tools, hook application).
- **ltk** — pre-tool command/edit gating.
- **harp** — sessions + tasks (today these live inside ctxloom; the harp IDs and harp session dir already name this layer).

```
tools:        ctxloom            ltk            harp
                  \               |              /
shared libs:   harness-config  workspace     mcpkit
                       (tools -> libs only; never tool -> tool)
```

Independence: each tool works with zero others installed. Interop: when several
are present they coexist in one `.claude/settings.json` and one `.ctxloom/`,
each reconciling only its own state.

## Resolved decisions

- **Shared `.ctxloom`** — one workspace root, per-tool subdirs (layout below). A standalone tool falls back to its own dot-dir when no `.ctxloom` exists; `workspace` owns that resolution.
- **Versioning** — handled (per-repo versionator auto-release; shared libs versioned independently, consumers pin). Not a blocker.
- **Owner predicate** — prototyped and validated (see below).

## Shared libraries

### `harness-config` (the linchpin)
Reads/merges/writes `.claude/settings.json`, `.gemini/settings.json`, `.mcp.json`
across engine adapters. Lift the union of two existing seams:
- ltk `internal/engine/claudecode.go` — `map[string]any` merge + `SettingsPath`/`Install`/`Uninstall` (engine-agnostic, schema-tolerant).
- ctxloom `internal/lm/backends/hooks.go` — the richer reconciliation (managed-set removal, statusline, MCP split-out).

Provides: the **Owner predicate** + `Reconcile` (below), a **canonical marshaller**
(sorted keys + trailing newline — kills the ltk↔ctxloom key-order churn observed
on 2026-06-05), and the engine adapters. Command parsing should reuse **ltk's**
parser (it already resolves vars and unwraps `sh -c`/trivial wrappers) — the
synthesis: ltk's parser + ctxloom's reconciler.

### `workspace` (could be named `harp`)
Project-root + per-project state dir + session identity + harp-ID generation.
Already have the pieces: `internal/projectroot` (CTXLOOM_ROOT work) and the harp
session machinery. Resolves "where is my state for THIS project" with fallback
when no `.ctxloom` exists.

### `mcpkit` (optional)
MCP server scaffolding so each tool exposes its own server AND is mountable.
The interop lever: ctxloom's server can aggregate harp's task tools, or each runs
standalone. The current MCP instructions already split "context retrieval" from
"task tracking" along this line.

## Owner predicate (linchpin design — prototyped, exec-token only)

Problem: Claude Code's strict (`.strict()`) schema rejects unknown fields, so a
tool **cannot** persist an `_owner` marker on its entries. With N tools writing
one settings file, each must recognize **only its own** entries to reconcile
idempotently without clobbering the user's or other tools' entries.

Mechanism: **executable token**. An entry is owned if its command's executable
token resolves to the tool's `Bin` — path-, quote-, and verb-agnostic. That is
all that's needed, because in the decoupled design **each tool owns its own
executable namespace** (ctxloom / ltk / harp) and never writes a foreign
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
  sessions/              # harp sessions (existing)
  tasks/                 # harp task logs (existing; moves under harp ownership)
  ltk/config.yaml        # ltk rules when inside a ctxloom workspace (else .ltk/)
```

## Sequence (strangler — each phase ships independently)

- **P0 — harness-config extraction.** New module = ltk's `map[string]any` merge ∪ ctxloom's reconciler, the Owner predicate (graduate the prototype), canonical marshaller, engine adapters. *Exit:* ctxloom and ltk both write via it; the key-order churn is gone; both tools' existing hook tests pass.
- **P1 — workspace extraction.** Pull `projectroot` + state-dir + session resolution into `workspace`; adopt the shared `.ctxloom` layout with fallback. *Exit:* all three tools resolve state through one lib; standalone-mode (no `.ctxloom`) still works.
- **P2 — carve harp out of ctxloom.** Move `internal/tasks` + `internal/operations/tasks.go` + `cmd/tasks_cmd.go` + `cmd/mcp_tools_tasks.go` into `harp`: its own CLI + MCP server, and it **owns the `stamp-plan` PostFileEdit hook** (today emitted as `ctxloom hook stamp-plan` — a tasks concern). Installed via harness-config. *Exit:* harp runs standalone; ctxloom no longer ships task code or the stamp-plan hook.
- **P3 — MCP composition.** ctxloom's server optionally mounts harp's task tools via `mcpkit`; each still runs standalone. *Exit:* one MCP endpoint can serve context + tasks, or two endpoints, by config.
- **P4 — slim ctxloom + ltk onto the libs.** Remove the now-duplicated writer/projectroot code from both. *Exit:* no settings-writing or root-resolution logic outside the shared libs.

Depends: P0 → (P1, P2) → P3; P4 after P0/P1.

## Open decisions

- Lib + tool names (`harness-config`/`agentkit`? `workspace`/`harp`?), and one-repo-per-lib vs a shared multi-module repo.
- ltk config home under a ctxloom workspace: `.ctxloom/ltk/` vs keep `.ltk/`.
- statusLine + MCP-server entries: same owner-predicate model (single-slot variants) — confirm in P0.
- Gemini adapter parity (the `feat/gemini-parity` work feeds straight into harness-config's engine adapters).

## Risks

- **Owner-predicate identity model** — the core risk, much reduced by the exec-token-only design; the command tokenizer must be solid (reuse ltk's parser for env-prefix / `sh -c`).
- **Migration churn during extraction** — both writers must agree on canonical bytes before either is swapped (do the marshaller first in P0).
- **State-dir backward compatibility** — existing `.ctxloom/` users must keep working; `workspace` resolution needs fallbacks + a one-time migration for ltk's `.ltk/`.
```
