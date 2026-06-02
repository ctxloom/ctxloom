package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

var llmListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available LLMs",
	Long:    `Lists the available LLM backends.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		builtinNames := backends.List()
		sort.Strings(builtinNames)

		defaultLLM := config.DefaultLLM
		if cfg, err := config.Load(); err == nil {
			defaultLLM = cfg.GetDefaultLLM()
		}

		fmt.Println("Built-in LLMs:")
		for _, name := range builtinNames {
			if name == defaultLLM {
				fmt.Printf("  %s (default)\n", name)
			} else {
				fmt.Printf("  %s\n", name)
			}
		}
		return nil
	},
}

func init() {
	llmCmd.AddCommand(llmListCmd)
}
