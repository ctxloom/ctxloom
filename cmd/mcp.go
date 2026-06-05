package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/shared/wire"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run ctxloom as an MCP server",
	Long: `Run ctxloom as an MCP (Model Context Protocol) server over stdio.

When called without subcommands, runs ctxloom as an MCP server over stdio.

RUNNING AS MCP SERVER:
  ctxloom mcp              Run as MCP server over stdio
  ctxloom mcp serve        Alias for running as MCP server

  Available tools when running as server:
    Context:  assemble_context, search_content, search_remotes
    Sync:     sync_dependencies
    Bundles:  create_bundle, update_bundle, delete_bundle
    Review:   acknowledge_bundle_review, decline_bundle, show_bundle_verbatim,
              trust_remote, approve_remote_pending, pin_bundle, unpin_bundle
    Sessions: compact_session, load_session, get_previous_session, recover_session
    Tasks:    task_add, task_list, task_set_status

  Read-only listings (fragments, profiles, prompts, remotes, mcp-servers,
  sessions) are exposed as MCP resources (ctxloom://...), not tools.

Manage configured MCP servers and ctxloom's own auto-registration under
'ctxloom manage mcp'.`,
	RunE: runMCPServerSDK,
}

// MCP subcommands for managing MCP server configurations

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as MCP server over stdio",
	Long:  `Run ctxloom as an MCP (Model Context Protocol) server over stdio. This is the default behavior when running 'ctxloom mcp' without subcommands.`,
	RunE:  runMCPServerSDK,
}

var mcpListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured MCP servers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.ListMCPServers(cmd.Context(), cfg, operations.ListMCPServersRequest{
			SortBy: "name",
		})
		if err != nil {
			return err
		}

		if result.Count == 0 {
			fmt.Println("No MCP servers configured.")
			fmt.Println()
			fmt.Printf("Auto-register ctxloom MCP server: %v\n", result.AutoRegister)
			fmt.Println("\nUse 'ctxloom manage mcp servers add <name> --command <cmd>' to add one.")
			return nil
		}

		fmt.Println("MCP Servers:")
		for _, srv := range result.Servers {
			fmt.Printf("  %s\n", srv.Name)
			fmt.Printf("    Command: %s\n", srv.Command)
			if len(srv.Args) > 0 {
				fmt.Printf("    Args: %s\n", strings.Join(srv.Args, " "))
			}
			fmt.Printf("    Scope: %s\n", srv.Backend)
		}

		fmt.Printf("\nAuto-register ctxloom MCP server: %v\n", result.AutoRegister)
		return nil
	},
}

var (
	mcpAddCommand string
	mcpAddArgs    []string
	mcpAddBackend string
)

var mcpAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add an MCP server configuration",
	Long: `Add an MCP server to be injected into backend settings.

Examples:
  ctxloom manage mcp servers add my-server --command "npx my-mcp-server"
  ctxloom manage mcp servers add tools --command "python" --args "-m,mcp_tools"
  ctxloom manage mcp servers add claude-only --command "./server" --backend claude-code`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if mcpAddCommand == "" {
			return fmt.Errorf("--command is required")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.AddMCPServer(cmd.Context(), cfg, operations.AddMCPServerRequest{
			Name:    name,
			Command: mcpAddCommand,
			Args:    mcpAddArgs,
			Backend: mcpAddBackend,
		})
		if err != nil {
			return err
		}

		scope := "unified (all backends)"
		if result.Backend != "" && result.Backend != "unified" {
			scope = result.Backend + " only"
		}
		fmt.Printf("Added MCP server %q (%s)\n", result.Name, scope)
		fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	},
}

var mcpRemoveBackend string

var mcpRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove an MCP server configuration",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.RemoveMCPServer(cmd.Context(), cfg, operations.RemoveMCPServerRequest{
			Name:    name,
			Backend: mcpRemoveBackend,
		})
		if err != nil {
			return err
		}

		for _, backend := range result.RemovedFrom {
			if backend != "unified" {
				fmt.Printf("Removed from backend: %s\n", backend)
			}
		}

		fmt.Printf("Removed MCP server %q\n", result.Name)
		fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	},
}

var mcpShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPShow,
}

func runMCPShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if srv, ok := cfg.MCP.Servers[name]; ok {
		printMCPServerDetails(name, "unified (all backends)", srv)
		return nil
	}

	for backend, servers := range cfg.MCP.Plugins {
		if srv, ok := servers[name]; ok {
			printMCPServerDetails(name, backend+" only", srv)
			return nil
		}
	}

	return fmt.Errorf("MCP server %q not found", name)
}

// printMCPServerDetails prints an MCP server's scope, command, args, and env.
func printMCPServerDetails(name, scope string, srv wire.MCPServer) {
	fmt.Printf("MCP Server: %s\n", name)
	fmt.Printf("Scope: %s\n", scope)
	fmt.Printf("Command: %s\n", srv.Command)
	if len(srv.Args) > 0 {
		fmt.Printf("Args: %s\n", strings.Join(srv.Args, " "))
	}
	if len(srv.Env) > 0 {
		fmt.Println("Environment:")
		for k, v := range srv.Env {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
}

func init() {
	rootCmd.AddCommand(mcpCmd)

	// The bare `ctxloom mcp` (and `mcp serve`) is the runtime server, referenced
	// by generated .mcp.json. Server-config management lives under `manage mcp`
	// (wired in manage.go); only the runtime entry hangs off root here.
	mcpCmd.AddCommand(mcpServeCmd)

	// Flags for the server-config CRUD commands (re-parented under manage mcp).
	mcpAddCmd.Flags().StringVarP(&mcpAddCommand, "command", "c", "", "Command to run the MCP server (required)")
	mcpAddCmd.Flags().StringSliceVarP(&mcpAddArgs, "args", "a", nil, "Arguments for the command (can be repeated)")
	mcpAddCmd.Flags().StringVarP(&mcpAddBackend, "backend", "b", "", "Backend to add server for (claude-code, gemini, or unified)")
	_ = mcpAddCmd.MarkFlagRequired("command")

	mcpRemoveCmd.Flags().StringVarP(&mcpRemoveBackend, "backend", "b", "", "Backend to remove server from")
}
