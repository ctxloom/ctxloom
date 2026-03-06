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
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/operations"
	"github.com/benjaminabbitt/scm/internal/profiles"
	"github.com/benjaminabbitt/scm/internal/remote"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server over stdio",
	Long: `Run scm as an MCP (Model Context Protocol) server over stdio.

This allows AI agents to interact with scm functionality using standard MCP tool calls.

Available tools:
  Context:
  - list_fragments: List available context fragments
  - get_fragment: Get a fragment's content by name
  - create_fragment: Create a new context fragment
  - delete_fragment: Delete a local fragment
  - assemble_context: Assemble context from profile/fragments/tags

  Profiles:
  - list_profiles: List configured profiles
  - get_profile: Get a profile's configuration
  - create_profile: Create a new profile
  - update_profile: Update an existing profile
  - delete_profile: Delete a profile

  Prompts:
  - list_prompts: List saved prompts
  - get_prompt: Get a prompt's content by name

  Search:
  - search_content: Search across all content types (fragments, prompts, profiles, MCP servers)

  Hooks:
  - apply_hooks: Apply/reapply SCM hooks to backend configs

  MCP Servers:
  - list_mcp_servers: List configured MCP servers
  - add_mcp_server: Add an MCP server to config
  - remove_mcp_server: Remove an MCP server from config
  - set_mcp_auto_register: Enable/disable SCM MCP auto-registration

  Remotes:
  - list_remotes: List configured remote sources
  - add_remote: Register a new remote source
  - remove_remote: Remove a remote source
  - discover_remotes: Search GitHub/GitLab for SCM repositories
  - browse_remote: List items available in a remote
  - preview_remote: Preview content before pulling
  - confirm_pull: Install a previewed item

  Lockfile:
  - lock_dependencies: Generate lockfile from installed items
  - install_dependencies: Install items from lockfile
  - check_outdated: Check for newer versions

Example:
  scm mcp`,
	RunE: runMCPServer,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
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
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	server := &mcpServer{
		reader:       bufio.NewReader(os.Stdin),
		writer:       os.Stdout,
		pendingPulls: make(map[string]*pendingPull),
	}

	return server.run(ctx)
}

type mcpServer struct {
	reader       *bufio.Reader
	writer       io.Writer
	cfg          *config.Config
	pendingPulls map[string]*pendingPull // token -> pending pull info
	pullMu       sync.RWMutex
}

// pendingPull stores preview data awaiting confirmation.
type pendingPull struct {
	Reference string          `json:"reference"` // remote/path@SHA
	ItemType  remote.ItemType `json:"item_type"`
	Content   []byte          `json:"content"`
	SHA       string          `json:"sha"`
	RemoteURL string          `json:"remote_url"`
}

// bundleLoader returns a bundle loader configured for the current config.
func (s *mcpServer) bundleLoader() *bundles.Loader {
	return bundles.NewLoader(s.cfg.GetBundleDirs(), s.cfg.Defaults.ShouldUseDistilled())
}

// profileLoader returns a profiles.Loader for directory-based profiles.
func (s *mcpServer) profileLoader() *profiles.Loader {
	return s.cfg.GetProfileLoader()
}

// loadProfile loads a profile by name, checking both config map and directory.
// Config map profiles take precedence over directory-based profiles.
func (s *mcpServer) loadProfile(name string) (*config.Profile, error) {
	// First check config map
	if profile, ok := s.cfg.Profiles[name]; ok {
		return &profile, nil
	}

	// Fall back to directory-based profile
	loader := s.profileLoader()
	dirProfile, err := loader.Load(name)
	if err != nil {
		return nil, fmt.Errorf("unknown profile: %s", name)
	}

	// Convert profiles.Profile to config.Profile
	return &config.Profile{
		Description: dirProfile.Description,
		Parents:     dirProfile.Parents,
		Tags:        dirProfile.Tags,
		Fragments:   dirProfile.Bundles, // Bundles field contains fragment references
		Variables:   dirProfile.Variables,
	}, nil
}

// resolveProfile resolves a profile with inheritance, checking both sources.
func (s *mcpServer) resolveProfile(name string) (*config.Profile, error) {
	// First try config-based resolution
	profile, err := config.ResolveProfile(s.cfg.Profiles, name)
	if err == nil {
		return profile, nil
	}

	// Fall back to directory-based resolution
	loader := s.profileLoader()
	resolved, err := loader.ResolveProfile(name, nil)
	if err != nil {
		return nil, fmt.Errorf("unknown profile: %s", name)
	}

	// Convert to config.Profile
	return &config.Profile{
		Tags:      resolved.Tags,
		Fragments: resolved.Bundles,
		Variables: resolved.Variables,
	}, nil
}

func (s *mcpServer) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(nil, -32700, "Parse error")
			continue
		}

		resp := s.handleRequest(ctx, &req)
		if resp != nil {
			s.sendResponse(resp)
		}
	}
}

func (s *mcpServer) sendResponse(resp *mcpResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		// Marshal error - send a minimal error response
		fmt.Fprintf(os.Stderr, "MCP: failed to marshal response: %v\n", err)
		fmt.Fprintln(s.writer, `{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal marshal error"}}`)
		return
	}
	fmt.Fprintln(s.writer, string(data))
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
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32603, Message: "Failed to load config: " + err.Error()},
		}
	}
	s.cfg = cfg

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
			Name:        "preview_remote",
			Description: "Preview content of a remote item before pulling. Returns a pull_token for confirm_pull.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"reference", "item_type"},
				"properties": map[string]interface{}{
					"reference": map[string]interface{}{
						"type":        "string",
						"description": "Remote reference (e.g., 'github/general/tdd' or 'github/security@v1.0.0')",
					},
					"item_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"fragment", "prompt", "profile"},
						"description": "Type of item to preview",
					},
				},
			},
		},
		{
			Name:        "confirm_pull",
			Description: "Install a previously previewed item using the pull_token from preview_remote",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"pull_token"},
				"properties": map[string]interface{}{
					"pull_token": map[string]interface{}{
						"type":        "string",
						"description": "Token from preview_remote response",
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
		// Lockfile management
		{
			Name:        "lock_dependencies",
			Description: "Generate a lockfile from currently installed remote items for reproducible installations",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "install_dependencies",
			Description: "Install all items from the lockfile",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Skip confirmation prompts (default: false)",
					},
				},
			},
		},
		{
			Name:        "check_outdated",
			Description: "Check if any locked items have newer versions available",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
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
	case "discover_remotes":
		result, err = s.toolDiscoverRemotes(ctx, params.Arguments)
	case "browse_remote":
		result, err = s.toolBrowseRemote(ctx, params.Arguments)
	case "preview_remote":
		result, err = s.toolPreviewRemote(ctx, params.Arguments)
	case "confirm_pull":
		result, err = s.toolConfirmPull(ctx, params.Arguments)
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
	case "lock_dependencies":
		result, err = s.toolLockDependencies(ctx, params.Arguments)
	case "install_dependencies":
		result, err = s.toolInstallDependencies(ctx, params.Arguments)
	case "check_outdated":
		result, err = s.toolCheckOutdated(ctx, params.Arguments)
	default:
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "Unknown tool: " + params.Name},
		}
	}

	if err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: mcpToolResult{
				Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}},
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

// containsTag checks if any tag contains the query string.
func containsTag(tags []string, query string) bool {
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

// sortContentInfos sorts content infos by the specified field and order.
func sortContentInfos(infos []bundles.ContentInfo, sortBy, sortOrder string) {
	if sortBy == "" {
		sortBy = "name"
	}
	reverse := sortOrder == "desc"

	switch sortBy {
	case "name":
		sortSlice(infos, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(infos[i].Name), strings.ToLower(infos[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "source":
		sortSlice(infos, func(i, j int) bool {
			cmp := strings.Compare(infos[i].Source, infos[j].Source)
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	}
}

// sortSlice is a helper that sorts a slice using a comparison function.
func sortSlice[T any](s []T, less func(i, j int) bool) {
	for i := 0; i < len(s)-1; i++ {
		for j := i + 1; j < len(s); j++ {
			if less(j, i) {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
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

func (s *mcpServer) toolPreviewRemote(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Reference string `json:"reference"`
		ItemType  string `json:"item_type"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.FetchRemoteContent(ctx, s.cfg, operations.FetchRemoteContentRequest{
		Reference: params.Reference,
		ItemType:  params.ItemType,
	})
	if err != nil {
		return nil, err
	}

	// Convert item_type string to remote.ItemType for storage
	var itemType remote.ItemType
	switch params.ItemType {
	case "bundle":
		itemType = remote.ItemTypeBundle
	case "profile":
		itemType = remote.ItemTypeProfile
	}

	// Store pending pull for confirm_pull
	s.pullMu.Lock()
	s.pendingPulls[result.PullToken] = &pendingPull{
		Reference: result.PullToken,
		ItemType:  itemType,
		Content:   []byte(result.Content),
		SHA:       result.FullSHA,
		RemoteURL: result.SourceURL,
	}
	s.pullMu.Unlock()

	return map[string]interface{}{
		"reference":  result.Reference,
		"item_type":  result.ItemType,
		"sha":        result.SHA,
		"full_sha":   result.FullSHA,
		"source_url": result.SourceURL,
		"file_path":  result.FilePath,
		"content":    result.Content,
		"pull_token": result.PullToken,
		"warning":    result.Warning,
	}, nil
}

func (s *mcpServer) toolConfirmPull(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		PullToken string `json:"pull_token"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.PullToken == "" {
		return nil, fmt.Errorf("pull_token is required")
	}

	// Get pending pull
	s.pullMu.Lock()
	pending, ok := s.pendingPulls[params.PullToken]
	if ok {
		delete(s.pendingPulls, params.PullToken)
	}
	s.pullMu.Unlock()

	if !ok {
		return nil, fmt.Errorf("invalid or expired pull_token: token must be obtained from preview_remote")
	}

	// Use operations.WriteRemoteItem which uses config's SCM path (the bug fix)
	result, err := operations.WriteRemoteItem(ctx, s.cfg, operations.WriteRemoteItemRequest{
		Reference: pending.Reference,
		ItemType:  string(pending.ItemType),
		Content:   pending.Content,
		SHA:       pending.SHA,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":      result.Status,
		"reference":   result.Reference,
		"item_type":   result.ItemType,
		"local_path":  result.LocalPath,
		"sha":         result.SHA,
		"overwritten": result.Overwritten,
	}, nil
}

// ============================================================================
// Profile management tools
// ============================================================================

func (s *mcpServer) toolCreateProfile(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Parents     []string `json:"parents"`
		Bundles []string `json:"bundles"`
		Tags    []string `json:"tags"`
		Default bool     `json:"default"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.CreateProfile(ctx, s.cfg, operations.CreateProfileRequest{
		Name:        params.Name,
		Description: params.Description,
		Parents:     params.Parents,
		Bundles:     params.Bundles,
		Tags:        params.Tags,
		Default:     params.Default,
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
		Name          string   `json:"name"`
		Description   *string  `json:"description"`
		AddParents    []string `json:"add_parents"`
		RemoveParents []string `json:"remove_parents"`
		AddBundles    []string `json:"add_bundles"`
		RemoveBundles []string `json:"remove_bundles"`
		AddTags       []string `json:"add_tags"`
		RemoveTags    []string `json:"remove_tags"`
		Default       *bool    `json:"default"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}

	result, err := operations.UpdateProfile(ctx, s.cfg, operations.UpdateProfileRequest{
		Name:          params.Name,
		Description:   params.Description,
		AddParents:    params.AddParents,
		RemoveParents: params.RemoveParents,
		AddBundles:    params.AddBundles,
		RemoveBundles: params.RemoveBundles,
		AddTags:       params.AddTags,
		RemoveTags:    params.RemoveTags,
		Default:       params.Default,
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
// Lockfile management tools
// ============================================================================

func (s *mcpServer) toolLockDependencies(ctx context.Context, args json.RawMessage) (interface{}, error) {
	return operations.LockDependencies(ctx, s.cfg, operations.LockDependenciesRequest{})
}

func (s *mcpServer) toolInstallDependencies(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Force bool `json:"force"`
	}
	_ = json.Unmarshal(args, &params)

	return operations.InstallDependencies(ctx, s.cfg, operations.InstallDependenciesRequest{
		Force: params.Force,
	})
}

func (s *mcpServer) toolCheckOutdated(ctx context.Context, args json.RawMessage) (interface{}, error) {
	return operations.CheckOutdated(ctx, s.cfg, operations.CheckOutdatedRequest{})
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

// ============================================================================
// Helper functions
// ============================================================================

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}
