package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPromptTools is a no-op stub after Phase 4 Lever A.
//
//   list_prompts → ctxloom://prompts
//   get_prompt   → ctxloom://prompts/{name}
func (s *ctxServer) registerPromptTools(server *mcp.Server) {
	_ = server
}
