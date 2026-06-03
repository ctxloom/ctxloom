package cmd

import (
	"fmt"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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
		if cfg, _ := config.Load(); cfg != nil {
			if bc := decodeBackendConfigForType(cfg, backendName); bc != nil {
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

func init() {
	llmCmd.AddCommand(llmServeCmd)
}
