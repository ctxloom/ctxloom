package cli

import (
	"github.com/spf13/cobra"
)

// Bare `ctxloom llm` lists the configured LLM backends: the collection is
// the one thing the noun is about, and reading it touches nothing.
var llmCmd = groupNodeDefault(&cobra.Command{
	Use:   "llm",
	Short: "Manage LLM backends",
	Long:  `Manage LLM backends - list available LLMs and set the default.`,
}, "list")

func init() {
	rootCmd.AddCommand(llmCmd)
}
