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
		for _, k := range []string{coord.EnvCoordURL, coord.EnvCoordCred, coord.EnvRunID} {
			_ = os.Unsetenv(k)
		}

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
		var home *coord.Home
		if homeCfg.URL != "" && homeCfg.Token != "" {
			h, herr := coord.NewHome(cmd.Context(), homeCfg)
			if herr != nil {
				clidiag.Warn("ctxloom", "runner dial-home failed (coordinator will synthesize loss): %v", herr)
			} else {
				home = h
				if cfg != nil {
					if endpoint, merr := serveRunnerMCP(cfg, harp, home); merr != nil {
						clidiag.Warn("ctxloom", "runner MCP endpoint failed (the harness shim will fall back to its local mode): %v", merr)
					} else {
						defer endpoint.close()
						// Exported into THIS process env: every engine spawn
						// path builds the harness env over os.Environ.
						_ = os.Setenv(coord.EnvMCPSocket, endpoint.socketPath)
					}
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
		if entry, ok := cfg.LM.Configs[label]; ok && entry.EffectiveType() == backendName {
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
