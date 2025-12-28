package cmd

import (
	"github.com/spf13/cobra"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage AI backend plugins",
	Long:  `Manage AI backend plugins - list, serve, and extract plugin binaries.`,
}

func init() {
	rootCmd.AddCommand(pluginCmd)
}
