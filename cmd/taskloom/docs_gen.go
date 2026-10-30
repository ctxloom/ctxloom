//go:build docsgen

package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/docsgen"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
)

// registerDocsCmd mounts the shared documentation generator (internal/shared/docsgen,
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
	markMCPRemoved(root)
	root.AddCommand(docsgen.NewCommand(&docsgen.Product{
		Bin:       progName,
		Root:      root,
		CLISource: "cmd/taskloom",
		LinkBase:  "/taskloom/reference/cli/",
		ManTitle:  "TASKLOOM",
		ManManual: "User Commands",
		// taskloom's own config surface (internal/taskloom/config): the
		// hand-authored schema is the source of truth GenConfig walks (see
		// internal/shared/docsgen/config.go's doc), same as ctxloom's.
		ConfigSchema: taskloomconfig.SchemaPath,

		MCPServer:  newMCPServer(),
		MCPSource:  "cmd/taskloom/mcp_tools.go",
		MCPCommand: "taskloom mcp",
		MCPIntro:   mcpRemovedNotice + "\n\n" + mcpServerInstructions,
	}))
}

// mcpRemoved says, on every generated page about taskloom's MCP surface, that
// the Rust taskloom replacing this one at cutover serves no MCP: agents drive
// its CLI instead. It is stamped at documentation time only, so the shipped Go
// binary's own --help is unchanged. The CLI pages (and their man pages) take
// the plain sentence; the MCP page, markdown alone, takes it as a callout.
const (
	mcpRemoved = "Removed in the Rust taskloom (github.com/ctxloom/taskloom), which serves no MCP and has no `taskloom manage`: agents run its CLI (`list`, `add`, `status`, `edit`, `tag`, `show`, with `--json`), and `taskloom <command> --help` is the reference. This documents the Go taskloom, which retires at cutover."

	mcpRemovedNotice = ":::caution[Removed in the Rust taskloom]\n" + mcpRemoved + " See the [CLI reference](/taskloom/reference/cli/).\n:::"
)

// markMCPRemoved prefixes mcpRemoved to the long description of `taskloom mcp`
// and of `taskloom manage` and its subcommands, the commands that serve or
// register the MCP surface, so their generated CLI pages carry it.
func markMCPRemoved(root *cobra.Command) {
	var mark func(*cobra.Command)
	mark = func(c *cobra.Command) {
		long := c.Long
		if long == "" {
			long = c.Short
		}
		c.Long = mcpRemoved + "\n\n" + strings.TrimSpace(long)
		for _, sub := range c.Commands() {
			mark(sub)
		}
	}
	for _, c := range root.Commands() {
		if c.Name() == "mcp" || c.Name() == "manage" {
			mark(c)
		}
	}
}
