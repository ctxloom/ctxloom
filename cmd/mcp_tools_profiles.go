package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerProfileTools is a no-op stub after Phase 4.
//
//   Lever A (listings → resources):
//     list_profiles → ctxloom://profiles
//     get_profile   → ctxloom://profiles/{name}
//   Lever B (writes → CLI):
//     create_profile | update_profile | delete_profile → ctxloom profile *
func (s *ctxServer) registerProfileTools(server *mcp.Server) {
	_ = server
}
