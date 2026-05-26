package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerFragmentTools is a no-op stub after Phase 4.
//
//   Lever A (listings → resources):
//     list_fragments → ctxloom://fragments
//     get_fragment   → ctxloom://fragments/{name}
//   Lever B (writes → CLI):
//     create_fragment → ctxloom fragment create <bundle> <name>
//     delete_fragment → ctxloom fragment delete <ref>
func (s *ctxServer) registerFragmentTools(server *mcp.Server) {
	_ = server
}
