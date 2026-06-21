package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// cmdVersionInfo is the machine-readable shape of `ctxloom version --format
// json` — the same {name, version} contract the companion binaries
// (taskloom, ltk) emit, so anything probing tool versions can treat all
// three uniformly.
type cmdVersionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return emit(cmd,
			cmdVersionInfo{Name: "ctxloom", Version: Version},
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
