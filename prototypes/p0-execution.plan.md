# P0 execution — extract `ctxloom/agent` + agent packages

> **Status: SUPERSEDED by the monorepo consolidation.** The agent substrate lives at `internal/shared/agent`, with per-engine packages at `internal/{claude,codex,antigravity}` in a single module (no separate `ctxloom/agent`/`ctxloom/<engine>` modules). Historical body retained below.

Status: in progress · 2026-06-05 · session `tiny-loud-lark`
Parent: `./decoupling-shared-substrate.plan.md` (phase P0)

Approach: **in-repo first.** Build the agent core inside the ctxloom repo
(`internal/agent`) and validate against the real writers + tests; extraction to
`github.com/ctxloom/agent` is a later mechanical packaging step (git filter-repo
+ go.mod + tags), not part of proving the design. Each step ships green.

## Steps

1. **Canonical marshaller** — ✅ done. `internal/agent.CanonicalJSON` (recursively
   sorted keys + trailing newline); wired into all three settings/MCP write
   points (claude settings, claude `.mcp.json`, gemini settings) in
   `internal/lm/backends/hooks.go`. Kills the ltk↔ctxloom key-order churn at the
   source. Full suite green; no golden-byte test broke.
2. **Graduate the owner predicate** — ✅ done. `agent.Owner`/`Owns`/`execToken` in
   `internal/agent`; `isCtxloomManaged` now delegates to
   `agent.Owner{Bin: "ctxloom"}`, retiring the duplicate
   `firstShellToken`/exec-token logic. (Reconcile graduation folded into step 4 —
   it's engine-specific and lands with the writer refactor.) `TestIsCtxloomManaged`
   + full suite green.
3. **Split the facets** — ✅ done. `SettingsWriter` + `SettingsStatus` moved into
   `internal/agent` (the settings facet), separate from `Backend` (launch, still
   in `backends`). `backends` keeps type aliases so existing sites are untouched;
   the concrete writers implement `agent.SettingsWriter`. A consumer (ltk) can now
   take the settings facet without launch. Full suite green. (NOTE: the interface
   still uses `config.*` types; those normalized types move to `agent` at
   extraction time — step 6.)
4. **Per-agent shape** (multi-part — the heavy refactor):
   - 4a ✅ Move the launch `Backend` contract (`interfaces.go`) into `internal/agent`,
     aliased in `backends`. Both facet contracts (`SettingsWriter` + `Backend`) now
     live in the core. Cycle-free (interfaces.go was stdlib-only, self-contained).
   - 4b ✅ Lift the engine-agnostic settings infra (`SettingsOptions`,
     `AtomicWriteFile`, `GetFS`, `Warn`, `ComputeHookHash`) into `internal/agent`;
     `backends` aliases the options and delegates the package helpers. Realization:
     the name→writer **registry is wiring, not core** — it stays in `backends` and
     simply imports the agent packages in 4c/4d, so no registration-inversion / no
     `init()` is needed. Full suite green.
   - 4c ✅ Move `ClaudeCodeHookWriter` (+ claude types/methods) → `internal/agent/claude`;
     registry calls `claude.NewWriter`. Done in two commits:
     - (1/2) shared-symbol lift: `ComputeMCPServerHash`, `CtxloomBinary`, `MCPServerName`,
       `CtxloomMCPArgs` → `agent`; backends references via aliases.
     - (2/2) verbatim writer move + shims (`getFS`/`atomicWriteFile`/`warn`/`computeHookHash`/
       `computeMCPServerHash`/`isCtxloomManaged` + `SettingsStatus`/`ctxloomBinary`/
       `ctxloomMCPArgs` aliases; constructor `NewWriter(agent.SettingsOptions)`), plus the
       **test split** — 13 claude writer tests → `internal/agent/claude/claude_test.go`; the one
       test needing backends helpers (`SetExecutablePathForTesting`/`NewContextInjectionHook`)
       stays in backends as `claude.ClaudeCodeHookWriter`; shared/gemini tests stay. Full suite green.
   - 4d ✅ Move `GeminiHookWriter` → `internal/agent/gemini`; registry calls
     `gemini.NewWriter`. Same recipe as 4c (verbatim move + shims). Wrinkle: gemini
     code was interspersed with shared symbols (`ctxloomBinary`/`ctxloomMCPArgs`/
     `isCtxloomManaged` + the whole context-injection subsystem) — those came out with
     the block as gemini's shims, and fresh copies were re-added to backends (symlink
     + context-injection still use them). 8 gemini tests → `gemini_test.go` (no backends
     deps). Removed the now-dead `AppMCPServerName`/`computeMCPServerHash` from backends.
     Full suite green.
   - 4e ✅ Move the launch impls (`claudecode.go`/`gemini.go` + `*_capabilities.go`)
     into the agent packages. Done in 5 sub-steps (see `./p0-4e.plan.md`): 4e-i broke
     the registry cycle via injected `agent.WriteSettingsFunc`; 4e-ii lifted the shared
     launch substrate (BaseBackend, capability bases, context file/chunk/rendezvous,
     context-injection web) to `agent`; 4e-iii/iv moved the claude/gemini launch
     backends + capabilities into their packages (with `PreviousSessionByListing` and
     the `BackendConfig` contract graduating to the core); 4e-v slimmed backends to the
     registry + codex/mock + cross-agent dispatch and pruned 23 orphaned aliases.
     `internal/agent/{claude,gemini}` are now full agents (settings + launch).
5. **Cross-tool gate** — point ltk at the shared writer (via the published module
   or a `go.work`); assert ctxloom + ltk coexist + idempotent re-apply in one
   settings.json.
6. **Extract to repos** (later) — `internal/agent` → `github.com/ctxloom/agent`,
   `claude`/`gemini` likewise; semver-tag via versionator.

Exit (P0): ctxloom + ltk both write via the shared agent core; churn gone; both
tools' hook tests pass; user keys preserved.
