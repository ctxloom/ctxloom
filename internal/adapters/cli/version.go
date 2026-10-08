package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/version"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

var versionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Print the version number",
	Example: `  ctxloom version`,
	RunE:    runVersion,
}

func runVersion(cmd *cobra.Command, _ []string) error {
	return emit(cmd, cobrafmt.VersionInfo{Name: "ctxloom", Version: version.Version}, func() error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		return err
	})
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
