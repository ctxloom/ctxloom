# P0 execution — extract `ctxloom/agent` + agent packages

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
4. **Per-agent shape** — refactor `internal/lm/backends` into `internal/agent/claude`
   + `internal/agent/gemini` implementing the core interfaces; ctxloom selects via
   a registry. `feat/gemini-parity` work folds in as the gemini agent.
5. **Cross-tool gate** — point ltk at the shared writer (via the published module
   or a `go.work`); assert ctxloom + ltk coexist + idempotent re-apply in one
   settings.json.
6. **Extract to repos** (later) — `internal/agent` → `github.com/ctxloom/agent`,
   `claude`/`gemini` likewise; semver-tag via versionator.

Exit (P0): ctxloom + ltk both write via the shared agent core; churn gone; both
tools' hook tests pass; user keys preserved.
