package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ltk version",
		RunE:  runLtkVersion,
	}
}

func runLtkVersion(cmd *cobra.Command, _ []string) error {
	// Routed through cliemit.EmitVersion like cmd/ctxloom and cmd/taskloom:
	// text prints the bare version line; json/yaml/toml/markdown serialize
	// cliversion.Info. json stays {name,version} — the shape ctxloom's boot
	// probe parses.
	return cliemit.EmitVersion(cmd, cliemit.Emit, progName, Version)
}
