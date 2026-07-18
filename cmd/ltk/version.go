package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ltk version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Routed through emit like cmd/ctxloom: text prints the bare version
			// line; json/yaml/toml/markdown serialize cliversion.Info. json stays
			// {name,version} — the shape ctxloom's boot probe parses.
			return cliemit.Emit(cmd, cliversion.Info{Name: progName, Version: Version}, func() error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
				return err
			})
		},
	}
}
