package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// assembleContextInput's `Fragments` field is named `bundles` over the wire
// to match the legacy schema. The legacy handler accepted both names from
// different generations of clients; we keep the wire-level `bundles` here.
type assembleContextInput struct {
	Profile   string   `json:"profile,omitempty" jsonschema:"Profile name to use"`
	Fragments []string `json:"bundles,omitempty" jsonschema:"Additional fragment names to include"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Include all fragments with these tags"`
}

type searchContentInput struct {
	Query     string   `json:"query" jsonschema:"Search text (matches name, description, tags)"`
	Types     []string `json:"types,omitempty" jsonschema:"Content types to search (any of: fragment, prompt, profile, mcp_server; default: all)"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Filter by tags (fragments only)"`
	SortBy    string   `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, type, relevance; default: relevance)"`
	SortOrder string   `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
	Limit     int      `json:"limit,omitempty" jsonschema:"Maximum results to return (default: 50)"`
}

func (s *ctxServer) registerContextTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "assemble_context",
			Description: "Assemble context from a profile, fragments, and/or tags. Returns the combined context that would be sent to an AI.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in assembleContextInput) (*mcp.CallToolResult, *operations.AssembleContextResult, error) {
			result, err := operations.AssembleContext(ctx, s.cfg, operations.AssembleContextRequest{
				Profile:   in.Profile,
				Fragments: in.Fragments,
				Tags:      in.Tags,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_content",
			Description: "Search across all ctxloom content types (fragments, prompts, profiles, MCP servers)",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchContentInput) (*mcp.CallToolResult, *operations.SearchContentResult, error) {
			result, err := operations.SearchContent(ctx, s.cfg, operations.SearchContentRequest{
				Query:     in.Query,
				Types:     in.Types,
				Tags:      in.Tags,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
				Limit:     in.Limit,
			})
			return nil, result, err
		})
}
