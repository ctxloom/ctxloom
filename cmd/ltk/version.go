package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

func newVersionCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the ltk version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cliversion.Render(cmd.OutOrStdout(), cliversion.Info{Name: progName, Version: Version}, format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json ({name, version})")
	return cmd
}
