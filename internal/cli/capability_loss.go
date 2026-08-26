package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file is the roster-wide read of capability loss: for every agent this
// project has configured, what the engine it resolves to has NO structural
// place for. It is the single computation `ctxloom doctor` and `ctxloom manage
// check` share, and it computes nothing of its own — operations.CapabilityLoss
// (which wraps the identical backends.UncarriedSurfaces call `profile
// materialize` reports as "NOT carried", and which `agent show` already
// reuses) remains the one place the question is answered.
//
// Reading it here rather than re-deriving it is the point: a second way to
// compute the same fact is how the four surfaces drift into disagreeing about
// what a user's engine can carry, which is the failure this whole report
// exists to prevent.

// capabilityLossByAgent reports, per configured agent, what its resolved
// engine binding drops of the hooks its profiles actually configure. Agents
// that lose nothing are omitted entirely rather than listed as clean: the same
// "only when it costs something" rule backends.UncarriedSurfaces itself
// applies, so a caller can render the result unconditionally and stay silent
// on a healthy project.
//
// Entries come out sorted by agent name, so the report can be diffed across
// runs rather than reshuffling with a map's range order.
//
// An agent that fails to RESOLVE is skipped, not reported: there is no engine
// binding to name a loss against, and the resolution failure is already its
// own finding (DOCTOR-CHECK-AGENTS-b2). Saying it twice in two vocabularies
// would make neither line believable.
func capabilityLossByAgent(ctx context.Context, cfg *config.Config) []operations.AgentSurfaceLoss {
	if cfg == nil {
		return nil
	}
	configured := cfg.GetConfiguredAgents()
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []operations.AgentSurfaceLoss
	for _, name := range names {
		resolved, err := operations.ResolveAgent(ctx, cfg, name, "")
		if err != nil {
			continue
		}
		losses := operations.CapabilityLoss(cfg, resolved.Backend, resolved.Profiles)
		if len(losses) == 0 {
			continue
		}
		out = append(out, operations.AgentSurfaceLoss{Agent: name, Backend: resolved.Backend, Losses: losses})
	}
	return out
}

// capabilityLossLines renders one report line per (agent, loss), in the SAME
// words `profile materialize` and `agent show` use — SurfaceLoss.String() is
// the one renderer, so the four surfaces cannot describe one engine's gap
// four different ways — prefixed with the agent that is paying for it.
func capabilityLossLines(entries []operations.AgentSurfaceLoss) []string {
	var lines []string
	for _, e := range entries {
		for _, loss := range e.Losses {
			lines = append(lines, fmt.Sprintf("NOT carried by %s (%s): %s", e.Agent, e.Backend, loss))
		}
	}
	return lines
}

// renderCapabilityLosses writes the loss section of a wiring report, and
// writes NOTHING AT ALL when nothing is lost — matching printSurfaceCurrencies'
// rule and UncarriedSurfaces' own: a bare, unlabelled "Capability loss:"
// heading over an empty list is the fastest way to teach a reader to skip the
// line that matters.
func renderCapabilityLosses(w io.Writer, entries []operations.AgentSurfaceLoss) {
	lines := capabilityLossLines(entries)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "\nCapability loss — configured, but this engine has nowhere to put it:")
	for _, line := range lines {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

// capabilityLossDetail folds the same lines into doctor's one-line-per-check
// Detail shape, dropping the repeated "NOT carried by" prefix the marker and
// status already imply.
func capabilityLossDetail(entries []operations.AgentSurfaceLoss) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		for _, loss := range e.Losses {
			parts = append(parts, fmt.Sprintf("%s (%s): %s", e.Agent, e.Backend, loss))
		}
	}
	return strings.Join(parts, "; ")
}
