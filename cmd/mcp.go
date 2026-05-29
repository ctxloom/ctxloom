package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)


var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server or manage MCP server configurations",
	Long: `Run ctxloom as an MCP (Model Context Protocol) server, or manage MCP server configurations.

When called without subcommands, runs ctxloom as an MCP server over stdio.
Subcommands manage external MCP server configurations that are injected into backend settings.

RUNNING AS MCP SERVER:
  ctxloom mcp              Run as MCP server over stdio
  ctxloom mcp serve        Alias for running as MCP server

  Available tools when running as server:
    Context:  assemble_context, search_content
    Hooks:    apply_hooks
    Sync:     sync_dependencies
    Bundles:  create_bundle, update_bundle, delete_bundle
    Review:   acknowledge_bundle_review, decline_bundle, show_bundle_verbatim,
              trust_remote, approve_remote_pending, pin_bundle, unpin_bundle
    Sessions: compact_session, load_session, get_previous_session, recover_session
    Tasks:    task_add, task_list, task_set_status

  Read-only listings (fragments, profiles, prompts, remotes, mcp-servers,
  sessions) are exposed as MCP resources (ctxloom://...), not tools. Other
  management (remotes, profiles, fragments, mcp servers) is CLI-only via the
  corresponding subcommands.

MANAGING MCP SERVERS:
  ctxloom mcp list         List configured MCP servers
  ctxloom mcp add          Add an MCP server configuration
  ctxloom mcp remove       Remove an MCP server configuration
  ctxloom mcp show         Show details of an MCP server
  ctxloom mcp auto-register Configure auto-registration of ctxloom's MCP server`,
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
			fmt.Println("\nUse 'ctxloom mcp add <name> --command <cmd>' to add one.")
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
  ctxloom mcp add my-server --command "npx my-mcp-server"
  ctxloom mcp add tools --command "python" --args "-m,mcp_tools"
  ctxloom mcp add claude-only --command "./server" --backend claude-code`,
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
		fmt.Println("Run 'ctxloom run' or 'ctxloom hook apply' to apply changes to backend settings.")
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
		fmt.Println("Run 'ctxloom run' or 'ctxloom hook apply' to apply changes to backend settings.")
		return nil
	},
}

var mcpShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Check unified servers
		if srv, ok := cfg.MCP.Servers[name]; ok {
			fmt.Printf("MCP Server: %s\n", name)
			fmt.Printf("Scope: unified (all backends)\n")
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
			return nil
		}

		// Check backend-specific servers
		for backend, servers := range cfg.MCP.Plugins {
			if srv, ok := servers[name]; ok {
				fmt.Printf("MCP Server: %s\n", name)
				fmt.Printf("Scope: %s only\n", backend)
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
				return nil
			}
		}

		return fmt.Errorf("MCP server %q not found", name)
	},
}

var mcpAutoRegisterDisable bool

var mcpAutoRegisterCmd = &cobra.Command{
	Use:   "auto-register",
	Short: "Configure auto-registration of ctxloom's MCP server",
	Long: `Configure whether ctxloom automatically registers its own MCP server.

When enabled (default), ctxloom injects its own MCP server into backend settings,
allowing AI agents to access ctxloom tools (fragments, profiles, prompts, etc.).

Examples:
  ctxloom mcp auto-register           # Show current setting
  ctxloom mcp auto-register --disable # Disable auto-registration
  ctxloom mcp auto-register --enable  # Enable auto-registration (default)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// If flags were provided, update the setting
		if cmd.Flags().Changed("disable") || cmd.Flags().Changed("enable") {
			enabled := !mcpAutoRegisterDisable

			result, err := operations.SetMCPAutoRegister(cmd.Context(), cfg, operations.SetMCPAutoRegisterRequest{
				Enabled: enabled,
			})
			if err != nil {
				return err
			}

			if result.AutoRegister {
				fmt.Println("ctxloom MCP server auto-registration: enabled")
			} else {
				fmt.Println("ctxloom MCP server auto-registration: disabled")
			}
			fmt.Println("Run 'ctxloom run' or 'ctxloom hook apply' to apply changes to backend settings.")
			return nil
		}

		// Show current setting
		fmt.Printf("ctxloom MCP server auto-registration: %v\n", cfg.MCP.ShouldAutoRegisterCtxloom())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)

	// Add subcommands
	mcpCmd.AddCommand(mcpServeCmd)
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpAddCmd)
	mcpCmd.AddCommand(mcpRemoveCmd)
	mcpCmd.AddCommand(mcpShowCmd)
	mcpCmd.AddCommand(mcpAutoRegisterCmd)

	// Flags for add command
	mcpAddCmd.Flags().StringVarP(&mcpAddCommand, "command", "c", "", "Command to run the MCP server (required)")
	mcpAddCmd.Flags().StringSliceVarP(&mcpAddArgs, "args", "a", nil, "Arguments for the command (can be repeated)")
	mcpAddCmd.Flags().StringVarP(&mcpAddBackend, "backend", "b", "", "Backend to add server for (claude-code, gemini, or unified)")
	_ = mcpAddCmd.MarkFlagRequired("command")

	// Flags for remove command
	mcpRemoveCmd.Flags().StringVarP(&mcpRemoveBackend, "backend", "b", "", "Backend to remove server from")

	// Flags for auto-register command
	mcpAutoRegisterCmd.Flags().BoolVar(&mcpAutoRegisterDisable, "disable", false, "Disable ctxloom MCP server auto-registration")
	mcpAutoRegisterCmd.Flags().Bool("enable", false, "Enable ctxloom MCP server auto-registration")
}
