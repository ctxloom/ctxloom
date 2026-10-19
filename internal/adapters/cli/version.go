package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	info := cobrafmt.VersionInfo{Name: "ctxloom", Version: version.Version, LockProtocol: safefs.LockProtocol}
	return emit(cmd, info, func() error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		return err
	})
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
