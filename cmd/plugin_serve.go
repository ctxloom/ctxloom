package cmd

import (
	"fmt"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

var pluginServeCmd = &cobra.Command{
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

		// Load config to get plugin settings
		cfg, _ := config.Load()
		if cfg != nil {
			if pluginCfg, ok := cfg.LM.Plugins[backendName]; ok {
				backends.ApplyPluginConfig(backend, &pluginCfg)
			}
		}

		// Create the plugin map with our backend
		pluginMap := map[string]plugin.Plugin{
			pb.PluginName: &pb.AIPluginGRPC{Impl: backend},
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
	pluginCmd.AddCommand(pluginServeCmd)
}
