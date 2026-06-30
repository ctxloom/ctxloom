package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

var llmListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available LLMs",
	Long:    `Lists the available LLM backends.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			// Degrade (CLAUDE.md fault tolerance): with no usable config we can
			// only enumerate built-in backends and can't know the configured
			// default or any custom labels.
			builtinNames := backends.List()
			sort.Strings(builtinNames)
			fmt.Println("Available LLMs:")
			for _, name := range builtinNames {
				fmt.Printf("  %s\n", name)
			}
			return nil
		}

		// Reuse the same name set and default identity `llm default` reports:
		// built-ins unioned with configured labels, and the primary *label*
		// (not the backend type) marked as default, so the two commands agree.
		names := availableLLMNames(cfg)
		defaultLabel := cfg.PrimaryLabel()
		fmt.Println("Available LLMs:")
		for _, name := range names {
			if name == defaultLabel {
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
