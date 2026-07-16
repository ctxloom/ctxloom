package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// llmEntry is one row of `llm list --format json`: an LLM config label (the
// value `-l`/`--llm` and `agent set --engine` accept) and whether it is the
// configured default (the primary label).
type llmEntry struct {
	Label   string `json:"label"`
	Default bool   `json:"default"`
}

// llmListEntries pairs each available LLM name with its default marker. An
// empty defaultLabel (config unavailable) marks nothing as default.
func llmListEntries(names []string, defaultLabel string) []llmEntry {
	entries := make([]llmEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, llmEntry{Label: name, Default: name != "" && name == defaultLabel})
	}
	return entries
}

var llmListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available LLMs",
	Long:    `Lists the available LLM backends.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		names, defaultLabel := availableLLMsWithDefault()
		entries := llmListEntries(names, defaultLabel)
		return emit(cmd, entries, func() error {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Available LLMs:")
			for _, e := range entries {
				if e.Default {
					fmt.Fprintf(out, "  %s (default)\n", e.Label)
				} else {
					fmt.Fprintf(out, "  %s\n", e.Label)
				}
			}
			return nil
		})
	},
}

// availableLLMsWithDefault enumerates the LLM labels and the configured
// default. With no usable config it degrades (CLAUDE.md fault tolerance) to
// the built-in backends and no default, since custom labels and the primary
// come from config.
func availableLLMsWithDefault() ([]string, string) {
	cfg, err := GetConfig()
	if err != nil {
		names := backends.List()
		sort.Strings(names)
		return names, ""
	}
	// Reuse the same name set and default identity `llm default` reports:
	// built-ins unioned with configured labels, and the primary *label*
	// (not the backend type) marked as default, so the two commands agree.
	return operations.AvailableLLMNames(cfg), cfg.PrimaryLabel()
}

func init() {
	llmCmd.AddCommand(llmListCmd)
}
