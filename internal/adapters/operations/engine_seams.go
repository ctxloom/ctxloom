package operations

import (
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// checkHookTargetScopeOf refuses (or, with force, loudly warns) when workDir
// resolves onto the named backend's user-GLOBAL scope instead of a project's
// per-PROJECT scope — e.g. Claude Code's settings.json
// (see each one's hookGlobalScopePaths wiring in registry.go for the
// collision class itself). A backend with no hookGlobalScopePaths hook
// (mock — audited as unable to hit this collision; see its descriptor's
// comment) or an unregistered name is a no-op: nothing to guard.
//
// force downgrades a real collision to a loud warning and proceeds — the
// deliberate escape hatch for a genuine intentional global install.
func checkHookTargetScopeOf(reg engine.Registry, name, workDir string, force bool) error {
	h, ok := agent.HostedIn(reg, name)
	if !ok {
		return nil
	}
	scope, ok := h.HookGlobalScope()
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

// InTreeAgentHomeSpec is one backend's ctxloom-CONTROLLED config home INSTANCE
// for one session's in-tree agent run: the engine's own home-relocation
// variable, the per-session directory it points at, and the preparation (if
// any) that has to happen before the engine is launched at it.
type InTreeAgentHomeSpec struct {
	// EnvVar is the engine's home-relocation variable (e.g.
	// CLAUDE_CONFIG_DIR).
	EnvVar string
	// Dir is THIS SESSION's instance home: paths.HarpSessionEngineHomes with the
	// engine's own leaf appended — the engine owns its leaf, so no two
	// engines can collide under one session root.
	Dir string
	// Subdir is the engine's DECLARED leaf (engine.HomeVar.Subdir) — Dir's last
	// element, stated rather than re-derived, so a run that presents the home
	// elsewhere (a container's fixed instance root) hangs it at the leaf the
	// engine declared, never at a guess from the host path.
	Subdir string
	// Prepare readies Dir before the engine is launched at it
	// (isolation.PrepareInstanceHome). cwd is the directory the engine will
	// actually run in — what a generated workspace-trust answer must name.
	Prepare func(cwd string) error
}

// inTreeAgentHomeFor resolves the named backend's controlled config-home
// INSTANCE for harp, or ok=false when that backend has none — or when the
// harp cannot name one. It is the polymorphic seam
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
// It is DERIVED from the engine's Home declaration (Engine.Home), not a slot
// of its own: the home var and its leaf are the same facts on every axis, so
// the in-tree instance is <session home>/<leaf> with the declared var
// pointing at it, prepared by isolation.PrepareInstanceHome.
// An engine whose Home relocates nothing (mock: the zero HomeSpec) has no
// in-tree home, and that absence is its own declaration.
//
// How the run authenticates is not this seam's question: the agent's auth
// mode is resolved to env before the cell is prepared (resolveRunAuth),
// because a container's auth gate needs it first.
func inTreeAgentHomeFor(reg engine.Registry, name, harp string) (InTreeAgentHomeSpec, bool) {
	kind, exists := reg.Lookup(engine.Name(name))
	if !exists {
		return InTreeAgentHomeSpec{}, false
	}
	home := kind.Home()
	if !home.Relocates() || harp == "" {
		return InTreeAgentHomeSpec{}, false
	}
	// The error is harp validation (paths.HarpSessionEngineHomes): an instance
	// cannot be named without a valid session, which is what keeps a durable
	// project-wide home from regrowing.
	root, err := paths.HarpSessionEngineHomes(harp)
	if err != nil {
		clidiag.Warn("ctxloom", "cannot resolve a per-session config home for %s in session %q (%v); this run uses the engine's own host config home instead", name, harp, err)
		return InTreeAgentHomeSpec{}, false
	}
	// ONE var, assumed explicitly: no engine declares more than one today,
	// so Vars[0] is the var. An engine that splits config and data across
	// several vars needs this spec to become a set (one EnvVar/Subdir per
	// var) — lift it here when one does.
	v := home.Vars[0]
	engine := name
	return InTreeAgentHomeSpec{
		EnvVar: v.Name,
		Dir:    filepath.Join(root, v.Subdir),
		Subdir: v.Subdir,
		Prepare: func(cwd string) error {
			return prepareInstanceHome(engine, root, cwd)
		},
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

// prepareInstanceHome is the Prepare every config-home instance shares
// (isolation.PrepareInstanceHome). cwd is the directory the engine runs in,
// which the generated workspace-trust answer names.
func prepareInstanceHome(engine, instanceRoot, cwd string) error {
	_, err := isolation.PrepareInstanceHome(isolation.InstanceHomeRequest{
		Engine:       engine,
		InstanceHome: instanceRoot,
		WorkDir:      cwd,
	})
	return err
}
