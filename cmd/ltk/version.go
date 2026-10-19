package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	// cobrafmt.VersionInfo. json is {name,version} — the shape ctxloom's boot
	// probe parses — plus lock_protocol, since ltk takes the home lock files
	// (`ltk manage`, sessions.WithFileLock).
	return cobrafmt.EmitVersion(cmd, cobrafmt.VersionInfo{Name: progName, Version: Version, LockProtocol: safefs.LockProtocol})
}
