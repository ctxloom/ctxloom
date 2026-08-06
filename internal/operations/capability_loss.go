package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CapabilityLoss reports, for one resolved engine binding, which parts of the
// named profiles' hooks configuration that engine has no structural place
// for — the SAME backends.UncarriedSurfaces read MaterializeProfile already
// performs and reports as "NOT carried" (whiny-exclusive), now available to a
// caller that names an engine binding without materializing anything at all:
// `agent show`, `doctor`, `manage status`, `acp list` (trusting-ambiguity).
//
// Only hooks are checked because UncarriedSurfaces only ever reports hooks
// today (see its doc). workDir/contextHash are irrelevant to which hooks are
// LOST (only to the synthetic SessionStart context-injection hook this call
// deliberately omits, passing contextHash "" exactly as
// backends.AssembleManagedConfig does), so this never needs a target
// directory the way `profile materialize` does.
//
// nil cfg or an empty backend name report no loss — there is nothing to
// resolve a hooks configuration against.
func CapabilityLoss(cfg *config.Config, backend string, profileNames []string) []agent.SurfaceLoss {
	if cfg == nil || backend == "" {
		return nil
	}
	hooks := backends.AssembleManagedHooks(cfg, "", "", profileNames).Wire()
	return backends.UncarriedSurfaces(backend, agent.SurfaceInputs{Hooks: hooks})
}
