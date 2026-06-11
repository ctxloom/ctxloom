package cmd

import (
	"encoding/json"
	"fmt"
	"io"

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

var versionFormat string

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return printVersion(cmd.OutOrStdout(), versionFormat)
	},
}

func printVersion(w io.Writer, format string) error {
	switch format {
	case "text":
		_, err := fmt.Fprintln(w, Version)
		return err
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(cmdVersionInfo{Name: "ctxloom", Version: Version})
	default:
		return fmt.Errorf("unknown format %q (supported: text, json)", format)
	}
}

func init() {
	versionCmd.Flags().StringVar(&versionFormat, "format", "text", "Output format: text or json ({name, version})")
	rootCmd.AddCommand(versionCmd)
}
