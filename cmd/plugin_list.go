package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

var pluginListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available plugins",
	Long:    `Lists the available AI backend plugins.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		builtinNames := backends.List()
		sort.Strings(builtinNames)

		defaultPlugin := config.DefaultLLM
		if cfg, err := config.Load(); err == nil {
			defaultPlugin = cfg.GetDefaultLLM()
		}

		fmt.Println("Built-in plugins:")
		for _, name := range builtinNames {
			if name == defaultPlugin {
				fmt.Printf("  %s (default)\n", name)
			} else {
				fmt.Printf("  %s\n", name)
			}
		}
		return nil
	},
}

func init() {
	pluginCmd.AddCommand(pluginListCmd)
}
