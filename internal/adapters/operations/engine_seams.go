package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
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
