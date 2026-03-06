package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/operations"
)

var mcpServersCmd = &cobra.Command{
	Use:   "mcp-servers",
	Short: "Manage MCP (Model Context Protocol) server configurations",
	Long: `Manage MCP server configurations that are injected into backend settings.

MCP servers extend AI agent capabilities by providing additional tools and resources.
SCM can manage these configurations and inject them into backend-specific settings
files (.claude/settings.json, .gemini/settings.json, etc.).

By default, SCM auto-registers its own MCP server. You can disable this with
'scm mcp-servers auto-register --disable'.`,
}

var mcpServersListCmd = &cobra.Command{
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
			fmt.Printf("Auto-register SCM MCP server: %v\n", result.AutoRegister)
			fmt.Println("\nUse 'scm mcp-servers add <name> --command <cmd>' to add one.")
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

		fmt.Printf("\nAuto-register SCM MCP server: %v\n", result.AutoRegister)
		return nil
	},
}

var (
	mcpServersAddCommand string
	mcpServersAddArgs    []string
	mcpServersAddBackend string
)

var mcpServersAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add an MCP server configuration",
	Long: `Add an MCP server to be injected into backend settings.

Examples:
  scm mcp-servers add my-server --command "npx my-mcp-server"
  scm mcp-servers add tools --command "python" --args "-m,mcp_tools"
  scm mcp-servers add claude-only --command "./server" --backend claude-code`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if mcpServersAddCommand == "" {
			return fmt.Errorf("--command is required")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.AddMCPServer(cmd.Context(), cfg, operations.AddMCPServerRequest{
			Name:    name,
			Command: mcpServersAddCommand,
			Args:    mcpServersAddArgs,
			Backend: mcpServersAddBackend,
		})
		if err != nil {
			return err
		}

		scope := "unified (all backends)"
		if result.Backend != "" && result.Backend != "unified" {
			scope = result.Backend + " only"
		}
		fmt.Printf("Added MCP server %q (%s)\n", result.Name, scope)
		fmt.Println("Run 'scm run' or 'scm hook apply' to apply changes to backend settings.")
		return nil
	},
}

var mcpServersRemoveBackend string

var mcpServersRemoveCmd = &cobra.Command{
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
			Backend: mcpServersRemoveBackend,
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
		fmt.Println("Run 'scm run' or 'scm hook apply' to apply changes to backend settings.")
		return nil
	},
}

var mcpServersShowCmd = &cobra.Command{
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

var mcpServersAutoRegisterDisable bool

var mcpServersAutoRegisterCmd = &cobra.Command{
	Use:   "auto-register",
	Short: "Configure auto-registration of SCM's MCP server",
	Long: `Configure whether SCM automatically registers its own MCP server.

When enabled (default), SCM injects its own MCP server into backend settings,
allowing AI agents to access SCM tools (fragments, profiles, prompts, etc.).

Examples:
  scm mcp-servers auto-register           # Show current setting
  scm mcp-servers auto-register --disable # Disable auto-registration
  scm mcp-servers auto-register --enable  # Enable auto-registration (default)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// If flags were provided, update the setting
		if cmd.Flags().Changed("disable") || cmd.Flags().Changed("enable") {
			enabled := !mcpServersAutoRegisterDisable

			result, err := operations.SetMCPAutoRegister(cmd.Context(), cfg, operations.SetMCPAutoRegisterRequest{
				Enabled: enabled,
			})
			if err != nil {
				return err
			}

			if result.AutoRegister {
				fmt.Println("SCM MCP server auto-registration: enabled")
			} else {
				fmt.Println("SCM MCP server auto-registration: disabled")
			}
			fmt.Println("Run 'scm run' or 'scm hook apply' to apply changes to backend settings.")
			return nil
		}

		// Show current setting
		fmt.Printf("SCM MCP server auto-registration: %v\n", cfg.MCP.ShouldAutoRegisterSCM())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(mcpServersCmd)

	mcpServersCmd.AddCommand(mcpServersListCmd)
	mcpServersCmd.AddCommand(mcpServersAddCmd)
	mcpServersCmd.AddCommand(mcpServersRemoveCmd)
	mcpServersCmd.AddCommand(mcpServersShowCmd)
	mcpServersCmd.AddCommand(mcpServersAutoRegisterCmd)

	mcpServersAddCmd.Flags().StringVarP(&mcpServersAddCommand, "command", "c", "", "Command to run the MCP server (required)")
	mcpServersAddCmd.Flags().StringSliceVarP(&mcpServersAddArgs, "args", "a", nil, "Arguments for the command (can be repeated)")
	mcpServersAddCmd.Flags().StringVarP(&mcpServersAddBackend, "backend", "b", "", "Backend to add server for (claude-code, gemini, or unified)")
	_ = mcpServersAddCmd.MarkFlagRequired("command")

	mcpServersRemoveCmd.Flags().StringVarP(&mcpServersRemoveBackend, "backend", "b", "", "Backend to remove server from")

	mcpServersAutoRegisterCmd.Flags().BoolVar(&mcpServersAutoRegisterDisable, "disable", false, "Disable SCM MCP server auto-registration")
	mcpServersAutoRegisterCmd.Flags().Bool("enable", false, "Enable SCM MCP server auto-registration")
}
