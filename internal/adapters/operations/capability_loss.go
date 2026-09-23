package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/adapters/operations/managedhooks"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// CapabilityLoss reports, for one resolved engine binding, which parts of the
// named profiles' hooks configuration that engine has no structural place
// for — the SAME uncarriedSurfaces read MaterializeProfile already
// performs and reports as "NOT carried", now available to a
// caller that names an engine binding without materializing anything at all:
// `agent show`, `doctor`, `manage check` (trusting-ambiguity).
//
// Only hooks are checked because UncarriedSurfaces only ever reports hooks
// today (see its doc). workDir/contextHash are irrelevant to which hooks are
// LOST (only to the synthetic SessionStart context-injection hook this call
// deliberately omits, passing contextHash "" exactly as
// managedhooks.Assemble does), so this never needs a target
// directory the way `profile materialize` does.
//
// nil cfg or an empty backend name report no loss — there is nothing to
// resolve a hooks configuration against.
func CapabilityLoss(reg engine.Registry, cfg *config.Config, backend string, profileNames []string) []agent.SurfaceLoss {
	if cfg == nil || backend == "" {
		return nil
	}
	if cfg.ShouldSilenceUnsupported() {
		return nil
	}
	hooks := managedhooks.Assemble(report.To(cfg.Reporter()), cfg, "", "", profileNames).WireDeclared()
	return uncarriedSurfaces(reg, backend, agent.SurfaceInputs{Hooks: hooks})
}

// CapabilityLossByAgent is the roster-wide read of CapabilityLoss: for every
// agent this project has configured, what the engine it resolves to drops of
// the hooks its profiles actually configure. It is the single computation
// `ctxloom doctor` and `ctxloom manage check` share; a second way to compute
// the same fact is how the surfaces drift into disagreeing about what a
// user's engine can carry.
//
// Agents that lose nothing are omitted entirely rather than listed as clean:
// the same "only when it costs something" rule uncarriedSurfaces
// itself applies, so a caller can render the result unconditionally and stay
// silent on a healthy project. Entries come out sorted by agent name, so the
// report can be diffed across runs rather than reshuffling with a map's
// range order.
//
// An agent that fails to RESOLVE is skipped, not reported: there is no engine
// binding to name a loss against, and the resolution failure is already its
// own finding (DOCTOR-CHECK-AGENTS-b2). Saying it twice in two vocabularies
// would make neither line believable.
func CapabilityLossByAgent(ctx context.Context, reg engine.Registry, cfg *config.Config) []AgentSurfaceLoss {
	if cfg == nil {
		return nil
	}
	configured := cfg.GetConfiguredAgents()
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []AgentSurfaceLoss
	for _, name := range names {
		resolved, err := ResolveAgent(ctx, reg, cfg, name, "")
		if err != nil {
			continue
		}
		losses := CapabilityLoss(reg, cfg, resolved.Backend, resolved.Profiles)
		if len(losses) == 0 {
			continue
		}
		out = append(out, AgentSurfaceLoss{Agent: name, Backend: resolved.Backend, Losses: losses})
	}
	return out
}

// CapabilityLossLines is one line per (agent, loss), in the SAME words
// `profile materialize` and `agent show` use — SurfaceLoss.String() is the
// one renderer, so the surfaces cannot describe one engine's gap several
// different ways — prefixed with the agent that is paying for it. Every
// frontend's rendering is built from these lines.
func CapabilityLossLines(entries []AgentSurfaceLoss) []string {
	var lines []string
	for _, e := range entries {
		for _, loss := range e.Losses {
			lines = append(lines, fmt.Sprintf("%s (%s): %s", e.Agent, e.Backend, loss))
		}
	}
	return lines
}

// capabilityLossDetail folds the lines into doctor's one-line-per-check
// Detail shape.
func capabilityLossDetail(entries []AgentSurfaceLoss) string {
	return strings.Join(CapabilityLossLines(entries), "; ")
}
