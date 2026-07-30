package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
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
		cfg, err := GetConfigForUpdate()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		return runLLMDefault(cmd, config.NewManager(), cfg, args)
	},
}

// llmDefaultShowResult is emit()'s result for the show path (`llm default`
// with no argument) — a single-field wrapper so json/yaml/toml/markdown
// carry the same "default" key `llm list`'s entries use, rather than a bare
// unlabeled string.
type llmDefaultShowResult struct {
	Default string `json:"default"`
}

// runLLMDefault is the testable body of `ctxloom llm default`: with no args
// it reports the current default; with one, it validates and sets it. Both
// paths route through emit() so --format json/yaml/toml/markdown works
// without duplicating this branch. cfg serves the read-only show path and the
// known-LLM validation; mgr is the write half SetDefaultLLM performs its
// locked transaction through.
func runLLMDefault(cmd *cobra.Command, mgr *config.Manager, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		result := llmDefaultShowResult{Default: cfg.PrimaryLabel()}
		return emit(cmd, result, func() error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), result.Default)
			return err
		})
	}

	name := args[0]
	if !isKnownLLM(cfg, name) {
		available := operations.AvailableLLMNames(cfg)
		return fmt.Errorf("unknown LLM %q; available: %s", name, strings.Join(available, ", "))
	}

	res, err := operations.SetDefaultLLM(cmd.Context(), mgr, operations.SetDefaultLLMRequest{Name: name})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		if res.Status == "unchanged" {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Default LLM is already %s\n", name)
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Default LLM set to: %s\n", name)
		return err
	})
}

// isKnownLLM reports whether name is one of the LLMs this config exposes. It is
// a membership test over operations.AvailableLLMNames — the ONE definition of
// "known LLM" — so the set `llm default` accepts is by construction the set its
// rejection message offers. Re-deriving membership here (built-in registry OR a
// config entry) restated that set in a second place with no compiler help, and
// a command that rejects a name its own error lists as available is the drift
// that costs.
func isKnownLLM(cfg *config.Config, name string) bool {
	return slices.Contains(operations.AvailableLLMNames(cfg), name)
}

func init() {
	llmCmd.AddCommand(llmDefaultCmd)
}
