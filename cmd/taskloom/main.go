// Command taskloom is the per-project task store extracted from ctxloom: a CLI
// over the append-only task log plus an MCP server (`taskloom mcp`) exposing the
// same operations to agents.
package main

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/logboot"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

func main() {
	// Before anything can log: without it zap.L() is the no-op global and a
	// stalled lock wait on the task log leaves nothing on disk. Not verbose:
	// taskloom is an MCP server and a hook-driven CLI, and a stderr tee is
	// ctxloom's operator switch, not this binary's.
	flush := logboot.Install("taskloom", false)
	// taskloom writes ownership records beneath ctxloom's private home roots,
	// so it establishes them as every ctxloom process does.
	if err := paths.EnsureHomeRoots(safefs.New().Private); err != nil {
		fmt.Fprintf(os.Stderr, "taskloom: %v\n", err)
	}

	// A no-op unless built with `-tags docsgen` (`just gen-docs`), which mounts
	// the shared reference-doc generator on the tree. See docs_gen.go.
	registerDocsCmd(rootCmd)
	// newLoadoutCmd is a factory (not wired via this file's own init()
	// convention) so registration has no hidden ordering dependency.
	rootCmd.AddCommand(newLoadoutCmd())

	// Execute reports a failure in the family's form ("taskloom: <msg>", or
	// an envelope under an explicit structured --format) and returns the
	// status; it never exits, so the flush below still runs.
	code := cobrafmt.Execute(rootCmd, progName, os.Stderr)
	// Flushed as a plain statement before the exit; see logboot.Install.
	flush()
	os.Exit(code)
}
