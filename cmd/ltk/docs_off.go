//go:build !docsgen

package main

import "github.com/spf13/cobra"

// registerDocsCmd is a no-op in the shipped binary: the documentation generator
// (and the cobra/doc + MCP machinery it drags in) is compiled in only under
// `-tags docsgen`, which `just gen-docs` passes. See docs_gen.go.
func registerDocsCmd(*cobra.Command) {}
