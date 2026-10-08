package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ltk version",
		RunE:  runLtkVersion,
	}
}

func runLtkVersion(cmd *cobra.Command, _ []string) error {
	// Routed through cobrafmt.EmitVersion like cmd/ctxloom and cmd/taskloom:
	// text prints the bare version line; json/yaml/toml/markdown serialize
	// cobrafmt.VersionInfo. json stays {name,version} — the shape ctxloom's boot
	// probe parses.
	return cobrafmt.EmitVersion(cmd, progName, Version)
}
