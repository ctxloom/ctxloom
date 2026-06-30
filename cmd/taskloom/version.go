package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// version is stamped at build time from versionator via
// -ldflags "-X main.version=<v>" (see justfile); "dev" for a bare go build.
var version = "dev"

var versionFormat string

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the taskloom version",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cliversion.Render(cmd.OutOrStdout(), cliversion.Info{Name: "taskloom", Version: version}, versionFormat)
	},
}

func init() {
	rootCmd.Version = version
	versionCmd.Flags().StringVar(&versionFormat, "format", "text", "Output format: text or json ({name, version})")
	rootCmd.AddCommand(versionCmd)
}
