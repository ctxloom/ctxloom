package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

type listPromptsInput struct {
	Query     string `json:"query,omitempty" jsonschema:"Text search on name"`
	SortBy    string `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name; default: name)"`
	SortOrder string `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
}

type getPromptInput struct {
	Name string `json:"name" jsonschema:"Prompt name (without extension)"`
}

func (s *ctxServer) registerPromptTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_prompts",
			Description: "List saved prompts",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listPromptsInput) (*mcp.CallToolResult, *operations.ListPromptsResult, error) {
			result, err := operations.ListPrompts(ctx, s.cfg, operations.ListPromptsRequest{
				Query:     in.Query,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_prompt",
			Description: "Get a saved prompt's content by name",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getPromptInput) (*mcp.CallToolResult, *operations.GetPromptResult, error) {
			result, err := operations.GetPrompt(ctx, s.cfg, operations.GetPromptRequest{
				Name: in.Name,
			})
			return nil, result, err
		})
}
