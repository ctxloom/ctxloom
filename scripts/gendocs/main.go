// Command gendocs generates ctxloom's reference docs from their sources of
// truth: the CLI reference (man pages via --man, Starlight markdown via
// --markdown) from the cobra command tree, the MCP reference (--mcp) from the
// live tool/resource registrations, and the configuration reference (--config)
// from the tracked JSON Schema.
//
// The generator itself is internal/docsgen, shared with taskloom and ltk (which
// mount it as a hidden `gendocs` subcommand under `-tags docsgen`, their cobra
// trees living in `package main`). This entrypoint only describes ctxloom.
package main

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/docsgen"
)

func main() {
	if err := docsgen.NewCommand(ctxloomProduct()).Execute(); err != nil {
		os.Exit(1)
	}
}

// ctxloomProduct describes ctxloom to the generator: its cobra tree, its
// documentation-time MCP server, and where each lives (cited in the generated
// banners so a reader knows what to edit).
func ctxloomProduct() *docsgen.Product {
	return &docsgen.Product{
		Bin:       "ctxloom",
		Root:      cli.GetRootCmd(),
		CLISource: "internal/cli",
		LinkBase:  "/reference/cli/",
		ManTitle:  "CTXLOOM",
		ManManual: "User Commands",
		// `bundle` is hidden from --help as advanced, but still documented.
		Unhide:       []string{"bundle"},
		ConfigSchema: "resources/schema/input/config-schema.json",

		MCPServer:  cli.NewDocMCPServer(),
		MCPSource:  "internal/cli",
		MCPCommand: "ctxloom mcp serve",
		MCPIntro:   mcpIntro,
	}
}

// mcpIntro is the ctxloom MCP page's opening prose: what the surface is for and,
// just as importantly, what it deliberately is not (management is CLI-only;
// tasks live in taskloom).
const mcpIntro = "Reference for the tools and resources exposed by ctxloom's MCP server (`ctxloom mcp serve`).\n" +
	"\n" +
	"The MCP surface is for **retrieving context during a session**: assembling context, " +
	"searching content, and working with session memory. Everything that *manages* ctxloom " +
	"(creating or editing bundles, profiles, fragments, and skills; pulling remotes; reviewing, " +
	"approving, and trusting changes) is done with the ctxloom CLI, not MCP tools. Task tracking " +
	"lives in the separate `taskloom` binary; its MCP server (`taskloom mcp`) serves the `task_*` tools."
