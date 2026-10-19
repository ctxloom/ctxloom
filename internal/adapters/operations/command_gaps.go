package operations

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/toolchain"
)

// CommandGaps is the read of whether the commands each agent requires (its
// composed bundles' `requires`, and the launchers of the stdio MCP servers it
// attaches) resolve where that agent runs.
type CommandGaps struct {
	// Missing maps an agent to the requirements that do not resolve in its
	// runtime (the host PATH, or its container image), sorted by command, each
	// carrying its sources. Agents with no gap are absent.
	Missing map[string][]toolchain.Requirement
	// ImageNotBuilt names the container agents whose image is not in any
	// runtime yet, so nothing could be probed for them; sorted.
	ImageNotBuilt []string
}

// CommandGapsByAgent resolves, for every configured agent, its required
// commands (RequiredCommandsForResolved) and asks the agent's
// own runtime whether each resolves: exec.LookPath for a host agent, `command
// -v` inside the agent's already-built image for a container agent
// (isolation.Container.MissingCommands, which never builds or pulls).
//
// Only the launcher of an MCP server is checked, never the package behind it:
// `npx foo` and `uvx foo` pass whenever npx or uvx exist, and fail later at
// fetch time.
//
// An agent that fails to RESOLVE is skipped; the resolution failure is its
// own finding (DOCTOR-CHECK-AGENTS-b2). An image no runtime holds is reported
// in ImageNotBuilt rather than as a gap: it gets built on first launch.
func CommandGapsByAgent(ctx context.Context, reg engine.Registry, cfg *config.Config, runtimes []isolation.Runtime) CommandGaps {
	gaps := CommandGaps{Missing: map[string][]toolchain.Requirement{}}
	if cfg == nil {
		return gaps
	}
	configured := cfg.GetConfiguredAgents()
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		resolved, err := ResolveAgent(ctx, reg, cfg, name, "")
		if err != nil {
			continue
		}
		required, err := RequiredCommandsForResolved(cfg, resolved)
		if err != nil || len(required) == 0 {
			continue
		}
		commands := toolchain.Commands(required)
		var missing []string
		if launch.IsContainerRuntimeAxis(resolved.Runtime) {
			var built bool
			missing, built = missingInContainer(ctx, runtimes, resolved.Backend, cfg, commands)
			if !built {
				gaps.ImageNotBuilt = append(gaps.ImageNotBuilt, name)
				continue
			}
		} else {
			missing = doctorMissingFromPath(commands)
		}
		if len(missing) > 0 {
			gaps.Missing[name] = requirementsNamed(required, missing)
		}
	}
	return gaps
}

// missingInContainer probes the agent image in the first runtime that holds
// it. built is false when no runtime does (or none is available), or when the
// probe itself could not run: nothing was learned, so nothing is reported.
func missingInContainer(ctx context.Context, runtimes []isolation.Runtime, backend string, cfg *config.Config, commands []string) (missing []string, built bool) {
	img := launch.ImageConfigFor(cfg, engine.Name(backend))
	for _, rt := range runtimes {
		ref, ok := isolation.LiveImageRef(rt, backend, img)
		if !ok {
			continue
		}
		missing, err := isolation.NewContainerFor(rt, backend).WithImage(ref).MissingCommands(ctx, commands)
		if err != nil {
			continue
		}
		return missing, true
	}
	return nil, false
}

// requirementsNamed keeps the requirements whose command is in names, in
// required's order.
func requirementsNamed(required []toolchain.Requirement, names []string) []toolchain.Requirement {
	var out []toolchain.Requirement
	for _, r := range required {
		if slices.Contains(names, r.Command) {
			out = append(out, r)
		}
	}
	return out
}

// commandGapsDetail renders the missing requirements as doctor's one-line
// detail, each command with the sources that require it:
// "dev-rust: cargo (bundle rust-development), serena (mcp serena)".
func commandGapsDetail(missing map[string][]toolchain.Requirement) string {
	agentNames := slices.Sorted(maps.Keys(missing))
	parts := make([]string, 0, len(agentNames))
	for _, a := range agentNames {
		cmds := make([]string, 0, len(missing[a]))
		for _, r := range missing[a] {
			srcs := make([]string, len(r.Sources))
			for i, src := range r.Sources {
				srcs[i] = src.String()
			}
			cmds = append(cmds, fmt.Sprintf("%s (%s)", r.Command, strings.Join(srcs, ", ")))
		}
		parts = append(parts, fmt.Sprintf("%s: %s", a, strings.Join(cmds, ", ")))
	}
	return strings.Join(parts, "; ")
}
