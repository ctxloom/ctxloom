package backends

import (
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file holds the two polymorphic seams T12 moved out of
// internal/adapters/operations (hooks.go's checkHookTargetScope, delegate.go's
// resolveChatModel): both used to branch on backend identity and call
// claude/codex package functions directly from the operations core — a
// literal ADR-0026 violation (operations, the core, reaching across the
// subsidiary-application-plugin edge instead of depending on the injected,
// polymorphic internal/lm/backends seam ADR-0020 already names for exactly
// this). Per that ADR, this package IS the sanctioned place for
// backend-identity branching; operations now calls ResolveModelFor /
// CheckHookTargetScope and never imports claude/codex itself.
//
// Both seams are descriptor fields (HookGlobalScope, ResolveModel on
// engine.Descriptor) rather than a hardcoded switch here, so a backend that needs either capability registers it once,
// in its own descriptor block, and both operations call sites pick it up with
// no operations-side edit — closing the gap the pre-fix hardcoded 3-way
// if/else left: a NEW backend with its own project/global collision class (or
// its own model-nickname table) got NEITHER protection until someone
// remembered to add its name to operations' own copy of the list.

// ResolveModelFor translates rs.Model through the named backend's own
// resolveModel hook when it has one — the delegated-child launch path's
// model resolution (internal/engines/claude.ResolveModel today), generalized
// off a hardcoded "is this claude-code" branch in operations. A backend with
// no resolveModel hook (every backend but claude-code today) or an
// unregistered name passes model through unchanged with ok=true: "nothing to
// resolve" is not a failure.
func ResolveModelFor(name, model string) (resolved string, ok bool) {
	d, exists := lookup(name)
	if !exists || d.ResolveModel == nil {
		return model, true
	}
	return d.ResolveModel(model)
}

// CheckHookTargetScope refuses (or, with force, loudly warns) when workDir
// resolves onto the named backend's user-GLOBAL scope instead of a project's
// per-PROJECT scope — Claude Code's settings.json, codex's whole
// config.toml/prompts/skills home
// (see each one's hookGlobalScopePaths wiring in registry.go for the
// collision class itself). A backend with no hookGlobalScopePaths hook
// (opencode, mock — audited as unable to hit this
// collision; see each descriptor's comment) or an unregistered name is a
// no-op: nothing to guard.
//
// force downgrades a real collision to a loud warning and proceeds — the
// deliberate escape hatch for a genuine intentional global install.
func CheckHookTargetScope(name, workDir string, force bool) error {
	d, ok := lookup(name)
	if !ok {
		return nil
	}
	scope, ok := d.HookGlobalScope.Get()
	if !ok {
		return nil
	}
	projectPath, globalPath, err := scope.Paths(workDir)
	if err != nil {
		// No resolvable home directory: nothing to collide with.
		return nil
	}
	if cleanAbsPath(projectPath) != cleanAbsPath(globalPath) {
		return nil
	}
	label := scope.Label
	if force {
		clidiag.Warn("ctxloom", "hooks target %s resolves to %s (%s); proceeding because --force was given — this applies ctxloom to EVERY project, not just this one.", workDir, label, globalPath)
		return nil
	}
	return fmt.Errorf("refusing to install hooks: %s resolves to %s (%s), which would apply ctxloom to every project instead of just this one; run from inside a project (or set CTXLOOM_ROOT), or pass --force to proceed anyway", workDir, label, globalPath)
}

// UnregisterForTesting removes a name's descriptor entirely — Register's
// cleanup counterpart for a test's synthetic engine, so it does not linger in
// the shared, package-level table for later tests to trip over. It unwinds
// every table Register wrote.
func UnregisterForTesting(name string) {
	if d, ok := descriptors[name]; ok {
		isolation.RegisterCredentialSeed(name, agent.Declared[agent.CredentialSeed]{})
		isolation.RegisterProvisioningPolicy(name, agent.Declared[agent.ProvisioningPolicy]{})
		isolation.RegisterEngineContainer(name, agent.Declared[agent.EngineContainer]{}, engine.DistributionUnset)
		if _, had := d.InstanceConfig.Get(); had {
			isolation.RegisterInstanceConfigWriter(name, nil)
		}
	}
	delete(descriptors, name)
}

// InTreeAgentHomeSpec is one backend's ctxloom-CONTROLLED config home INSTANCE
// for one session's in-tree agent run: the engine's own home-relocation
// variable, the per-session directory it points at, and the preparation (if
// any) that has to happen before the engine is launched at it.
type InTreeAgentHomeSpec struct {
	// EnvVar is the engine's home-relocation variable (CLAUDE_CONFIG_DIR,
	// CODEX_HOME).
	EnvVar string
	// Dir is THIS SESSION's instance home, resolved through the owning engine
	// package's own paths.SessionHomePath-derived helper — the engine package
	// owns its own leaf, so no two engines can collide under one session root.
	Dir string
	// Subdir is the engine's DECLARED leaf (agent.HomeVar.Subdir) — Dir's last
	// element, stated rather than re-derived, so a run that presents the home
	// elsewhere (a container's fixed instance root) hangs it at the leaf the
	// engine declared, never at a guess from the host path.
	Subdir string
	// Prepare populates Dir before the engine is launched at it: the one-way
	// copy-in of ambient host material (credentials today) plus any
	// engine-specific scaffolding, returning an actionable error when there is
	// nothing to authenticate with. cwd is the directory the engine will
	// actually run in — what a generated workspace-trust answer must name.
	// nil when the backend needs neither.
	Prepare func(cwd string) error
}

// InTreeAgentHomeFor resolves the named backend's controlled config-home
// INSTANCE for (workDir, harp), or ok=false when that backend has none — or
// when the harp cannot name one. It is the polymorphic seam
// operations.ResolveInTreeAgentHome reads instead of branching on engine
// identity (ADR-0026) — the same shape ResolveModelFor and
// CheckHookTargetScope above have, and for the same reason.
//
// This answers only WHERE, never WHETHER. The scoping rule — controlled homes
// go to runs whose agent binding declares `engine_home: session`, whichever
// cell they run in — belongs to the caller and lives in ONE place there.
//
// harp is REQUIRED. An empty harp resolves nothing and creates nothing: there
// is no session-less instance to fall back to, and a project-wide fallback is
// exactly the durable home the per-session model retired. A harp that fails
// validation warns and declines, because a caller that got this far with an
// unusable session name has a bug the run should not paper over.
//
// It is DERIVED from the engine's Home declaration, not a slot of its own:
// the home var and its leaf are the same facts on every axis, so the
// in-tree instance is <session home>/<leaf> with the declared var pointing at
// it, prepared by THE ambient copy-in (isolation.CopyAmbient). An engine
// with Home declared absent (mock: no engine-global home to control) has no
// in-tree home, and that absence is its own declaration.
func InTreeAgentHomeFor(name, workDir, harp string) (InTreeAgentHomeSpec, bool) {
	d, exists := lookup(name)
	if !exists {
		return InTreeAgentHomeSpec{}, false
	}
	home, ok := d.Home.Get()
	if !ok || harp == "" {
		return InTreeAgentHomeSpec{}, false
	}
	// The error is harp validation (paths.SessionHomePath): an instance
	// cannot be named without a valid session, which is what keeps a durable
	// project-wide home from regrowing.
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harp)
	if err != nil {
		clidiag.Warn("ctxloom", "cannot resolve a per-session config home for %s in session %q (%v); this run uses the engine's own host config home instead", name, harp, err)
		return InTreeAgentHomeSpec{}, false
	}
	// ONE var, assumed explicitly: agent.EngineHome.Validate refuses a Home
	// with more than one var today, so Vars[0] is the var. An engine that
	// splits config and data across several vars needs this spec to become
	// a set (one EnvVar/Subdir per var) — lift it here when one does.
	v := home.Vars[0]
	engine := d.Name
	return InTreeAgentHomeSpec{
		EnvVar:  v.EnvVar,
		Dir:     filepath.Join(root, v.Subdir),
		Subdir:  v.Subdir,
		Prepare: func(cwd string) error { return prepareInTreeAmbient(engine, root, cwd) },
	}, true
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

// prepareInTreeAmbient is the Prepare every config-home instance shares: THE
// ambient copy-in (isolation.CopyAmbient) into this session's instance root,
// turning its "nothing seedable" DECISION into the actionable error
// operations.ResolveInTreeAgentHome fails loud on — the relocation is refused
// outright rather than point an engine at a home it cannot authenticate
// against. cwd is the directory the engine runs in, which the generated
// workspace-trust answer names.
func prepareInTreeAmbient(engine, instanceRoot, cwd string) error {
	report, err := isolation.CopyAmbient(isolation.AmbientRequest{
		Engine:       engine,
		InstanceHome: instanceRoot,
		WorkDir:      cwd,
	})
	if err != nil {
		return err
	}
	if report.NoSource {
		return fmt.Errorf("%s", report.NoSourceReason)
	}
	return nil
}
