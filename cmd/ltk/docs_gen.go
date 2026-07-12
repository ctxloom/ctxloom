//go:build docsgen

package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/docsgen"
)

// registerDocsCmd mounts the shared documentation generator (internal/docsgen,
// the same one ctxloom and taskloom use) as a hidden `ltk gendocs` subcommand.
//
// ltk's cobra tree lives in `package main` and so cannot be imported by a
// standalone generator; instead the generator comes to the tree, compiled in
// only under `-tags docsgen` (`just gen-docs`). The shipped binary carries the
// no-op in docs_off.go. The command is Hidden, so it never documents itself.
func registerDocsCmd(root *cobra.Command) {
	root.AddCommand(docsgen.NewCommand(&docsgen.Product{
		Bin:       progName,
		Root:      root,
		CLISource: "cmd/ltk",
		LinkBase:  "/ltk/reference/cli/",
		ManTitle:  "LTK",
		ManManual: "User Commands",
		// ltk has no MCP surface and no config schema: it is a hook binary
		// configured by .ltk.yaml rule files (see docs/RULES.md).
	}))
}
