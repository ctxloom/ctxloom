package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return emit(cmd,
			cliversion.Info{Name: "ctxloom", Version: Version},
			func() error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
				return err
			},
		)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
