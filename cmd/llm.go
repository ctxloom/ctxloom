package cmd

import (
	"github.com/spf13/cobra"
)

var llmCmd = &cobra.Command{
	Use:   "llm",
	Short: "Manage LLM backends",
	Long:  `Manage LLM backends - list available LLMs and set the default.`,
}

func init() {
	rootCmd.AddCommand(llmCmd)
}
