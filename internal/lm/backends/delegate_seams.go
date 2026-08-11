package backends

import (
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file holds the two polymorphic seams T12 moved out of
// internal/operations (hooks.go's checkHookTargetScope, delegate.go's
// resolveChatModel): both used to branch on backend identity and call
// claude/codex/kiro package functions directly from the operations core — a
// literal ADR-0026 violation (operations, the core, reaching across the
// subsidiary-application-plugin edge instead of depending on the injected,
// polymorphic internal/lm/backends seam ADR-0020 already names for exactly
// this). Per that ADR, this package IS the sanctioned place for
// backend-identity branching; operations now calls ResolveModelFor /
// CheckHookTargetScope and never imports claude/codex/kiro itself.
//
// Both seams are descriptor fields (hookGlobalScopePaths/hookGlobalScopeLabel,
// resolveModel in registry.go's agentDescriptor) rather than a hardcoded
// switch here, so a backend that needs either capability registers it once,
// in its own descriptor block, and both operations call sites pick it up with
// no operations-side edit — closing the gap the pre-fix hardcoded 3-way
// if/else left: a NEW backend with its own project/global collision class (or
// its own model-nickname table) got NEITHER protection until someone
// remembered to add its name to operations' own copy of the list.

// ResolveModelFor translates rs.Model through the named backend's own
// resolveModel hook when it has one — the delegated-child launch path's
// ACP/API model resolution (internal/claude.ResolveModel today), generalized
// off a hardcoded "is this claude-code" branch in operations. A backend with
// no resolveModel hook (every backend but claude-code today) or an
// unregistered name passes model through unchanged with ok=true: "nothing to
// resolve" is not a failure.
func ResolveModelFor(name, model string) (resolved string, ok bool) {
	d, exists := descriptors[name]
	if !exists || d.resolveModel == nil {
		return model, true
	}
	return d.resolveModel(model)
}

// CheckHookTargetScope refuses (or, with force, loudly warns) when workDir
// resolves onto the named backend's user-GLOBAL scope instead of a project's
// per-PROJECT scope — Claude Code's settings.json, codex's whole
// config.toml/prompts/skills home, kiro's whole agents/settings/steering home
// (see each one's hookGlobalScopePaths wiring in registry.go for the
// collision class itself). A backend with no hookGlobalScopePaths hook
// (opencode, acp, mock — audited as unable to hit this
// collision; see each descriptor's comment) or an unregistered name is a
// no-op: nothing to guard.
//
// force downgrades a real collision to a loud warning and proceeds — the
// deliberate escape hatch for a genuine intentional global install.
func CheckHookTargetScope(name, workDir string, force bool) error {
	d, ok := descriptors[name]
	if !ok || d.hookGlobalScopePaths == nil {
		return nil
	}
	projectPath, globalPath, err := d.hookGlobalScopePaths(workDir)
	if err != nil {
		// No resolvable home directory: nothing to collide with.
		return nil
	}
	if cleanAbsPath(projectPath) != cleanAbsPath(globalPath) {
		return nil
	}
	label := d.hookGlobalScopeLabel
	if force {
		clidiag.Warn("ctxloom", "hooks target %s resolves to %s (%s); proceeding because --force was given — this applies ctxloom to EVERY project, not just this one.", workDir, label, globalPath)
		return nil
	}
	return fmt.Errorf("refusing to install hooks: %s resolves to %s (%s), which would apply ctxloom to every project instead of just this one; run from inside a project (or set CTXLOOM_ROOT), or pass --force to proceed anyway", workDir, label, globalPath)
}

// RegisterHookGlobalScopeForTesting wires a backend's project/global
// collision-check hook for tests only (mirrors agent.
// SetExecutablePathForTesting's naming/shape) — it exists so a flow test can
// prove T12's generic property directly: a backend that registers
// hookGlobalScopePaths gets CheckHookTargetScope's (and so ApplyHooks')
// guard automatically, with no operations-side edit, rather than only the
// three names a pre-fix hardcoded if/else happened to know about. Production
// code never calls this; only registerDescriptor's own init() wiring
// (registry.go) sets these fields outside tests.
func RegisterHookGlobalScopeForTesting(name string, paths func(workDir string) (projectPath, globalPath string, err error), label string) {
	d := descriptorFor(name)
	d.hookGlobalScopePaths = paths
	d.hookGlobalScopeLabel = label
}

// UnregisterForTesting removes a name's descriptor entirely — the
// RegisterHookGlobalScopeForTesting cleanup counterpart, so a test's
// synthetic backend does not linger in the shared, package-level descriptors
// table for later tests to trip over.
func UnregisterForTesting(name string) {
	delete(descriptors, name)
}

// cleanAbsPath returns p's cleaned absolute form for path comparison, falling
// back to just Clean if it cannot be made absolute (e.g. a synthetic test
// path with no real filesystem behind it — filepath.Abs only fails when the
// process cwd itself cannot be determined).
func cleanAbsPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
