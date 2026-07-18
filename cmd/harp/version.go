package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/clifmt"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// newVersionCmd prints harp's version. json/yaml/toml/markdown render
// cliversion.Info reflectively via clifmt, same as every other harp output;
// text is the one escape hatch, printing the bare version string (the
// universal CLI convention, and what `ctxloom version`'s own text path
// does) rather than clifmt's default "Name: harp\nVersion: dev" struct dump.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the harp version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := resolveFormat(cmd)
			if err != nil {
				return err
			}
			if format == clifmt.FormatText {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
				return err
			}
			return clifmt.Render(cmd.OutOrStdout(), cliversion.Info{Name: progName, Version: Version}, format)
		},
	}
}
