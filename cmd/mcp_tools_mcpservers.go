package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listMCPServersInput struct {
	Query     string `json:"query,omitempty" jsonschema:"Text search on name or command"`
	SortBy    string `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, command; default: name)"`
	SortOrder string `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
}

type addMCPServerInput struct {
	Name    string   `json:"name" jsonschema:"Server name (unique identifier)"`
	Command string   `json:"command" jsonschema:"Command to run the MCP server"`
	Args    []string `json:"args,omitempty" jsonschema:"Command arguments"`
	Backend string   `json:"backend,omitempty" jsonschema:"Backend to add server to (one of: unified, claude-code, gemini; default: unified)"`
}

type removeMCPServerInput struct {
	Name    string `json:"name" jsonschema:"Server name to remove"`
	Backend string `json:"backend,omitempty" jsonschema:"Backend to remove server from (one of: unified, claude-code, gemini; default: all)"`
}

type setMCPAutoRegisterInput struct {
	Enabled bool `json:"enabled" jsonschema:"Whether to auto-register ctxloom's MCP server"`
}

// MCP-server management tools mutate the in-memory cfg via the Config
// field on each operation's result. The legacy implementation stamps
// s.cfg = result.Config after every successful mutation so subsequent
// tool calls see the new state. We preserve that pattern here.
// registerMCPServerTools is a no-op stub after Phase 4.
//
//   Lever A (listings → resources):
//     list_mcp_servers → ctxloom://mcp-servers
//   Lever B (writes → CLI):
//     add_mcp_server | remove_mcp_server | set_mcp_auto_register
//        → ctxloom mcp add | remove | auto-register
func (s *ctxServer) registerMCPServerTools(server *mcp.Server) {
	_ = server
}
