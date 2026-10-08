package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// newVersionCmd prints harp's version through cobrafmt.EmitVersion, like
// every family binary: text prints the bare version string, the structured
// formats the {name, version} payload.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the harp version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cobrafmt.EmitVersion(cmd, progName, Version)
		},
	}
}
