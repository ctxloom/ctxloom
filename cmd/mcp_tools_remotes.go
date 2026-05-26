package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerRemoteTools is a no-op on the SDK server after Phase 4.
//
// Lever A (listings → resources): list_remotes and browse_remote are
// now served via ctxloom://remotes and ctxloom://remotes/{name}/contents.
//
// Lever B (writes → CLI): add_remote, remove_remote, update_remote,
// discover_remotes, search_remotes, and pull_remote are no longer
// callable as MCP tools. They remain available via the CLI:
//
//	ctxloom remote add | rm | update | search | discover
//	ctxloom remote pull <ref>
func (s *ctxServer) registerRemoteTools(server *mcp.Server) {
	_ = server
}
