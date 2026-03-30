package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/operations"
)


var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server or manage MCP server configurations",
	Long: `Run scm as an MCP (Model Context Protocol) server, or manage MCP server configurations.

When called without subcommands, runs SCM as an MCP server over stdio.
Subcommands manage external MCP server configurations that are injected into backend settings.

RUNNING AS MCP SERVER:
  scm mcp              Run as MCP server over stdio
  scm mcp serve        Alias for running as MCP server

  Available tools when running as server:
    Context: list_fragments, get_fragment, create_fragment, delete_fragment, assemble_context
    Profiles: list_profiles, get_profile, create_profile, update_profile, delete_profile
    Prompts: list_prompts, get_prompt
    Search: search_content
    Hooks: apply_hooks
    MCP Servers: list_mcp_servers, add_mcp_server, remove_mcp_server, set_mcp_auto_register
    Remotes: list_remotes, add_remote, remove_remote, search_remotes, browse_remote, pull_remote
    Sync: sync_dependencies

MANAGING MCP SERVERS:
  scm mcp list         List configured MCP servers
  scm mcp add          Add an MCP server configuration
  scm mcp remove       Remove an MCP server configuration
  scm mcp show         Show details of an MCP server
  scm mcp auto-register Configure auto-registration of SCM's MCP server`,
	RunE: runMCPServer,
}

// MCP subcommands for managing MCP server configurations

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as MCP server over stdio",
	Long:  `Run scm as an MCP (Model Context Protocol) server over stdio. This is the default behavior when running 'scm mcp' without subcommands.`,
	RunE:  runMCPServer,
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
			fmt.Printf("Auto-register SCM MCP server: %v\n", result.AutoRegister)
			fmt.Println("\nUse 'scm mcp add <name> --command <cmd>' to add one.")
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
	mcpAddCommand string
	mcpAddArgs    []string
	mcpAddBackend string
)

var mcpAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add an MCP server configuration",
	Long: `Add an MCP server to be injected into backend settings.

Examples:
  scm mcp add my-server --command "npx my-mcp-server"
  scm mcp add tools --command "python" --args "-m,mcp_tools"
  scm mcp add claude-only --command "./server" --backend claude-code`,
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
		fmt.Println("Run 'scm run' or 'scm hook apply' to apply changes to backend settings.")
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
		fmt.Println("Run 'scm run' or 'scm hook apply' to apply changes to backend settings.")
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
	Short: "Configure auto-registration of SCM's MCP server",
	Long: `Configure whether SCM automatically registers its own MCP server.

When enabled (default), SCM injects its own MCP server into backend settings,
allowing AI agents to access SCM tools (fragments, profiles, prompts, etc.).

Examples:
  scm mcp auto-register           # Show current setting
  scm mcp auto-register --disable # Disable auto-registration
  scm mcp auto-register --enable  # Enable auto-registration (default)`,
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
	mcpAutoRegisterCmd.Flags().BoolVar(&mcpAutoRegisterDisable, "disable", false, "Disable SCM MCP server auto-registration")
	mcpAutoRegisterCmd.Flags().Bool("enable", false, "Enable SCM MCP server auto-registration")
}

// MCP JSON-RPC types

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func runMCPServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, shutdownSignals...)
	go func() {
		<-sigCh
		cancel()
		// Close stdin to unblock any pending reads
		_ = os.Stdin.Close()
	}()

	server := &mcpServer{
		reader: bufio.NewReader(os.Stdin),
		writer: os.Stdout,
	}

	return server.run(ctx)
}

type mcpServer struct {
	reader *bufio.Reader
	writer io.Writer
	cfg    *config.Config
}

type readResult struct {
	line []byte
	err  error
}

func (s *mcpServer) run(ctx context.Context) error {
	lineCh := make(chan readResult, 1)

	// Start reader goroutine
	go func() {
		for {
			line, err := s.reader.ReadBytes('\n')
			select {
			case lineCh <- readResult{line, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Connection health check ticker - detects when client disappears without closing
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	originalPPID := os.Getppid()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Check if parent process has changed (reparented to init/systemd)
			// This detects when the client process has exited but stdin hasn't closed
			currentPPID := os.Getppid()
			if currentPPID != originalPPID {
				fmt.Fprintf(os.Stderr, "SCM MCP: parent changed %d -> %d, exiting\n", originalPPID, currentPPID)
				return nil
			}
		case result := <-lineCh:
			if result.err != nil {
				// Any stdin read error means the client disconnected.
				// This is normal - treat all read errors as graceful shutdown.
				// Common cases: io.EOF, os.ErrClosed, pipe broken, etc.
				fmt.Fprintf(os.Stderr, "SCM MCP: stdin closed: %v\n", result.err)
				return nil
			}

			var req mcpRequest
			if err := json.Unmarshal(result.line, &req); err != nil {
				s.sendError(nil, -32700, "Parse error")
				continue
			}

			// Debug: log incoming requests
			fmt.Fprintf(os.Stderr, "SCM MCP: received %s (id=%v)\n", req.Method, req.ID)

			resp := s.handleRequest(ctx, &req)
			if resp != nil {
				s.sendResponse(resp)
			}
		}
	}
}

func (s *mcpServer) sendResponse(resp *mcpResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		// Marshal error - send a minimal error response
		_, _ = fmt.Fprintf(os.Stderr, "MCP: failed to marshal response: %v\n", err)
		_, _ = fmt.Fprintln(s.writer, `{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal marshal error"}}`)
		return
	}
	_, _ = fmt.Fprintln(s.writer, string(data))
}

func (s *mcpServer) sendError(id interface{}, code int, message string) {
	resp := &mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcpError{Code: code, Message: message},
	}
	s.sendResponse(resp)
}

func (s *mcpServer) handleRequest(ctx context.Context, req *mcpRequest) *mcpResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

func (s *mcpServer) handleInitialize(req *mcpRequest) *mcpResponse {

	cfg, err := config.Load()
	if err != nil {
		// Log the error but continue with an empty config
		fmt.Fprintf(os.Stderr, "SCM: warning: failed to load config: %v\n", err)
		cfg = &config.Config{
			LM:       config.LMConfig{Plugins: make(map[string]config.PluginConfig)},
			Profiles: make(map[string]config.Profile),
			Warnings: []string{fmt.Sprintf("failed to load config: %v", err)},
		}
	}
	s.cfg = cfg

	// Output any warnings collected during config loading
	for _, warning := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "SCM: warning: %s\n", warning)
	}

	// Auto-sync dependencies on startup if enabled (blocking, graceful failure)
	if cfg.Sync.ShouldAutoSync() {
		fmt.Fprintf(os.Stderr, "SCM: syncing remote bundles and profiles from config...\n")
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
		result, err := operations.SyncOnStartup(syncCtx, cfg)
		syncCancel()
		if err != nil {
			// Log but don't fail - missing deps will be handled when accessed
			fmt.Fprintf(os.Stderr, "SCM: warning: sync failed: %v\n", err)
		} else if result.Status != "up_to_date" && result.Installed+result.Updated > 0 {
			fmt.Fprintf(os.Stderr, "SCM: %s\n", result.Message)
		} else if result.Errors > 0 {
			fmt.Fprintf(os.Stderr, "SCM: warning: sync completed with %d errors\n", result.Errors)
		}
	}

	// Transform llm.md/scm.md to backend-specific context files (blocking, graceful failure)
	ctxResult, err := operations.TransformContextOnStartup(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SCM: warning: context transform failed: %v\n", err)
	} else if ctxResult.Status == "no_source" {
		// No llm.md or scm.md - that's fine, just skip silently
	} else if ctxResult.Status == "deferred" {
		// Context regeneration is deferred - that's fine
		fmt.Fprintf(os.Stderr, "SCM: context regeneration deferred\n")
	} else {
		if len(ctxResult.Errors) > 0 {
			for _, e := range ctxResult.Errors {
				fmt.Fprintf(os.Stderr, "SCM: warning: %s\n", e)
			}
		}
		// Warn about user-managed context files
		for _, w := range ctxResult.Warnings {
			fmt.Fprintf(os.Stderr, "SCM: warning: %s\n", w)
		}
		if ctxResult.Message != "" && !strings.HasPrefix(ctxResult.Message, "Context files up to date") {
			// Only log when there are actual changes
			fmt.Fprintf(os.Stderr, "SCM: %s\n", ctxResult.Message)
		}
	}

	// Apply hooks (including slash command generation) - graceful failure
	hooksResult, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "SCM: warning: failed to apply hooks: %v\n", err)
	}
	_ = hooksResult // Successfully applied - don't log unless verbose

	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "scm",
				"version": Version,
			},
		},
	}
}

func (s *mcpServer) handleToolsList(req *mcpRequest) *mcpResponse {
	tools := append(s.getLocalTools(), s.getRemoteTools()...)
	tools = append(tools, s.getMemoryTools()...)

	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"tools": tools,
		},
	}
}

func (s *mcpServer) getRemoteTools() []mcpToolInfo {
	return []mcpToolInfo{
		{
			Name:        "list_remotes",
			Description: "List configured remote sources for fragments and prompts",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "search_remotes",
			Description: "Search for bundles and profiles across ALL configured remotes. Use this when looking for a fragment or profile by name without knowing which remote contains it.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query - can be a name, tag (tag:foo), author (author:name), or plain text",
					},
					"item_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"bundle", "fragment", "profile"},
						"description": "Type of items to search for (default: all)",
					},
				},
			},
		},
		{
			Name:        "discover_remotes",
			Description: "Search GitHub/GitLab for SCM repositories containing fragments and prompts",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Optional search term to filter repositories",
					},
					"source": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"github", "gitlab", "all"},
						"description": "Which forge to search (default: all)",
					},
					"min_stars": map[string]interface{}{
						"type":        "integer",
						"description": "Minimum star count filter (default: 0)",
					},
				},
			},
		},
		{
			Name:        "browse_remote",
			Description: "List items (fragments, prompts, profiles) available in a remote repository",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"remote"},
				"properties": map[string]interface{}{
					"remote": map[string]interface{}{
						"type":        "string",
						"description": "Remote name (from list_remotes)",
					},
					"item_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"fragment", "prompt", "profile"},
						"description": "Type of items to list (default: all)",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Subdirectory path to browse (optional)",
					},
				},
			},
		},
		{
			Name:        "pull_remote",
			Description: "Pull (install) a bundle or profile from a remote repository. Fetches and installs in one step.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"reference", "item_type"},
				"properties": map[string]interface{}{
					"reference": map[string]interface{}{
						"type":        "string",
						"description": "Remote reference (e.g., 'remote-name/bundle-name' or 'remote-name/bundle-name@v1.0.0')",
					},
					"item_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"bundle", "fragment", "profile"},
						"description": "Type of item to pull",
					},
				},
			},
		},
	}
}

func (s *mcpServer) getLocalTools() []mcpToolInfo {
	return []mcpToolInfo{
		{
			Name:        "list_fragments",
			Description: "List available local context fragments with their tags and source locations",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Text search on name (optional)",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Filter by tags (optional)",
					},
					"sort_by": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"name", "source"},
						"description": "Sort field (default: name)",
					},
					"sort_order": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order (default: asc)",
					},
				},
			},
		},
		{
			Name:        "get_fragment",
			Description: "Get a local fragment's content by name",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Fragment name (without extension)",
					},
				},
			},
		},
		{
			Name:        "list_profiles",
			Description: "List all configured profiles with their descriptions",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Text search on name or description (optional)",
					},
					"sort_by": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"name", "default"},
						"description": "Sort field (default: name)",
					},
					"sort_order": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order (default: asc)",
					},
				},
			},
		},
		{
			Name:        "get_profile",
			Description: "Get a profile's configuration including fragments, tags, and variables",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Profile name",
					},
				},
			},
		},
		{
			Name:        "assemble_context",
			Description: "Assemble context from a profile, fragments, and/or tags. Returns the combined context that would be sent to an AI.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"profile": map[string]interface{}{
						"type":        "string",
						"description": "Profile name to use (optional)",
					},
					"bundles": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Additional fragment names to include (optional)",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Include all fragments with these tags (optional)",
					},
				},
			},
		},
		{
			Name:        "list_prompts",
			Description: "List saved prompts",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Text search on name (optional)",
					},
					"sort_by": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"name"},
						"description": "Sort field (default: name)",
					},
					"sort_order": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order (default: asc)",
					},
				},
			},
		},
		{
			Name:        "get_prompt",
			Description: "Get a saved prompt's content by name",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Prompt name (without extension)",
					},
				},
			},
		},
		{
			Name:        "search_content",
			Description: "Search across all SCM content types (fragments, prompts, profiles, MCP servers)",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search text (matches name, description, tags)",
					},
					"types": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string", "enum": []string{"fragment", "prompt", "profile", "mcp_server"}},
						"description": "Content types to search (default: all)",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Filter by tags (fragments only)",
					},
					"sort_by": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"name", "type", "relevance"},
						"description": "Sort field (default: relevance)",
					},
					"sort_order": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order (default: asc)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum results to return (default: 50)",
					},
				},
			},
		},
		// Profile management
		{
			Name:        "create_profile",
			Description: "Create a new profile with bundles, tags, and/or parent profiles",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Profile name",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Profile description (optional)",
					},
					"parents": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Parent profiles to inherit from (optional)",
					},
					"bundles": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Bundle references to include (optional)",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Tags to include fragments by (optional)",
					},
					"default": map[string]interface{}{
						"type":        "boolean",
						"description": "Set as default profile (optional)",
					},
					"exclude_fragments": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Fragment names to exclude (optional)",
					},
					"exclude_prompts": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Prompt names to exclude (optional)",
					},
					"exclude_mcp": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "MCP server names to exclude (optional)",
					},
				},
			},
		},
		{
			Name:        "update_profile",
			Description: "Update an existing profile by adding/removing bundles, tags, or parents",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Profile name to update",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "New description (optional)",
					},
					"add_parents": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Parent profiles to add (optional)",
					},
					"remove_parents": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Parent profiles to remove (optional)",
					},
					"add_bundles": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Bundles to add (optional)",
					},
					"remove_bundles": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Bundles to remove (optional)",
					},
					"add_tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Tags to add (optional)",
					},
					"remove_tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Tags to remove (optional)",
					},
					"default": map[string]interface{}{
						"type":        "boolean",
						"description": "Set as default profile (optional)",
					},
					"add_exclude_fragments": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Fragment names to add to exclusion list (optional)",
					},
					"remove_exclude_fragments": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Fragment names to remove from exclusion list (optional)",
					},
					"add_exclude_prompts": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Prompt names to add to exclusion list (optional)",
					},
					"remove_exclude_prompts": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Prompt names to remove from exclusion list (optional)",
					},
					"add_exclude_mcp": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "MCP server names to add to exclusion list (optional)",
					},
					"remove_exclude_mcp": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "MCP server names to remove from exclusion list (optional)",
					},
				},
			},
		},
		{
			Name:        "delete_profile",
			Description: "Delete a profile",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Profile name to delete",
					},
				},
			},
		},
		// Fragment management
		{
			Name:        "create_fragment",
			Description: "Create a new context fragment",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "content"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Fragment name (without extension)",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Fragment content (markdown)",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Tags for the fragment (optional)",
					},
					"version": map[string]interface{}{
						"type":        "string",
						"description": "Version string (optional, default: 1.0)",
					},
				},
			},
		},
		{
			Name:        "delete_fragment",
			Description: "Delete a local context fragment",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Fragment name to delete",
					},
				},
			},
		},
		// Hooks management
		{
			Name:        "apply_hooks",
			Description: "Apply/reapply SCM hooks to backend configuration files (.claude/settings.json, .gemini/settings.json)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"backend": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"claude-code", "gemini", "all"},
						"description": "Which backend to apply hooks for (default: all)",
					},
					"regenerate_context": map[string]interface{}{
						"type":        "boolean",
						"description": "Also regenerate the context file (default: true)",
					},
				},
			},
		},
		// Remote management
		{
			Name:        "add_remote",
			Description: "Register a new remote source for fragments and prompts",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "url"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Short name for the remote (e.g., 'alice')",
					},
					"url": map[string]interface{}{
						"type":        "string",
						"description": "Repository URL (e.g., 'alice/scm' or 'https://github.com/alice/scm')",
					},
				},
			},
		},
		{
			Name:        "remove_remote",
			Description: "Remove a registered remote source",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Remote name to remove",
					},
				},
			},
		},
		// MCP server management
		{
			Name:        "list_mcp_servers",
			Description: "List configured MCP servers",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Text search on name or command (optional)",
					},
					"sort_by": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"name", "command"},
						"description": "Sort field (default: name)",
					},
					"sort_order": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"asc", "desc"},
						"description": "Sort order (default: asc)",
					},
				},
			},
		},
		{
			Name:        "add_mcp_server",
			Description: "Add an MCP server to the configuration",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "command"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Server name (unique identifier)",
					},
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Command to run the MCP server",
					},
					"args": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Command arguments (optional)",
					},
					"backend": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"unified", "claude-code", "gemini"},
						"description": "Backend to add server for (default: unified for all backends)",
					},
				},
			},
		},
		{
			Name:        "remove_mcp_server",
			Description: "Remove an MCP server from the configuration",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Server name to remove",
					},
					"backend": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"unified", "claude-code", "gemini"},
						"description": "Backend to remove server from (default: all)",
					},
				},
			},
		},
		{
			Name:        "set_mcp_auto_register",
			Description: "Enable or disable auto-registration of SCM's own MCP server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"enabled"},
				"properties": map[string]interface{}{
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to auto-register SCM's MCP server",
					},
				},
			},
		},
		// Sync management
		{
			Name:        "sync_dependencies",
			Description: "Sync remote bundles and profiles referenced in config. Automatically fetches missing dependencies, updates lockfile, and applies hooks.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"profiles": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Specific profiles to sync (default: all profiles)",
					},
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Re-pull even if already installed (default: false)",
					},
					"lock": map[string]interface{}{
						"type":        "boolean",
						"description": "Update lockfile after sync (default: true)",
					},
					"apply_hooks": map[string]interface{}{
						"type":        "boolean",
						"description": "Apply hooks after sync (default: true)",
					},
				},
			},
		},
	}
}

func (s *mcpServer) handleToolsCall(ctx context.Context, req *mcpRequest) *mcpResponse {
	if s.cfg == nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32002, Message: "Server not initialized"},
		}
	}

	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "Invalid params"},
		}
	}

	var result interface{}
	var err error

	switch params.Name {
	// Local tools
	case "list_fragments":
		result, err = s.toolListFragments(ctx, params.Arguments)
	case "get_fragment":
		result, err = s.toolGetFragment(ctx, params.Arguments)
	case "list_profiles":
		result, err = s.toolListProfiles(ctx, params.Arguments)
	case "get_profile":
		result, err = s.toolGetProfile(ctx, params.Arguments)
	case "assemble_context":
		result, err = s.toolAssembleContext(ctx, params.Arguments)
	case "list_prompts":
		result, err = s.toolListPrompts(ctx, params.Arguments)
	case "get_prompt":
		result, err = s.toolGetPrompt(ctx, params.Arguments)
	case "search_content":
		result, err = s.toolSearchContent(ctx, params.Arguments)
	// Profile management
	case "create_profile":
		result, err = s.toolCreateProfile(ctx, params.Arguments)
	case "update_profile":
		result, err = s.toolUpdateProfile(ctx, params.Arguments)
	case "delete_profile":
		result, err = s.toolDeleteProfile(ctx, params.Arguments)
	// Fragment management
	case "create_fragment":
		result, err = s.toolCreateFragment(ctx, params.Arguments)
	case "delete_fragment":
		result, err = s.toolDeleteFragment(ctx, params.Arguments)
	// Hooks management
	case "apply_hooks":
		result, err = s.toolApplyHooks(ctx, params.Arguments)
	// Remote tools
	case "list_remotes":
		result, err = s.toolListRemotes(ctx, params.Arguments)
	case "search_remotes":
		result, err = s.toolSearchRemotes(ctx, params.Arguments)
	case "discover_remotes":
		result, err = s.toolDiscoverRemotes(ctx, params.Arguments)
	case "browse_remote":
		result, err = s.toolBrowseRemote(ctx, params.Arguments)
	case "pull_remote":
		result, err = s.toolPullRemote(ctx, params.Arguments)
	// Remote management
	case "add_remote":
		result, err = s.toolAddRemote(ctx, params.Arguments)
	case "remove_remote":
		result, err = s.toolRemoveRemote(ctx, params.Arguments)
	// MCP server management
	case "list_mcp_servers":
		result, err = s.toolListMCPServers(ctx, params.Arguments)
	case "add_mcp_server":
		result, err = s.toolAddMCPServer(ctx, params.Arguments)
	case "remove_mcp_server":
		result, err = s.toolRemoveMCPServer(ctx, params.Arguments)
	case "set_mcp_auto_register":
		result, err = s.toolSetMCPAutoRegister(ctx, params.Arguments)
	// Lockfile management
	// Sync management
	case "sync_dependencies":
		result, err = s.toolSyncDependencies(ctx, params.Arguments)
	// Memory tools
	case "compact_session":
		result, err = s.toolCompactSession(ctx, params.Arguments)
	case "list_sessions":
		result, err = s.toolListSessions(ctx, params.Arguments)
	case "load_session":
		result, err = s.toolLoadSession(ctx, params.Arguments)
	case "recover_session":
		result, err = s.toolRecoverSession(ctx, params.Arguments)
	case "browse_session_history":
		result, err = s.toolBrowseSessionHistory(ctx, params.Arguments)
	case "get_previous_session":
		result, err = s.toolGetPreviousSession(ctx, params.Arguments)
	default:
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "Unknown tool: " + params.Name},
		}
	}

	if err != nil {
		errText := "Error: " + err.Error()
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: mcpToolResult{
				Content: []mcpContent{{Type: "text", Text: errText}},
				IsError: true,
			},
		}
	}

	text, _ := json.MarshalIndent(result, "", "  ")
	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: string(text)}},
		},
	}
}

// ============================================================================
// Tool implementations
// ============================================================================

func (s *mcpServer) toolListFragments(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string   `json:"query"`
		Tags      []string `json:"tags"`
		SortBy    string   `json:"sort_by"`
		SortOrder string   `json:"sort_order"`
	}
	_ = json.Unmarshal(args, &params)

	result, err := operations.ListFragments(ctx, s.cfg, operations.ListFragmentsRequest{
		Query:     params.Query,
		Tags:      params.Tags,
		SortBy:    params.SortBy,
		SortOrder: params.SortOrder,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"fragments": result.Fragments,
		"count":     result.Count,
	}, nil
}


func (s *mcpServer) toolGetFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.GetFragment(ctx, s.cfg, operations.GetFragmentRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":    result.Name,
		"tags":    result.Tags,
		"content": result.Content,
	}, nil
}

func (s *mcpServer) toolListProfiles(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string `json:"query"`
		SortBy    string `json:"sort_by"`
		SortOrder string `json:"sort_order"`
	}
	_ = json.Unmarshal(args, &params)

	result, err := operations.ListProfiles(ctx, s.cfg, operations.ListProfilesRequest{
		Query:     params.Query,
		SortBy:    params.SortBy,
		SortOrder: params.SortOrder,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"profiles": result.Profiles,
		"count":    result.Count,
		"defaults": result.Defaults,
	}, nil
}

func (s *mcpServer) toolGetProfile(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.GetProfile(ctx, s.cfg, operations.GetProfileRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":        result.Name,
		"description": result.Description,
		"parents":     result.Parents,
		"tags":        result.Tags,
		"bundles":     result.Bundles,
		"variables":   result.Variables,
		"path":        result.Path,
	}, nil
}

func (s *mcpServer) toolAssembleContext(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Profile   string   `json:"profile"`
		Fragments []string `json:"fragments"`
		Tags      []string `json:"tags"`
	}
	// Unmarshal errors are non-fatal - use defaults for optional params
	_ = json.Unmarshal(args, &params)

	return operations.AssembleContext(ctx, s.cfg, operations.AssembleContextRequest{
		Profile:   params.Profile,
		Fragments: params.Fragments,
		Tags:      params.Tags,
	})
}

func (s *mcpServer) toolListPrompts(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string `json:"query"`
		SortBy    string `json:"sort_by"`
		SortOrder string `json:"sort_order"`
	}
	_ = json.Unmarshal(args, &params)

	result, err := operations.ListPrompts(ctx, s.cfg, operations.ListPromptsRequest{
		Query:     params.Query,
		SortBy:    params.SortBy,
		SortOrder: params.SortOrder,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"prompts": result.Prompts,
		"count":   result.Count,
	}, nil
}

func (s *mcpServer) toolGetPrompt(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.GetPrompt(ctx, s.cfg, operations.GetPromptRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":    result.Name,
		"content": result.Content,
	}, nil
}

func (s *mcpServer) toolSearchContent(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string   `json:"query"`
		Types     []string `json:"types"`
		Tags      []string `json:"tags"`
		SortBy    string   `json:"sort_by"`
		SortOrder string   `json:"sort_order"`
		Limit     int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.SearchContent(ctx, s.cfg, operations.SearchContentRequest{
		Query:     params.Query,
		Types:     params.Types,
		Tags:      params.Tags,
		SortBy:    params.SortBy,
		SortOrder: params.SortOrder,
		Limit:     params.Limit,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"results": result.Results,
		"count":   result.Count,
		"query":   result.Query,
	}, nil
}

// ============================================================================
// Remote tool implementations
// ============================================================================

func (s *mcpServer) toolListRemotes(ctx context.Context, args json.RawMessage) (interface{}, error) {
	result, err := operations.ListRemotes(ctx, s.cfg, operations.ListRemotesRequest{})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"remotes": result.Remotes,
		"count":   result.Count,
	}, nil
}

func (s *mcpServer) toolSearchRemotes(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query    string `json:"query"`
		ItemType string `json:"item_type"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.SearchRemotes(ctx, s.cfg, operations.SearchRemotesRequest{
		Query:    params.Query,
		ItemType: params.ItemType,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"results":  result.Results,
		"count":    result.Count,
		"query":    result.Query,
		"warnings": result.Warnings,
	}, nil
}

func (s *mcpServer) toolDiscoverRemotes(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query    string `json:"query"`
		Source   string `json:"source"`
		MinStars int    `json:"min_stars"`
	}
	_ = json.Unmarshal(args, &params)

	result, err := operations.DiscoverRemotes(ctx, s.cfg, operations.DiscoverRemotesRequest{
		Query:    params.Query,
		Source:   params.Source,
		MinStars: params.MinStars,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"repositories": result.Repositories,
		"count":        result.Count,
		"errors":       result.Errors,
	}, nil
}

func (s *mcpServer) toolBrowseRemote(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Remote   string `json:"remote"`
		ItemType string `json:"item_type"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.BrowseRemote(ctx, s.cfg, operations.BrowseRemoteRequest{
		Remote:   params.Remote,
		ItemType: params.ItemType,
		Path:     params.Path,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"remote": result.Remote,
		"url":    result.URL,
		"items":  result.Items,
		"count":  result.Count,
	}, nil
}

func (s *mcpServer) toolPullRemote(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Reference string `json:"reference"`
		ItemType  string `json:"item_type"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	// Normalize item_type (fragment -> bundle)
	itemType := params.ItemType
	if itemType == "fragment" {
		itemType = "bundle"
	}

	// Fetch the content
	fetchResult, err := operations.FetchRemoteContent(ctx, s.cfg, operations.FetchRemoteContentRequest{
		Reference: params.Reference,
		ItemType:  itemType,
	})
	if err != nil {
		return nil, err
	}

	// Write immediately
	writeResult, err := operations.WriteRemoteItem(ctx, s.cfg, operations.WriteRemoteItemRequest{
		Reference: fetchResult.PullToken,
		ItemType:  itemType,
		Content:   []byte(fetchResult.Content),
		SHA:       fetchResult.FullSHA,
	})
	if err != nil {
		return nil, err
	}

	result := map[string]interface{}{
		"status":      writeResult.Status,
		"reference":   writeResult.Reference,
		"item_type":   writeResult.ItemType,
		"local_path":  writeResult.LocalPath,
		"sha":         writeResult.SHA,
		"source_url":  fetchResult.SourceURL,
		"overwritten": writeResult.Overwritten,
	}

	// Extract installation instructions from bundle
	if itemType == "bundle" {
		bundle, parseErr := bundles.ParseBundle([]byte(fetchResult.Content))
		if parseErr == nil && bundle.Installation != "" {
			result["installation"] = bundle.Installation
		}
	}

	return result, nil
}

// ============================================================================
// Profile management tools
// ============================================================================

func (s *mcpServer) toolCreateProfile(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name             string   `json:"name"`
		Description      string   `json:"description"`
		Parents          []string `json:"parents"`
		Bundles          []string `json:"bundles"`
		Tags             []string `json:"tags"`
		Default          bool     `json:"default"`
		ExcludeFragments []string `json:"exclude_fragments"`
		ExcludePrompts   []string `json:"exclude_prompts"`
		ExcludeMCP       []string `json:"exclude_mcp"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.CreateProfile(ctx, s.cfg, operations.CreateProfileRequest{
		Name:             params.Name,
		Description:      params.Description,
		Parents:          params.Parents,
		Bundles:          params.Bundles,
		Tags:             params.Tags,
		Default:          params.Default,
		ExcludeFragments: params.ExcludeFragments,
		ExcludePrompts:   params.ExcludePrompts,
		ExcludeMCP:       params.ExcludeMCP,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  result.Status,
		"profile": result.Profile,
		"path":    result.Path,
	}, nil
}

func (s *mcpServer) toolUpdateProfile(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name                   string   `json:"name"`
		Description            *string  `json:"description"`
		AddParents             []string `json:"add_parents"`
		RemoveParents          []string `json:"remove_parents"`
		AddBundles             []string `json:"add_bundles"`
		RemoveBundles          []string `json:"remove_bundles"`
		AddTags                []string `json:"add_tags"`
		RemoveTags             []string `json:"remove_tags"`
		Default                *bool    `json:"default"`
		AddExcludeFragments    []string `json:"add_exclude_fragments"`
		RemoveExcludeFragments []string `json:"remove_exclude_fragments"`
		AddExcludePrompts      []string `json:"add_exclude_prompts"`
		RemoveExcludePrompts   []string `json:"remove_exclude_prompts"`
		AddExcludeMCP          []string `json:"add_exclude_mcp"`
		RemoveExcludeMCP       []string `json:"remove_exclude_mcp"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.UpdateProfile(ctx, s.cfg, operations.UpdateProfileRequest{
		Name:                   params.Name,
		Description:            params.Description,
		AddParents:             params.AddParents,
		RemoveParents:          params.RemoveParents,
		AddBundles:             params.AddBundles,
		RemoveBundles:          params.RemoveBundles,
		AddTags:                params.AddTags,
		RemoveTags:             params.RemoveTags,
		Default:                params.Default,
		AddExcludeFragments:    params.AddExcludeFragments,
		RemoveExcludeFragments: params.RemoveExcludeFragments,
		AddExcludePrompts:      params.AddExcludePrompts,
		RemoveExcludePrompts:   params.RemoveExcludePrompts,
		AddExcludeMCP:          params.AddExcludeMCP,
		RemoveExcludeMCP:       params.RemoveExcludeMCP,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  result.Status,
		"profile": result.Profile,
		"changes": result.Changes,
		"path":    result.Path,
	}, nil
}

func (s *mcpServer) toolDeleteProfile(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.DeleteProfile(ctx, s.cfg, operations.DeleteProfileRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  result.Status,
		"profile": result.Profile,
	}, nil
}

// ============================================================================
// Fragment management tools
// ============================================================================

func (s *mcpServer) toolCreateFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name    string   `json:"name"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Version string   `json:"version"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.CreateFragment(ctx, s.cfg, operations.CreateFragmentRequest{
		Name:    params.Name,
		Content: params.Content,
		Tags:    params.Tags,
		Version: params.Version,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":      result.Status,
		"fragment":    result.Fragment,
		"path":        result.Path,
		"overwritten": result.Overwritten,
	}, nil
}

func (s *mcpServer) toolDeleteFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	_, err := operations.DeleteFragment(ctx, s.cfg, operations.DeleteFragmentRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "deleted",
	}, nil
}

// ============================================================================
// Hooks management tools
// ============================================================================

func (s *mcpServer) toolApplyHooks(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Backend           string `json:"backend"`
		RegenerateContext *bool  `json:"regenerate_context"`
	}
	_ = json.Unmarshal(args, &params)

	regenerate := true
	if params.RegenerateContext != nil {
		regenerate = *params.RegenerateContext
	}

	return operations.ApplyHooks(ctx, s.cfg, operations.ApplyHooksRequest{
		Backend:           params.Backend,
		RegenerateContext: regenerate,
	})
}

// ============================================================================
// Remote management tools
// ============================================================================

func (s *mcpServer) toolAddRemote(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.AddRemote(ctx, s.cfg, operations.AddRemoteRequest{
		Name: params.Name,
		URL:  params.URL,
	})
	if err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"status": result.Status,
		"name":   result.Name,
		"url":    result.URL,
	}
	if result.Warning != "" {
		resp["warning"] = result.Warning
	}

	return resp, nil
}

func (s *mcpServer) toolRemoveRemote(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.RemoveRemote(ctx, s.cfg, operations.RemoveRemoteRequest{
		Name: params.Name,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": result.Status,
		"name":   result.Name,
	}, nil
}

// ============================================================================
// Sync management tools
// ============================================================================

func (s *mcpServer) toolSyncDependencies(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Profiles   []string `json:"profiles"`
		Force      bool     `json:"force"`
		Lock       *bool    `json:"lock"`
		ApplyHooks *bool    `json:"apply_hooks"`
	}
	_ = json.Unmarshal(args, &params)

	// Default to true for lock and apply_hooks
	lock := true
	if params.Lock != nil {
		lock = *params.Lock
	}
	applyHooks := true
	if params.ApplyHooks != nil {
		applyHooks = *params.ApplyHooks
	}

	return operations.SyncDependencies(ctx, s.cfg, operations.SyncDependenciesRequest{
		Profiles:   params.Profiles,
		Force:      params.Force,
		Lock:       lock,
		ApplyHooks: applyHooks,
	})
}

// ============================================================================
// MCP server management tools
// ============================================================================

func (s *mcpServer) toolListMCPServers(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string `json:"query"`
		SortBy    string `json:"sort_by"`
		SortOrder string `json:"sort_order"`
	}
	_ = json.Unmarshal(args, &params)

	return operations.ListMCPServers(ctx, s.cfg, operations.ListMCPServersRequest{
		Query:     params.Query,
		SortBy:    params.SortBy,
		SortOrder: params.SortOrder,
	})
}

func (s *mcpServer) toolAddMCPServer(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name    string   `json:"name"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Backend string   `json:"backend"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.AddMCPServer(ctx, s.cfg, operations.AddMCPServerRequest{
		Name:    params.Name,
		Command: params.Command,
		Args:    params.Args,
		Backend: params.Backend,
	})
	if err != nil {
		return nil, err
	}

	// Update server's config reference
	if result.Config != nil {
		s.cfg = result.Config
	}

	return result, nil
}

func (s *mcpServer) toolRemoveMCPServer(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name    string `json:"name"`
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.RemoveMCPServer(ctx, s.cfg, operations.RemoveMCPServerRequest{
		Name:    params.Name,
		Backend: params.Backend,
	})
	if err != nil {
		return nil, err
	}

	// Update server's config reference
	if result.Config != nil {
		s.cfg = result.Config
	}

	return result, nil
}

func (s *mcpServer) toolSetMCPAutoRegister(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.SetMCPAutoRegister(ctx, s.cfg, operations.SetMCPAutoRegisterRequest{
		Enabled: params.Enabled,
	})
	if err != nil {
		return nil, err
	}

	// Update server's config reference
	if result.Config != nil {
		s.cfg = result.Config
	}

	return result, nil
}

