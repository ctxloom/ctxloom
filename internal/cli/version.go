package cli

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	RunE:  runVersion,
}

func runVersion(cmd *cobra.Command, _ []string) error {
	return cliemit.EmitVersion(cmd, emit, "ctxloom", version.Version)
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
