package main

import (
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/claude/relay"
)

// claudeRelayCommand is claude's session relay as a hidden subcommand: claude
// spawns it as its ctxloom MCP server (the stdio entry claude's definition
// renders), and nothing else runs it.
func claudeRelayCommand() *cobra.Command {
	return &cobra.Command{
		Use:          claude.RelayCommand,
		Short:        "Claude's session relay: the stdio MCP server claude spawns for ctxloom (machine callback)",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return relay.Run(cmd.Context(), relay.Config{Env: os.LookupEnv, Stderr: cmd.ErrOrStderr()}, &mcp.StdioTransport{})
		},
	}
}
