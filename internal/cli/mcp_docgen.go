package cli

import (
	"context"
	"fmt"
	"sort"

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


// ListDocMCPToolNames returns the sorted tool names registered on the
// documented (runner-terminated) MCP surface built by NewDocMCPServer, via an
// in-memory client round trip -- the SDK exposes no direct accessor on the
// server itself (see internal/docsgen/mcp.go's enumerateMCPSurface, which
// does the identical round trip to render the published reference page).
//
// U156-F01: this exists so a completeness gate can measure the SAME surface
// this package documents. Before it, tests/acceptance/completeness_test.go
// only ever enumerated the STANDALONE `ctxloom mcp serve` surface (a
// deliberately reduced agent-delegation surface with different schemas --
// see mcpIntro's own caution block), so the surface a real harness actually
// talks to through `ctxloom run`/`ctxloom acp` had zero completeness
// coverage: roster, agent_report and agent_fetch_artifact are named in this
// package's own doc as existing only here, and were never checked by
// anything.
func ListDocMCPToolNames(ctx context.Context) ([]string, error) {
	server := NewDocMCPServer()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return nil, fmt.Errorf("connect doc server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "completeness-check"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return nil, fmt.Errorf("connect doc client: %w", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names, nil
}
