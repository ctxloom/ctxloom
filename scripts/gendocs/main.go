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
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/docsgen"
)

func main() {
	product, closeMCP, err := ctxloomProduct()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gendocs: %v\n", err)
		os.Exit(1)
	}
	defer closeMCP()
	if err := docsgen.NewCommand(product).Execute(); err != nil {
		os.Exit(1)
	}
}

// ctxloomProduct describes ctxloom to the generator: its cobra tree, its
// documentation-time MCP server, and where each lives (cited in the generated
// banners so a reader knows what to edit). The returned closer releases the
// MCP server's backing coord.Home, whose construction opens a gRPC client and
// two background loops.
func ctxloomProduct() (*docsgen.Product, func(), error) {
	mcpServer, closeMCP, err := cli.NewDocMCPServer()
	if err != nil {
		return nil, nil, err
	}
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

		MCPServer: mcpServer,
		MCPSource: "internal/cli",
		// The documented surface is the RUNNER-terminated one (NewDocMCPServer →
		// newRunnerMCPServer): what a harness actually sees inside `ctxloom run`
		// / `ctxloom acp`, through its stdio `ctxloom mcp` shim. Naming
		// `ctxloom mcp serve` here would be a lie — that standalone server
		// registers a REDUCED agent surface with different schemas (see mcpIntro).
		MCPCommand: "ctxloom run",
		MCPIntro:   mcpIntro,
	}, closeMCP, nil
}

// mcpIntro is the ctxloom MCP page's opening prose: which surface this is (the
// runner-terminated mainline, not standalone `mcp serve`), what it is for, and,
// just as importantly, what it deliberately is not (management is CLI-only;
// tasks live in taskloom).
const mcpIntro = "Reference for the tools and resources ctxloom exposes to the agent it launches — the " +
	"**runner-terminated** MCP surface a harness sees inside `ctxloom run` (and `ctxloom acp`), " +
	"reached through the stdio `ctxloom mcp` shim ctxloom wires into the harness's settings. " +
	"This is the surface you get in a normal ctxloom session, and it is the one generated here.\n" +
	"\n" +
	":::caution[A standalone `ctxloom mcp serve` is not this surface]\n" +
	"Registering `ctxloom mcp serve` yourself, as a plain MCP server in some other harness's " +
	"config, gets you the retrieval and session-memory tools below **unchanged** — but a " +
	"**reduced agent-delegation surface with different schemas**: `agent_run`, `agent_send`, " +
	"`agent_recv`, and `agent_stop` only (no `roster`, no `agent_report`, no " +
	"`agent_fetch_artifact`), and those four take different parameters than documented here. " +
	"Agent delegation is coordinated by the runner, so drive it from `ctxloom run` / `ctxloom acp`.\n" +
	":::\n" +
	"\n" +
	"The MCP surface is for **working inside a session**: assembling context, searching content, " +
	"session memory, and delegating to child agents. Everything that *manages* ctxloom " +
	"(creating or editing bundles, profiles, fragments, and commands; pulling remotes; reviewing " +
	"and approving content; trusting a publisher's signing key) is done with the ctxloom CLI, " +
	"not MCP tools. Task tracking lives in the separate `taskloom` binary; its MCP server " +
	"(`taskloom mcp`) serves the `task_*` tools."
