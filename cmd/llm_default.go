package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var llmDefaultCmd = &cobra.Command{
	Use:   "default [name]",
	Short: "Show or set the default LLM",
	Long: `Show or set the default AI backend LLM.

Without arguments, prints the current default LLM name.
With an LLM name argument, sets that LLM as the default.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeLLMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(args) == 0 {
			fmt.Println(cfg.PrimaryLabel())
			return nil
		}

		name := args[0]

		if !isKnownLLM(cfg, name) {
			available := availableLLMNames(cfg)
			return fmt.Errorf("unknown LLM %q; available: %s", name, strings.Join(available, ", "))
		}

		res, err := operations.SetDefaultLLM(cmd.Context(), cfg, operations.SetDefaultLLMRequest{Name: name})
		if err != nil {
			return err
		}
		if res.Status == "unchanged" {
			fmt.Printf("Default LLM is already %s\n", name)
			return nil
		}
		fmt.Printf("Default LLM set to: %s\n", name)
		return nil
	},
}

// isKnownLLM checks if an LLM is a registered built-in or has a config entry.
func isKnownLLM(cfg *config.Config, name string) bool {
	if backends.Exists(name) {
		return true
	}
	_, ok := cfg.LM.Configs[name]
	return ok
}

// availableLLMNames returns a sorted list of all known LLM names:
// registered built-ins plus any with an explicit config entry.
func availableLLMNames(cfg *config.Config) []string {
	seen := map[string]bool{}
	var names []string
	for _, n := range backends.List() {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range cfg.LM.Configs {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func init() {
	llmCmd.AddCommand(llmDefaultCmd)
}
