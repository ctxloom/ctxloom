package backends

import (
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	d, exists := lookup(name)
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
	d, ok := lookup(name)
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
	delete(descriptors, agent.CanonicalEngineName(name))
}

// InTreeAgentHomeSpec is one backend's ctxloom-CONTROLLED config home INSTANCE
// for one session's in-tree agent run: the engine's own home-relocation
// variable, the per-session directory it points at, and the preparation (if
// any) that has to happen before the engine is launched at it.
type InTreeAgentHomeSpec struct {
	// EnvVar is the engine's home-relocation variable (CLAUDE_CONFIG_DIR,
	// CODEX_HOME, KIRO_HOME).
	EnvVar string
	// Dir is THIS SESSION's instance home, resolved through the owning engine
	// package's own paths.SessionHomePath-derived helper — the engine package
	// owns its own leaf, so no two engines can collide under one session root.
	Dir string
	// Prepare populates Dir before the engine is launched at it: the one-way
	// copy-in of ambient host material (credentials today) plus any
	// engine-specific scaffolding, returning an actionable error when there is
	// nothing to authenticate with. nil when the backend needs neither (kiro,
	// whose credentials live in a global store no home var relocates — a
	// DECLARED empty ambient set, not an omission).
	Prepare func() error
}

// InTreeAgentHomeFor resolves the named backend's controlled config-home
// INSTANCE for (workDir, harp), or ok=false when that backend has none — or
// when the harp cannot name one. It is the polymorphic seam
// operations.InTreeAgentHomeEnv reads instead of branching on engine identity
// (ADR-0026) — the same shape ResolveModelFor and CheckHookTargetScope above
// have, and for the same reason.
//
// This answers only WHERE, never WHETHER. The scoping rule — controlled homes
// go to runs whose agent binding declares `config_home: project`, on the
// in-tree axis, when nothing has already set the var — belongs to the caller
// and lives in ONE place there, for all three engines.
//
// harp is REQUIRED. An empty harp resolves nothing and creates nothing: there
// is no session-less instance to fall back to, and a project-wide fallback is
// exactly the durable home the per-session model retired. A harp that fails
// validation warns and declines, because a caller that got this far with an
// unusable session name has a bug the run should not paper over.
//
// The roster's absentees are deliberate:
//
//   - opencode has none because its only lever is XDG_CONFIG_HOME /
//     XDG_DATA_HOME, which are not engine-private: relocating them moves git's,
//     fish's and every other XDG-aware tool's config for the child too.
//   - acp, mock and antigravity have no engine-global home for ctxloom to
//     control.
//
// codex USED to be an absentee, on the reasoning that it relocated CODEX_HOME
// on every axis itself and a second contributor here would race the one that
// works. The D2 ruling ended that asymmetry: codex reads config_home like
// claude and kiro, its own resolver's non-isolated arm now lands on the real
// ~/.codex, and this seam is the single contributor of a controlled home for
// all three.
func InTreeAgentHomeFor(name, workDir, harp string) (InTreeAgentHomeSpec, bool) {
	d, exists := lookup(name)
	if !exists || d.inTreeAgentHome == nil {
		return InTreeAgentHomeSpec{}, false
	}
	if harp == "" {
		return InTreeAgentHomeSpec{}, false
	}
	spec, err := d.inTreeAgentHome(workDir, harp)
	if err != nil {
		clidiag.Warn("ctxloom", "cannot resolve a per-session config home for %s in session %q (%v); this run uses the engine's own host config home instead", name, harp, err)
		return InTreeAgentHomeSpec{}, false
	}
	return spec, true
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
