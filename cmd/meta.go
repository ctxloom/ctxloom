package cmd

import (
	"github.com/spf13/cobra"
)

var metaCmd = &cobra.Command{
	Use:   "meta",
	Short: "Output metadata for session tracking",
}

func init() {
	rootCmd.AddCommand(metaCmd)
}
