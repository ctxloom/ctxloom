package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// runnerStandup is the shared result of standUpRunner: the dialed-home runner's
// lifecycle handles plus the teardown both `llm serve` (go-plugin transport)
// and `llm host` (docker-direct, no plugin) run AFTER their own blocking wait.
// home/engineHost are nil when the coordinator trio was absent (an
// unconfigured/top-level serve, or a `llm host` launched with no reach-back).
type runnerStandup struct {
	home          *coord.Home
	engineHost    *coord.EngineHost
	endpointClose func()
}

// standUpRunner performs the runner standup shared by `llm serve` and `llm
// host` (queer-shrug §4.3): consume + scrub the coordinator reach-back trio,
// load + apply the backend config, stand up the EngineHost for a delegated
// StructuredChat run, dial home, stand up the runner-local MCP socket, and
// BindHome — everything llm_serve.go's body did EXCEPT the transport tail
// (plugin.Serve vs a lifecycle block, which each caller owns). It returns the
// standup on success, or a FATAL error (a hosted run whose MCP endpoint failed
// — never launch its engine with no reach-back) after closing home itself.
//
// label is the config label whose LLM entry configures the backend, passed by
// whichever command owns the standup — each has its own --label flag, and a
// parameter is what keeps the three from sharing one mutable package global.
func standUpRunner(cmd *cobra.Command, backend agent.Backend, backendName, label string) (*runnerStandup, error) {
	reach, rerr := consumeCoordinatorReachBack(backendName, os.Getenv, os.Unsetenv)
	if rerr != nil {
		return nil, rerr
	}
	homeCfg := reach.home

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		clidiag.Warn("ctxloom", "config load failed; serving %s unconfigured: %v", backendName, cfgErr)
	}
	if cfg != nil {
		// U037-F03: config.Load downgrades an unreadable/malformed/schema-invalid
		// config.yaml to warnings rather than an error — surface them (and
		// record the fatal-class findings the RunE's failOnFindings gate below
		// checks) so a corrupted config never silently launches an
		// empty/partial-context engine. Mirrors GetConfig/GetConfigForUpdate
		// (root.go) and runMCPServerSDK (mcp_server.go), the other
		// process-owning entry points.
		printConfigWarnings(os.Stderr, cfg.GetWarnings())
		if bc := serveBackendConfig(cfg, backendName, label); bc != nil {
			if c, ok := backend.(backends.Configurable); ok {
				c.Configure(bc)
			}
		}
	}

	standup := &runnerStandup{}
	if homeCfg.URL == "" || homeCfg.Token == "" {
		// No reach-back: nothing to dial or host (an unconfigured/top-level
		// serve, or a `llm host` launched without a coordinator).
		return standup, nil
	}

	if homeCfg.RunID != "" {
		if sc, ok := backend.(agent.StructuredChat); ok {
			standup.engineHost = coord.NewEngineHost(cmd.Context(), sc, backendName, homeCfg.RunID)
			homeCfg.Engine = standup.engineHost.Handle
		}
	}
	h, herr := coord.NewHome(cmd.Context(), homeCfg)
	if herr != nil {
		clidiag.Warn("ctxloom", "runner dial-home failed (coordinator will synthesize loss): %v", herr)
		return standup, nil
	}
	standup.home = h

	switch {
	case cfg != nil:
		endpoint, merr := serveRunnerMCP(cfg, reach.harp, h, reach.leaf, reach.cellWorkDir)
		if merr == nil {
			// The child's shim keys off CTXLOOM_MCP_SOCKET in THIS process's env
			// (every engine spawn path builds the harness env over os.Environ), so
			// a failed export leaves the endpoint standing and unreachable — the
			// same end state as no endpoint at all, and treated as the same
			// failure below rather than swallowed.
			merr = exportRunnerMCPSocket(os.Setenv, endpoint.socketPath)
			if merr != nil {
				endpoint.close()
			}
		}
		switch {
		case merr != nil && standup.engineHost != nil:
			// FAIL LOUD (icy-value): a runner that HOSTS a delegated run must
			// not launch its engine without this endpoint — the child's shim
			// keys entirely off CTXLOOM_MCP_SOCKET and would otherwise stand up
			// a rogue local coordinator nobody reads.
			h.Close(1, "")
			return nil, fmt.Errorf("runner MCP endpoint failed and this runner hosts delegated run %s — refusing to launch its engine with no reach-back: %w", homeCfg.RunID, merr)
		case merr != nil:
			clidiag.Warn("ctxloom", "runner MCP endpoint failed (the harness shim will fall back to its local mode): %v", merr)
		default:
			standup.endpointClose = endpoint.close
		}
	case runnerMustRefuseNoConfigReachBack(cfg, standup.engineHost):
		// U037-F05: config.Load failed (cfg == nil), so there is no
		// runner-local MCP to stand up or dial — exactly the same
		// "hosted delegated run with no reach-back" condition the merr branch
		// above refuses for. Hoist the identical fail-loud here instead of
		// falling through to BIND LAST below and binding EngineHost with
		// CTXLOOM_MCP_SOCKET never exported (the same end state, silently).
		h.Close(1, "")
		return nil, fmt.Errorf("config load failed and this runner hosts delegated run %s — refusing to launch its engine with no reach-back: %w", homeCfg.RunID, cfgErr)
	}
	// BIND LAST — strictly after the MCP socket exists and its env is exported
	// (icy-value): BindHome unblocks EngineHost.Handle, and StartRun is what
	// spawns the child's engine; binding earlier races the coordinator's
	// StartRun against socket bind + tool-schema generation.
	if standup.engineHost != nil {
		standup.engineHost.BindHome(h)
	}
	return standup, nil
}

// coordinatorReachBack is the per-spawn coordinator credential set a runner
// consumes from its own environment: the dial-home config, the session harp, the
// prepared cell workspace dir, and whether this runner is a LEAF (a delegated
// child that was not resolved Coordinator-capable, so it must not be served the
// coordinator-only MCP tools; the top-level human session never sets a RunID, so
// the human is never gated).
type coordinatorReachBack struct {
	home coord.HomeConfig
	harp string
	leaf bool
	// cellWorkDir is the prepared workspace dir stamped by the host StartRunner
	// (fix/host-discovery-anchor); empty on workspace:none or container spawns,
	// where serveRunnerMCP falls back to the runner's own os.Getwd().
	cellWorkDir string
}

// coordinatorEnvKeys is the reach-back set the per-spawn seam stamps onto a
// runner's environment. Every one of them must be GONE from this process before
// it spawns anything.
var coordinatorEnvKeys = []string{
	coord.EnvCoordURL,
	coord.EnvCoordCred,
	coord.EnvRunID,
	coord.EnvAgentCoordinator,
	coord.EnvCellWorkDir,
}

// consumeCoordinatorReachBack reads the coordinator reach-back out of the
// environment and SCRUBS it: the runner is the ONE credential holder, and the
// harness and every subprocess it spawns inherit this process's environment.
//
// A key that survives the scrub is a fatal error, not a warning. The engine is
// third-party code; leaving the coordinator credential in the environment it
// inherits is the isolation failing open, and there is no partial success to
// report — either the token is gone or it is handed over.
//
// getenv/unset are parameters because the failure they guard cannot be provoked
// through the real syscalls: on unix syscall.Unsetenv always reports success, so
// the branch is reachable only under test and on Windows. Injecting them is what
// makes the invariant testable at all.
func consumeCoordinatorReachBack(backendName string, getenv func(string) string, unset func(string) error) (coordinatorReachBack, error) {
	reach := coordinatorReachBack{
		home: coord.HomeConfig{
			URL:     getenv(coord.EnvCoordURL),
			Token:   getenv(coord.EnvCoordCred),
			RunID:   getenv(coord.EnvRunID),
			Harness: backendName,
			Version: Version,
		},
		harp:        getenv("CTXLOOM_SESSION_HARP"),
		cellWorkDir: getenv(coord.EnvCellWorkDir),
	}
	reach.leaf = reach.home.RunID != "" && getenv(coord.EnvAgentCoordinator) != "1"

	var unscrubbed []string
	for _, k := range coordinatorEnvKeys {
		if err := unset(k); err != nil {
			unscrubbed = append(unscrubbed, fmt.Sprintf("%s (%v)", k, err))
		}
	}
	if len(unscrubbed) > 0 {
		return coordinatorReachBack{}, fmt.Errorf("refusing to launch: the coordinator reach-back could not be scrubbed from this runner's environment, so the engine child would inherit the coordinator credential: %s", strings.Join(unscrubbed, ", "))
	}
	return reach, nil
}

// exportRunnerMCPSocket publishes the runner-local MCP socket path into THIS
// process's environment, which is where the engine child's shim reads it from
// (every engine spawn path builds the harness env over os.Environ). A failure
// here leaves the endpoint listening with nobody able to address it, so it is
// reported rather than dropped — the caller treats it exactly like an endpoint
// that never came up.
//
// set is a parameter for the same reason consumeCoordinatorReachBack's unset is:
// os.Setenv cannot fail for this constant key and a NUL-free socket path, so the
// error branch is otherwise untestable.
func exportRunnerMCPSocket(set func(string, string) error, socketPath string) error {
	if err := set(coord.EnvMCPSocket, socketPath); err != nil {
		return fmt.Errorf("export %s=%s (the engine child's shim reads the socket from this env): %w", coord.EnvMCPSocket, socketPath, err)
	}
	return nil
}

// runnerMustRefuseNoConfigReachBack reports whether standUpRunner must
// refuse to launch its engine because this runner hosts a delegated run
// (engineHost != nil, i.e. it has a RunID and a StructuredChat backend) but
// config.Load() failed, so there is no config to build a runner-local MCP
// endpoint from (U037-F05). Binding EngineHost in that state would let the
// engine launch with CTXLOOM_MCP_SOCKET never exported — the same "hosted
// delegated run with no reach-back" condition standUpRunner's merr branch
// (a few lines up) already refuses for when serveRunnerMCP itself fails.
// Extracted as a pure predicate so the branch condition is unit-testable
// without needing config.Load() to actually fail — which the loader's own
// fault tolerance (CLAUDE.md) makes hard to trigger from real file content;
// nearly every load fault degrades to a warning (cfg != nil, cfg.GetWarnings()
// non-empty) rather than this cfg == nil path.
func runnerMustRefuseNoConfigReachBack(cfg *config.Config, engineHost *coord.EngineHost) bool {
	return cfg == nil && engineHost != nil
}

// teardown reports the runner's exit through home.Close, mirroring
// llm_serve.go's original tail exactly (engine host joined first so an
// in-flight adapt can finish its terminal RunCompleted while home is still
// live; the MCP endpoint closes last).
func (s *runnerStandup) teardown() {
	if s.engineHost != nil {
		s.engineHost.Close()
	}
	if s.home != nil {
		s.home.Close(0, "")
	}
	if s.endpointClose != nil {
		s.endpointClose()
	}
}
