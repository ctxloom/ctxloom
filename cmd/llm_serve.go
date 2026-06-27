package cmd

import (
	"fmt"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/clidiag"
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
