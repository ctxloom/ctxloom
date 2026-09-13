// Command taskloom is the per-project task store extracted from ctxloom: a CLI
// over the append-only task log plus an MCP server (`taskloom mcp`) exposing the
// same operations to agents.
package main

import (
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
)

// reportExecuteError writes a terminal error in the format the invocation
// selected, so a caller reading a --format json/yaml/toml stream gets a
// parseable envelope for the failure instead of a human line that ends its
// parse. cobra's own error print is silenced on the root (see root.go) so this
// is the only tail; text keeps cobra's exact "Error: <msg>" wording.
//
// It takes the writer rather than reaching for os.Stderr so the wiring itself
// is testable, and discards EmitError's result because the only way it fails is
// a failing w — and w is the sole channel that failure could be reported on.
func reportExecuteError(w io.Writer, err error) {
	_ = cliemit.EmitError(w, rootCmd, err)
}

func main() {
	// A no-op unless built with `-tags docsgen` (`just gen-docs`), which mounts
	// the shared reference-doc generator on the tree. See docs_gen.go.
	registerDocsCmd(rootCmd)
	// newLoadoutCmd is a factory (not wired via this file's own init()
	// convention) so registration has no hidden ordering dependency.
	rootCmd.AddCommand(newLoadoutCmd())

	if err := rootCmd.Execute(); err != nil {
		reportExecuteError(os.Stderr, err)
		os.Exit(1)
	}
}
