package cli

import (
	"fmt"
	"os"

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
func standUpRunner(cmd *cobra.Command, backend agent.Backend, backendName string) (*runnerStandup, error) {
	// RUNNER-TERMINATED MCP: the per-spawn seam stamps the coordinator trio
	// onto THIS process's env. Consume it FIRST and scrub it — the runner is
	// the ONE credential holder; the harness and its subprocesses must never
	// inherit it.
	homeCfg := coord.HomeConfig{
		URL:     os.Getenv(coord.EnvCoordURL),
		Token:   os.Getenv(coord.EnvCoordCred),
		RunID:   os.Getenv(coord.EnvRunID),
		Harness: backendName,
		Version: Version,
	}
	harp := os.Getenv("CTXLOOM_SESSION_HARP")
	coordinatorCapable := os.Getenv(coord.EnvAgentCoordinator) == "1"
	// cellWorkDir is the prepared workspace dir stamped by the host StartRunner
	// (fix/host-discovery-anchor); empty on workspace:none or container spawns,
	// where serveRunnerMCP falls back to the runner's own os.Getwd().
	cellWorkDir := os.Getenv(coord.EnvCellWorkDir)
	for _, k := range []string{coord.EnvCoordURL, coord.EnvCoordCred, coord.EnvRunID, coord.EnvAgentCoordinator, coord.EnvCellWorkDir} {
		_ = os.Unsetenv(k)
	}

	// A LEAF delegated child (RunID set) not resolved Coordinator-capable must
	// not receive the coordinator-only MCP tools. The top-level human session
	// never sets RunID, so the human is never gated.
	leaf := homeCfg.RunID != "" && !coordinatorCapable

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		clidiag.Warn("ctxloom", "config load failed; serving %s unconfigured: %v", backendName, cfgErr)
	}
	if cfg != nil {
		if bc := serveBackendConfig(cfg, backendName, llmServeLabel); bc != nil {
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

	if cfg != nil {
		endpoint, merr := serveRunnerMCP(cfg, harp, h, leaf, cellWorkDir)
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
			// Exported into THIS process env: every engine spawn path builds
			// the harness env over os.Environ.
			_ = os.Setenv(coord.EnvMCPSocket, endpoint.socketPath)
		}
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
