package cli

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// NewDocMCPServer builds an MCP server with the full tool + resource surface
// registered but no live config, for documentation generation (scripts/gendocs).
//
// It mirrors GetRootCmd(): the cobra tree is the single source of truth for the
// CLI reference, and this server is the single source of truth for the MCP
// reference. Since B1.6 the documented surface is the RUNNER-terminated one —
// the mainline every harness sees through its stdio shim: the proto-canonical
// generated coordination tools (agent_run/send/recv/stop, roster,
// agent_report), the cell-local content tools, and the host-relay session
// tools. Registration only reads static tool literals and the embedded
// generated schemas — no handler is ever invoked and nothing is dialed, so a
// nil config and a dead coordinator endpoint are safe. gendocs enumerates the
// registered tools/resources via an in-memory MCP client (the SDK exposes no
// direct ListTools accessor on the server).
func NewDocMCPServer() *mcp.Server {
	home, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp", // never dialed successfully; docgen only reads registrations
		Token:   "docgen",
		Harness: "docgen",
		Version: Version,
	})
	if err != nil {
		panic("docgen: dead-endpoint home: " + err.Error())
	}
	server, err := newRunnerMCPServer(nil, "", home, false, "") // full surface documented, never the leaf-gated subset
	if err != nil {
		panic("docgen: assemble runner MCP surface: " + err.Error())
	}
	return server
}
