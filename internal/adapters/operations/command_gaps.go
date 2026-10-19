package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// CommandGaps is the read of whether the stdio MCP servers each agent
// attaches can start where that agent runs.
type CommandGaps struct {
	// Missing maps an agent to the commands that do not resolve in its
	// runtime (the host PATH, or its container image), sorted. Agents with no
	// gap are absent.
	Missing map[string][]string
	// ImageNotBuilt names the container agents whose image is not in any
	// runtime yet, so nothing could be probed for them; sorted.
	ImageNotBuilt []string
}

// CommandGapsByAgent resolves, for every configured agent, the launcher of
// each stdio MCP server its profiles attach (the first word of the server's
// command; URL and session-endpoint servers have none) and asks the agent's
// own runtime whether it resolves: exec.LookPath for a host agent, `command
// -v` inside the agent's already-built image for a container agent
// (isolation.Container.MissingCommands, which never builds or pulls).
//
// Only the launcher is checked, never the package behind it: `npx foo` and
// `uvx foo` pass whenever npx or uvx exist, and fail later at fetch time.
//
// An agent that fails to RESOLVE is skipped; the resolution failure is its
// own finding (DOCTOR-CHECK-AGENTS-b2). An image no runtime holds is reported
// in ImageNotBuilt rather than as a gap: it gets built on first launch.
func CommandGapsByAgent(ctx context.Context, reg engine.Registry, cfg *config.Config, runtimes []isolation.Runtime) CommandGaps {
	gaps := CommandGaps{Missing: map[string][]string{}}
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
		commands := stdioLaunchers(cfg, resolved.Profiles)
		if len(commands) == 0 {
			continue
		}
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
			gaps.Missing[name] = missing
		}
	}
	return gaps
}

// stdioLaunchers is the sorted, de-duplicated first word of every stdio MCP
// server the profiles attach. A server with a URL or served by the session's
// endpoint carries no command, so it contributes none.
func stdioLaunchers(cfg *config.Config, profiles []string) []string {
	set := map[string]bool{}
	for _, srv := range cfg.ResolveBundleMCPServers(profiles) {
		if srv.URL != "" || srv.ServedBy != "" {
			continue
		}
		if fields := strings.Fields(srv.Command); len(fields) > 0 {
			set[fields[0]] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
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

// commandGapsDetail renders the missing commands as doctor's one-line
// "agent -> commands" detail.
func commandGapsDetail(missing map[string][]string) string {
	agentNames := make([]string, 0, len(missing))
	for a := range missing {
		agentNames = append(agentNames, a)
	}
	sort.Strings(agentNames)
	parts := make([]string, 0, len(agentNames))
	for _, a := range agentNames {
		parts = append(parts, fmt.Sprintf("%s -> %s", a, strings.Join(missing[a], ", ")))
	}
	return strings.Join(parts, "; ")
}
