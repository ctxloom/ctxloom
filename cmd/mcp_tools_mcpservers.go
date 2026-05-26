package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
