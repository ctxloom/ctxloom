package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// Input types for fragment tools. Fields without "omitempty" are required;
// fields with "omitempty" are optional. The SDK's schema generator reads
// the json tag for the property name and the jsonschema tag for the
// description that surfaces in tools/list to the client.

type listFragmentsInput struct {
	Query     string   `json:"query,omitempty" jsonschema:"Text search on name"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Filter by tags"`
	SortBy    string   `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, source; default: name)"`
	SortOrder string   `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
}

type getFragmentInput struct {
	Name string `json:"name" jsonschema:"Fragment name (without extension)"`
}

type createFragmentInput struct {
	Name    string   `json:"name" jsonschema:"Fragment name (without extension)"`
	Content string   `json:"content" jsonschema:"Fragment content (markdown)"`
	Tags    []string `json:"tags,omitempty" jsonschema:"Tags for the fragment"`
	Version string   `json:"version,omitempty" jsonschema:"Version string (default: 1.0)"`
}

type deleteFragmentInput struct {
	Name string `json:"name" jsonschema:"Fragment name to delete"`
}

// deleteFragmentResult mirrors the legacy {"status": "deleted"} response
// shape so existing clients keep working through the SDK migration. The
// operations.DeleteFragmentResult type isn't surfaced because the legacy
// handler intentionally returned only the status string.
type deleteFragmentResult struct {
	Status string `json:"status"`
}

func (s *ctxServer) registerFragmentTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_fragments",
			Description: "List available local context fragments with their tags and source locations",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listFragmentsInput) (*mcp.CallToolResult, *operations.ListFragmentsResult, error) {
			result, err := operations.ListFragments(ctx, s.cfg, operations.ListFragmentsRequest{
				Query:     in.Query,
				Tags:      in.Tags,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_fragment",
			Description: "Get a local fragment's content by name",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getFragmentInput) (*mcp.CallToolResult, *operations.GetFragmentResult, error) {
			result, err := operations.GetFragment(ctx, s.cfg, operations.GetFragmentRequest{
				Name: in.Name,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_fragment",
			Description: "Create a new context fragment",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in createFragmentInput) (*mcp.CallToolResult, *operations.CreateFragmentResult, error) {
			result, err := operations.CreateFragment(ctx, s.cfg, operations.CreateFragmentRequest{
				Name:    in.Name,
				Content: in.Content,
				Tags:    in.Tags,
				Version: in.Version,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_fragment",
			Description: "Delete a local context fragment",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deleteFragmentInput) (*mcp.CallToolResult, *deleteFragmentResult, error) {
			_, err := operations.DeleteFragment(ctx, s.cfg, operations.DeleteFragmentRequest{
				Name: in.Name,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, &deleteFragmentResult{Status: "deleted"}, nil
		})
}
