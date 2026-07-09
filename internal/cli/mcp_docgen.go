package cli

import "github.com/modelcontextprotocol/go-sdk/mcp"

// NewDocMCPServer builds an MCP server with the full tool + resource surface
// registered but no live config, for documentation generation (scripts/gendocs).
//
// It mirrors GetRootCmd(): the cobra tree is the single source of truth for the
// CLI reference, and this server is the single source of truth for the MCP
// reference. Registration only reads the static Tool/Resource literals (names,
// descriptions, jsonschema-derived input schemas) — the handlers are never
// invoked here, so a nil cfg on the ctxServer is safe. gendocs enumerates the
// registered tools/resources via an in-memory MCP client (the SDK exposes no
// direct ListTools accessor on the server).
func NewDocMCPServer() *mcp.Server {
	s := &ctxServer{}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: Version,
	}, nil)
	s.registerTools(server)
	s.registerResources(server)
	return server
}
