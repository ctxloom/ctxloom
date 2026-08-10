package cli

import (
	"github.com/spf13/cobra"
)

// Bare `ctxloom llm` lists the configured LLM backends: the collection is
// the one thing the noun is about, and reading it touches nothing.
var llmCmd = groupNodeDefault(&cobra.Command{
	Use:   "llm",
	Short: "Manage LLM backends",
	Long: `Manage LLM backends: list available LLMs, create/edit/remove a labeled
engine config, and set the default.

  ctxloom llm                    List configured LLMs (with the default marked)
  ctxloom llm create <label>     Create a new labeled engine config
  ctxloom llm edit <label>       Change an existing one
  ctxloom llm remove <label>     Remove one (report-only by default; --yes applies)
  ctxloom llm default [label]    Show or set the default`,
}, "list")

func init() {
	rootCmd.AddCommand(llmCmd)
}
