//go:build docsgen

package main

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/docsgen"
)

// registerDocsCmd mounts the shared documentation generator (internal/docsgen,
// the same one ctxloom and ltk use) as a hidden `taskloom gendocs` subcommand.
//
// taskloom's cobra tree lives in `package main` and so cannot be imported by a
// standalone generator; instead the generator comes to the tree, compiled in
// only under `-tags docsgen` (`just gen-docs`). The shipped binary carries the
// no-op in docs_off.go. The command is Hidden, so it never documents itself.
//
// Both references come from the same code the binary runs: the CLI pages from
// this cobra tree, the MCP page from newMCPServer() — the very server
// `taskloom mcp` serves, with the tool descriptions and reflected input schemas
// read straight off the registrations. The page's intro is the server's own
// instructions string, not a hand-written copy of it.
func registerDocsCmd(root *cobra.Command) {
	root.AddCommand(docsgen.NewCommand(&docsgen.Product{
		Bin:       "taskloom",
		Root:      root,
		CLISource: "cmd/taskloom",
		LinkBase:  "/taskloom/reference/cli/",
		ManTitle:  "TASKLOOM",
		ManManual: "User Commands",

		MCPServer:  newMCPServer(),
		MCPSource:  "cmd/taskloom/mcp_tools.go",
		MCPCommand: "taskloom mcp",
		MCPIntro:   mcpServerInstructions,
	}))
}
