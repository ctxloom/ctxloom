package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// NewStandaloneMCPServer builds the STANDALONE `ctxloom mcp serve` tool surface
// — the legacy stdio one a harness gets when it registers `ctxloom mcp serve`
// itself rather than reaching the runner through the stdio shim. It is the
// same registration `ctxloom mcp serve` performs (registerTools) over a config
// -less ctxServer, so nothing dials and no handler runs.
//
// It exists so the difference between this surface and the DOCUMENTED one
// (runnermcp.NewDocServer) is measurable. The MCP
// reference page's intro states that difference as fact — which tools the
// standalone surface lacks, and which of the shared ones take different
// parameters — and nothing could check it, so the prose could only ever be
// verified by hand and could go stale without a single test noticing.
func NewStandaloneMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: version.Version}, nil)
	(&ctxServer{}).registerTools(server)
	return server
}

// ListStandaloneMCPToolContracts returns the full advertised contract of every
// tool on the standalone `ctxloom mcp serve` surface, via the same in-memory
// client round trip runnermcp.ListDocToolContracts uses against the
// documented one.
func ListStandaloneMCPToolContracts(ctx context.Context) ([]runnermcp.ToolContract, error) {
	return runnermcp.ToolContracts(ctx, NewStandaloneMCPServer())
}
