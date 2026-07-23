package cli

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var llmServeCmd = &cobra.Command{
	Use:     "serve <backend>",
	Aliases: []string{"srv"},
	Short:   "Run as a plugin server (internal use)",
	Long:    `Starts the ctxloom binary as a plugin server for the specified backend. This is used internally by the plugin system.`,
	Args:    cobra.ExactArgs(1),
	Hidden:  true, // Hide from help since it's for internal use
	RunE: func(cmd *cobra.Command, args []string) error {
		backendName := args[0]

		// Get the backend from the registry
		backend := backends.Get(backendName)
		if backend == nil {
			return fmt.Errorf("unknown backend: %s", backendName)
		}

		// RUNNER-TERMINATED MCP (agentcoord B1.6): the per-spawn seam stamps
		// the coordinator trio onto THIS process's env. Consume it FIRST and
		// scrub it — the runner is the ONE credential holder; the harness
		// and its subprocesses must never inherit it.
		homeCfg := coord.HomeConfig{
			URL:     os.Getenv(coord.EnvCoordURL),
			Token:   os.Getenv(coord.EnvCoordCred),
			RunID:   os.Getenv(coord.EnvRunID),
			Harness: backendName,
			Version: Version,
		}
		harp := os.Getenv("CTXLOOM_SESSION_HARP")
		coordinatorCapable := os.Getenv(coord.EnvAgentCoordinator) == "1"
		// cellWorkDir is the prepared workspace dir (isolation.Workspace.Dir())
		// stamped by None.SpawnClient (fix/host-discovery-anchor) — this
		// runner's own os.Getwd() never changes to it (see EnvCellWorkDir's
		// doc), but the discovery marker must be keyed by it so it agrees
		// with the shim's cwd (=the child's WorkDir). Read + scrub alongside
		// the trio; empty on workspace:none or container spawns, where
		// serveRunnerMCP falls back to the runner's own os.Getwd().
		cellWorkDir := os.Getenv(coord.EnvCellWorkDir)
		for _, k := range []string{coord.EnvCoordURL, coord.EnvCoordCred, coord.EnvRunID, coord.EnvAgentCoordinator, coord.EnvCellWorkDir} {
			_ = os.Unsetenv(k)
		}

		// Trust-boundary gate: a LEAF delegated child (RunID set: this
		// runner was spawned for a specific agent_run/resume, not the
		// top-level human session) that was NOT resolved as
		// Coordinator-capable must not receive the coordinator-only MCP
		// tools (agent_run/roster/agent_stop/agent_fetch_artifact) - a leaf
		// holding an agent_recv inbox plus a roster infers it has children
		// and stalls waiting for notifications that never arrive. CRITICAL:
		// the top-level human `ctxloom run` session never sets RunID
		// (sessionOwnerEnv, coord_host.go), so RunID == "" here means the
		// human is NEVER gated - preserved exactly.
		leaf := homeCfg.RunID != "" && !coordinatorCapable

		// Load config and apply the first labeled entry whose type matches this
		// backend. serve receives only the backend type (the self-invoked
		// transport names backends, not labels), so binary/args/env come from
		// any matching entry; the model + env for a specific run are carried on
		// the request itself.
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			// Degrade to an unconfigured backend, but say so — every other
			// startup path warns on a config-load failure.
			clidiag.Warn("ctxloom", "config load failed; serving %s unconfigured: %v", backendName, cfgErr)
		}
		if cfg != nil {
			if bc := serveBackendConfig(cfg, backendName, llmServeLabel); bc != nil {
				if c, ok := backend.(backends.Configurable); ok {
					c.Configure(bc)
				}
			}
		}

		// With the trio present, this runner dials home (RunnerChannel
		// lifecycle + the RunChannel every coordination tool rides, both
		// reconnecting) and TERMINATES MCP: the surface serves on a
		// container-local unix socket, exported to the harness via
		// CTXLOOM_MCP_SOCKET so its stdio `ctxloom mcp` shim forwards here.
		// The socket listens BEFORE the harness can spawn (plugin.Serve
		// below is what accepts the Chat/Run that spawns it — assert, don't
		// race). A dial failure is a warning, never a launch blocker: the
		// coordinator synthesizes RunExited on runner loss either way.
		//
		// A SPAWNED run's runner (RunID set) whose backend speaks
		// StructuredChat also stands up the ENGINE HOST: the coordinator's
		// StartRun launches the conversation IN-PROCESS here (Wave C1) —
		// the go-plugin Chat RPC is never dialed for a delegated child;
		// go-plugin remains only this process's spawn/kill transport.
		var home *coord.Home
		var engineHost *coord.EngineHost
		if homeCfg.URL != "" && homeCfg.Token != "" {
			if homeCfg.RunID != "" {
				if sc, ok := backend.(agent.StructuredChat); ok {
					engineHost = coord.NewEngineHost(cmd.Context(), sc, backendName, homeCfg.RunID)
					homeCfg.Engine = engineHost.Handle
				}
			}
			h, herr := coord.NewHome(cmd.Context(), homeCfg)
			if herr != nil {
				clidiag.Warn("ctxloom", "runner dial-home failed (coordinator will synthesize loss): %v", herr)
			} else {
				home = h
				if cfg != nil {
					endpoint, merr := serveRunnerMCP(cfg, harp, home, leaf, cellWorkDir)
					switch {
					case merr != nil && engineHost != nil:
						// FAIL LOUD (icy-value). A runner that HOSTS a
						// delegated run must not launch its engine without
						// this endpoint: the child's `ctxloom mcp` shim
						// keys entirely off CTXLOOM_MCP_SOCKET, and without
						// it that shim silently runs its LOCAL surface —
						// which stands up a SECOND, rogue coordinator in
						// the shim process and offers an `agent_send`
						// (to/body) that can never reach the real parent.
						// A child that reports into a coordinator nobody
						// reads is worse than a child that never launched.
						home.Close(1, "")
						return fmt.Errorf("runner MCP endpoint failed and this runner hosts delegated run %s — refusing to launch its engine with no reach-back: %w", homeCfg.RunID, merr)
					case merr != nil:
						clidiag.Warn("ctxloom", "runner MCP endpoint failed (the harness shim will fall back to its local mode): %v", merr)
					default:
						defer endpoint.close()
						// Exported into THIS process env: every engine spawn
						// path builds the harness env over os.Environ.
						_ = os.Setenv(coord.EnvMCPSocket, endpoint.socketPath)
					}
				}
				// BIND LAST — strictly after the MCP socket exists and its
				// env is exported (icy-value). BindHome is what unblocks
				// EngineHost.Handle, and StartRun is what spawns the child's
				// engine: binding before this point raced the coordinator's
				// StartRun (a local gRPC round trip) against socket bind +
				// mcpschema tool-schema generation on this goroutine, and a
				// lost race spawned the engine with CTXLOOM_MCP_SOCKET
				// unset. Handle already waits on the bind (homeBindTimeout),
				// so ordering it this way costs a bounded wait instead of a
				// silently mis-wired child.
				if engineHost != nil {
					engineHost.BindHome(home)
				}
			}
		}

		// Create the plugin map with our backend
		pluginMap := map[string]plugin.Plugin{
			pb.LLMPluginKey: &pb.LLMGRPCPlugin{Impl: backend},
		}

		// Serve the plugin
		plugin.Serve(&plugin.ServeConfig{
			HandshakeConfig: pb.HandshakeConfig,
			Plugins:         pluginMap,
			GRPCServer:      plugin.DefaultGRPCServer,
		})

		if engineHost != nil {
			// Cancel and join the hosted run's own goroutines FIRST (deaf-rut):
			// if adapt is still mid-stream, this lets it finish its terminal
			// RunCompleted/ReportRunExited via home while home is still
			// live, before home itself tears down below.
			engineHost.Close()
		}
		if home != nil {
			// The harness exited with the plugin: report it. docker-stop
			// rarely gives this path a chance — synthesis covers that.
			home.Close(0, "")
		}
		return nil
	},
}

var llmServeLabel string

// serveBackendConfig picks the labeled entry to configure the served backend
// with. The label the run resolved wins (verified against the backend type so
// a stale flag can't misconfigure); the type-based scan remains the fallback
// for callers that only know the type — its map-order tie is exactly why the
// run path passes the label through.
func serveBackendConfig(cfg *config.Config, backendName, label string) agent.BackendConfig {
	if label != "" {
		if entry, ok := cfg.GetLLMEntry(label); ok && entry.EffectiveType() == backendName {
			if bc := decodeBackendConfig(cfg, label); bc != nil {
				return bc
			}
		}
		clidiag.Warn("ctxloom", "label %q does not resolve to backend %s; falling back to type lookup", label, backendName)
	}
	return decodeBackendConfigForType(cfg, backendName)
}

func init() {
	llmCmd.AddCommand(llmServeCmd)
	llmServeCmd.Flags().StringVar(&llmServeLabel, "label", "", "Config label to apply (internal; passed by the self-invoking run path)")
}
