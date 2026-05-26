package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
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
func (s *ctxServer) registerMCPServerTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_mcp_servers",
			Description: "List configured MCP servers",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listMCPServersInput) (*mcp.CallToolResult, *operations.ListMCPServersResult, error) {
			result, err := operations.ListMCPServers(ctx, s.cfg, operations.ListMCPServersRequest{
				Query:     in.Query,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "add_mcp_server",
			Description: "Add an MCP server to the configuration",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in addMCPServerInput) (*mcp.CallToolResult, *operations.AddMCPServerResult, error) {
			result, err := operations.AddMCPServer(ctx, s.cfg, operations.AddMCPServerRequest{
				Name:    in.Name,
				Command: in.Command,
				Args:    in.Args,
				Backend: in.Backend,
			})
			if err != nil {
				return nil, nil, err
			}
			if result.Config != nil {
				s.cfg = result.Config
			}
			return nil, result, nil
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "remove_mcp_server",
			Description: "Remove an MCP server from the configuration",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeMCPServerInput) (*mcp.CallToolResult, *operations.RemoveMCPServerResult, error) {
			result, err := operations.RemoveMCPServer(ctx, s.cfg, operations.RemoveMCPServerRequest{
				Name:    in.Name,
				Backend: in.Backend,
			})
			if err != nil {
				return nil, nil, err
			}
			if result.Config != nil {
				s.cfg = result.Config
			}
			return nil, result, nil
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "set_mcp_auto_register",
			Description: "Enable or disable auto-registration of ctxloom's own MCP server",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in setMCPAutoRegisterInput) (*mcp.CallToolResult, *operations.SetMCPAutoRegisterResult, error) {
			result, err := operations.SetMCPAutoRegister(ctx, s.cfg, operations.SetMCPAutoRegisterRequest{
				Enabled: in.Enabled,
			})
			if err != nil {
				return nil, nil, err
			}
			if result.Config != nil {
				s.cfg = result.Config
			}
			return nil, result, nil
		})
}
