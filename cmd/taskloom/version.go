package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// version is stamped at build time from versionator via
// -ldflags "-X main.version=<v>" (see justfile); "dev" for a bare go build.
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the taskloom version",
	RunE:  runVersion,
}

func runVersion(cmd *cobra.Command, _ []string) error {
	// Routed through cobrafmt.EmitVersion like every other command (and
	// cmd/ctxloom's own version): text prints the bare version line;
	// json/yaml/toml/markdown serialize cobrafmt.VersionInfo. json stays
	// {name,version} — the shape ctxloom's boot probe parses from
	// `taskloom version --format json`.
	return cobrafmt.EmitVersion(cmd, progName, version)
}

func init() {
	rootCmd.Version = version
	rootCmd.AddCommand(versionCmd)
}
