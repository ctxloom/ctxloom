// Command taskloom is the per-project task store extracted from ctxloom: a CLI
// over the append-only task log plus an MCP server (`taskloom mcp`) exposing the
// same operations to agents.
package main

import "os"

func main() {
	// A no-op unless built with `-tags docsgen` (`just gen-docs`), which mounts
	// the shared reference-doc generator on the tree. See docs_gen.go.
	registerDocsCmd(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
