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
	"syscall"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"mlcm/internal/config"
	"mlcm/internal/fragments"
	pb "mlcm/server/proto/fragmentspb"
)

var (
	mcpServerAddr string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server over stdio",
	Long: `Run mlcm as an MCP (Model Context Protocol) server over stdio.

This allows AI agents to interact with mlcm functionality using standard MCP tool calls.

Local tools (always available):
  - list_fragments: List available context fragments
  - get_fragment: Get a fragment's content by name
  - list_personas: List configured personas
  - get_persona: Get a persona's configuration
  - set_persona: Set the default persona for this session
  - assemble_context: Assemble context from persona/fragments/tags
  - list_prompts: List saved prompts
  - get_prompt: Get a prompt's content by name

Remote tools (when --addr is specified):
  - server_list_fragments: List fragments from remote server
  - server_get_fragment: Get a fragment by ID from remote server
  - server_search_fragments: Search fragments on remote server
  - server_create_fragment: Create a fragment on remote server
  - server_list_personas: List personas from remote server
  - server_get_persona: Get a persona by ID from remote server
  - server_create_persona: Create a persona on remote server

Examples:
  mlcm mcp                           # Local tools only
  mlcm mcp --addr localhost:50051    # Local + remote tools`,
	RunE: runMCPServer,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.Flags().StringVar(&mcpServerAddr, "addr", "", "Fragment server gRPC address (enables remote tools)")
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
		reader: bufio.NewReader(os.Stdin),
		writer: os.Stdout,
	}

	// Connect to remote server if address specified
	if mcpServerAddr != "" {
		conn, err := grpc.NewClient(mcpServerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to connect to fragment server: %v\n", err)
		} else {
			// Test connectivity with a simple call
			client := pb.NewFragmentServiceClient(conn)
			_, err := client.ListFragments(ctx, &pb.ListFragmentsRequest{PageSize: 1})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: fragment server not reachable: %v\n", err)
				conn.Close()
			} else {
				server.remoteClient = client
				server.remoteConn = conn
				defer conn.Close()
			}
		}
	}

	return server.run(ctx)
}

type mcpServer struct {
	reader         *bufio.Reader
	writer         io.Writer
	cfg            *config.Config
	remoteClient   pb.FragmentServiceClient
	remoteConn     *grpc.ClientConn
	sessionPersona string // Override persona for this session
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
	data, _ := json.Marshal(resp)
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
				"name":    "mlcm",
				"version": "1.0.0",
			},
		},
	}
}

func (s *mcpServer) handleToolsList(req *mcpRequest) *mcpResponse {
	tools := s.getLocalTools()
	if s.remoteClient != nil {
		tools = append(tools, s.getRemoteTools()...)
	}

	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"tools": tools,
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
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Filter by tags (optional)",
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
			Name:        "list_personas",
			Description: "List all configured personas with their descriptions",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "get_persona",
			Description: "Get a persona's configuration including fragments, tags, and variables",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Persona name",
					},
				},
			},
		},
		{
			Name:        "assemble_context",
			Description: "Assemble context from a persona, fragments, and/or tags. Returns the combined context that would be sent to an AI.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"persona": map[string]interface{}{
						"type":        "string",
						"description": "Persona name to use (optional)",
					},
					"fragments": map[string]interface{}{
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
				"type":       "object",
				"properties": map[string]interface{}{},
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
			Name:        "set_persona",
			Description: "Set the default persona for this session. Affects subsequent assemble_context calls.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Persona name to use as default",
					},
				},
			},
		},
	}
}

func (s *mcpServer) getRemoteTools() []mcpToolInfo {
	return []mcpToolInfo{
		{
			Name:        "server_list_fragments",
			Description: "List fragments from remote server with optional filtering",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tags":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Filter by tags"},
					"author":      map[string]interface{}{"type": "string", "description": "Filter by author"},
					"name_prefix": map[string]interface{}{"type": "string", "description": "Filter by name prefix"},
					"page_size":   map[string]interface{}{"type": "integer", "description": "Number of results per page"},
				},
			},
		},
		{
			Name:        "server_get_fragment",
			Description: "Get a fragment by ID from remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "Fragment ID"},
				},
			},
		},
		{
			Name:        "server_get_fragment_by_name",
			Description: "Get a fragment by author, name, and optional version from remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"author", "name"},
				"properties": map[string]interface{}{
					"author":  map[string]interface{}{"type": "string", "description": "Fragment author"},
					"name":    map[string]interface{}{"type": "string", "description": "Fragment name"},
					"version": map[string]interface{}{"type": "string", "description": "Fragment version (optional)"},
				},
			},
		},
		{
			Name:        "server_search_fragments",
			Description: "Search fragments by content on remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]interface{}{
					"query":     map[string]interface{}{"type": "string", "description": "Search query"},
					"tags":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Filter by tags"},
					"page_size": map[string]interface{}{"type": "integer", "description": "Number of results per page"},
				},
			},
		},
		{
			Name:        "server_create_fragment",
			Description: "Create a new fragment on remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "content"},
				"properties": map[string]interface{}{
					"name":      map[string]interface{}{"type": "string", "description": "Fragment name"},
					"version":   map[string]interface{}{"type": "string", "description": "Fragment version"},
					"author":    map[string]interface{}{"type": "string", "description": "Fragment author"},
					"tags":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Fragment tags"},
					"variables": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Fragment variables"},
					"content":   map[string]interface{}{"type": "string", "description": "Fragment content"},
				},
			},
		},
		{
			Name:        "server_download_fragment",
			Description: "Download a fragment from remote server (increments download count)",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "Fragment ID"},
				},
			},
		},
		{
			Name:        "server_list_personas",
			Description: "List personas from remote server",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"author":      map[string]interface{}{"type": "string", "description": "Filter by author"},
					"name_prefix": map[string]interface{}{"type": "string", "description": "Filter by name prefix"},
					"page_size":   map[string]interface{}{"type": "integer", "description": "Number of results per page"},
				},
			},
		},
		{
			Name:        "server_get_persona",
			Description: "Get a persona by ID from remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "Persona ID"},
				},
			},
		},
		{
			Name:        "server_get_persona_by_name",
			Description: "Get a persona by author, name, and optional version from remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"author", "name"},
				"properties": map[string]interface{}{
					"author":  map[string]interface{}{"type": "string", "description": "Persona author"},
					"name":    map[string]interface{}{"type": "string", "description": "Persona name"},
					"version": map[string]interface{}{"type": "string", "description": "Persona version (optional)"},
				},
			},
		},
		{
			Name:        "server_create_persona",
			Description: "Create a new persona on remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "author"},
				"properties": map[string]interface{}{
					"name":        map[string]interface{}{"type": "string", "description": "Persona name"},
					"author":      map[string]interface{}{"type": "string", "description": "Persona author"},
					"version":     map[string]interface{}{"type": "string", "description": "Persona version"},
					"description": map[string]interface{}{"type": "string", "description": "Persona description"},
					"fragments": map[string]interface{}{
						"type":        "array",
						"description": "List of fragment references",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":    map[string]interface{}{"type": "string"},
								"author":  map[string]interface{}{"type": "string"},
								"version": map[string]interface{}{"type": "string"},
							},
						},
					},
				},
			},
		},
		{
			Name:        "server_download_persona",
			Description: "Download a persona with resolved fragments from remote server",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "Persona ID"},
				},
			},
		},
	}
}

func (s *mcpServer) handleToolsCall(ctx context.Context, req *mcpRequest) *mcpResponse {
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

	// Local tools
	switch params.Name {
	case "list_fragments":
		result, err = s.toolListFragments(params.Arguments)
	case "get_fragment":
		result, err = s.toolGetFragment(params.Arguments)
	case "list_personas":
		result, err = s.toolListPersonas(params.Arguments)
	case "get_persona":
		result, err = s.toolGetPersona(params.Arguments)
	case "assemble_context":
		result, err = s.toolAssembleContext(params.Arguments)
	case "list_prompts":
		result, err = s.toolListPrompts(params.Arguments)
	case "get_prompt":
		result, err = s.toolGetPrompt(params.Arguments)
	case "set_persona":
		result, err = s.toolSetPersona(params.Arguments)
	// Remote tools
	case "server_list_fragments":
		result, err = s.remoteListFragments(ctx, params.Arguments)
	case "server_get_fragment":
		result, err = s.remoteGetFragment(ctx, params.Arguments)
	case "server_get_fragment_by_name":
		result, err = s.remoteGetFragmentByName(ctx, params.Arguments)
	case "server_search_fragments":
		result, err = s.remoteSearchFragments(ctx, params.Arguments)
	case "server_create_fragment":
		result, err = s.remoteCreateFragment(ctx, params.Arguments)
	case "server_download_fragment":
		result, err = s.remoteDownloadFragment(ctx, params.Arguments)
	case "server_list_personas":
		result, err = s.remoteListPersonas(ctx, params.Arguments)
	case "server_get_persona":
		result, err = s.remoteGetPersona(ctx, params.Arguments)
	case "server_get_persona_by_name":
		result, err = s.remoteGetPersonaByName(ctx, params.Arguments)
	case "server_create_persona":
		result, err = s.remoteCreatePersona(ctx, params.Arguments)
	case "server_download_persona":
		result, err = s.remoteDownloadPersona(ctx, params.Arguments)
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
// Local tool implementations
// ============================================================================

func (s *mcpServer) toolListFragments(args json.RawMessage) (interface{}, error) {
	var params struct {
		Tags []string `json:"tags"`
	}
	json.Unmarshal(args, &params)

	loader := fragments.NewLoader(s.cfg.GetFragmentDirs(),
		fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()),
		fragments.WithSuppressWarnings(true),
	)

	var infos []fragments.FragmentInfo
	var err error

	if len(params.Tags) > 0 {
		infos, err = loader.ListByTags(params.Tags)
	} else {
		infos, err = loader.List()
	}
	if err != nil {
		return nil, err
	}

	type fragmentEntry struct {
		Name   string   `json:"name"`
		Tags   []string `json:"tags,omitempty"`
		Source string   `json:"source"`
	}

	var result []fragmentEntry
	for _, info := range infos {
		result = append(result, fragmentEntry{
			Name:   info.Name,
			Tags:   info.Tags,
			Source: info.Source,
		})
	}

	return map[string]interface{}{
		"fragments": result,
		"count":     len(result),
	}, nil
}

func (s *mcpServer) toolGetFragment(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := fragments.NewLoader(s.cfg.GetFragmentDirs(),
		fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()),
		fragments.WithSuppressWarnings(true),
	)

	frag, err := loader.Load(params.Name)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":      params.Name,
		"tags":      frag.Tags,
		"variables": frag.Variables,
		"content":   frag.Content,
	}, nil
}

func (s *mcpServer) toolListPersonas(args json.RawMessage) (interface{}, error) {
	type personaEntry struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Tags        []string `json:"tags,omitempty"`
	}

	var result []personaEntry
	for name, persona := range s.cfg.Personas {
		result = append(result, personaEntry{
			Name:        name,
			Description: persona.Description,
			Tags:        persona.Tags,
		})
	}

	return map[string]interface{}{
		"personas": result,
		"count":    len(result),
		"default":  s.cfg.Defaults.Persona,
	}, nil
}

func (s *mcpServer) toolGetPersona(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	persona, exists := s.cfg.Personas[params.Name]
	if !exists {
		return nil, fmt.Errorf("persona not found: %s", params.Name)
	}

	return map[string]interface{}{
		"name":        params.Name,
		"description": persona.Description,
		"tags":        persona.Tags,
		"fragments":   persona.Fragments,
		"variables":   persona.Variables,
		"generators":  persona.Generators,
	}, nil
}

func (s *mcpServer) toolSetPersona(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Verify persona exists
	if _, exists := s.cfg.Personas[params.Name]; !exists {
		return nil, fmt.Errorf("persona not found: %s", params.Name)
	}

	s.sessionPersona = params.Name

	return map[string]interface{}{
		"persona": params.Name,
		"message": fmt.Sprintf("Session persona set to %q", params.Name),
	}, nil
}

func (s *mcpServer) toolAssembleContext(args json.RawMessage) (interface{}, error) {
	var params struct {
		Persona   string   `json:"persona"`
		Fragments []string `json:"fragments"`
		Tags      []string `json:"tags"`
	}
	json.Unmarshal(args, &params)

	loader := fragments.NewLoader(s.cfg.GetFragmentDirs(),
		fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()),
		fragments.WithSuppressWarnings(true),
	)

	var allFragments []string
	personaVars := make(map[string]string)

	personaName := params.Persona
	if personaName == "" && len(params.Fragments) == 0 && len(params.Tags) == 0 {
		// Use session persona if set, otherwise config default
		if s.sessionPersona != "" {
			personaName = s.sessionPersona
		} else {
			personaName = s.cfg.Defaults.Persona
		}
		allFragments = append(allFragments, s.cfg.Defaults.Fragments...)
	}

	if personaName != "" {
		persona, exists := s.cfg.Personas[personaName]
		if !exists {
			return nil, fmt.Errorf("persona not found: %s", personaName)
		}

		if len(persona.Tags) > 0 {
			taggedInfos, err := loader.ListByTags(persona.Tags)
			if err != nil {
				return nil, fmt.Errorf("failed to list fragments by persona tags: %w", err)
			}
			for _, info := range taggedInfos {
				allFragments = append(allFragments, info.Name)
			}
		}

		allFragments = append(allFragments, persona.Fragments...)
		for k, v := range persona.Variables {
			personaVars[k] = v
		}
	}

	allFragments = append(allFragments, params.Fragments...)

	if len(params.Tags) > 0 {
		taggedInfos, err := loader.ListByTags(params.Tags)
		if err != nil {
			return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
		}
		for _, info := range taggedInfos {
			allFragments = append(allFragments, info.Name)
		}
	}

	seen := make(map[string]bool)
	var uniqueFragments []string
	for _, f := range allFragments {
		if !seen[f] {
			seen[f] = true
			uniqueFragments = append(uniqueFragments, f)
		}
	}

	var contextContent string
	if len(uniqueFragments) > 0 {
		var err error
		contextContent, err = loader.LoadMultipleWithVars(uniqueFragments, personaVars)
		if err != nil {
			return nil, fmt.Errorf("failed to load fragments: %w", err)
		}
	}

	return map[string]interface{}{
		"persona":          personaName,
		"fragments_loaded": uniqueFragments,
		"context":          contextContent,
	}, nil
}

func (s *mcpServer) toolListPrompts(args json.RawMessage) (interface{}, error) {
	loader := fragments.NewLoader(s.cfg.GetPromptDirs(),
		fragments.WithPreferDistilled(false),
		fragments.WithSuppressWarnings(true),
	)

	prompts, err := loader.List()
	if err != nil {
		return nil, err
	}

	type promptEntry struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}

	var result []promptEntry
	for _, p := range prompts {
		result = append(result, promptEntry{
			Name:   p.Name,
			Source: p.Source,
		})
	}

	return map[string]interface{}{
		"prompts": result,
		"count":   len(result),
	}, nil
}

func (s *mcpServer) toolGetPrompt(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := fragments.NewLoader(s.cfg.GetPromptDirs(),
		fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()),
		fragments.WithSuppressWarnings(true),
	)

	prompt, err := loader.Load(params.Name)
	if err != nil {
		return nil, err
	}

	content := prompt.Content
	lines := strings.Split(content, "\n")
	var cleanedLines []string
	skipHeader := true
	for _, line := range lines {
		if skipHeader && strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		skipHeader = false
		cleanedLines = append(cleanedLines, line)
	}
	content = strings.TrimSpace(strings.Join(cleanedLines, "\n"))

	return map[string]interface{}{
		"name":    params.Name,
		"content": content,
	}, nil
}

// ============================================================================
// Remote tool implementations (gRPC)
// ============================================================================

func (s *mcpServer) checkRemote() error {
	if s.remoteClient == nil {
		return fmt.Errorf("remote server not connected (use --addr flag)")
	}
	return nil
}

func (s *mcpServer) remoteListFragments(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Tags       []string `json:"tags"`
		Author     string   `json:"author"`
		NamePrefix string   `json:"name_prefix"`
		PageSize   int32    `json:"page_size"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.ListFragments(ctx, &pb.ListFragmentsRequest{
		Tags:       params.Tags,
		Author:     params.Author,
		NamePrefix: params.NamePrefix,
		PageSize:   params.PageSize,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteGetFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		ID string `json:"id"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.GetFragment(ctx, &pb.GetFragmentRequest{Id: params.ID})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteGetFragmentByName(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Author  string `json:"author"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.GetFragmentByName(ctx, &pb.GetFragmentByNameRequest{
		Author:  params.Author,
		Name:    params.Name,
		Version: params.Version,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteSearchFragments(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Query    string   `json:"query"`
		Tags     []string `json:"tags"`
		PageSize int32    `json:"page_size"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.SearchFragments(ctx, &pb.SearchFragmentsRequest{
		Query:    params.Query,
		Tags:     params.Tags,
		PageSize: params.PageSize,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteCreateFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Name      string   `json:"name"`
		Version   string   `json:"version"`
		Author    string   `json:"author"`
		Tags      []string `json:"tags"`
		Variables []string `json:"variables"`
		Content   string   `json:"content"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.CreateFragment(ctx, &pb.CreateFragmentRequest{
		Name:      params.Name,
		Version:   params.Version,
		Author:    params.Author,
		Tags:      params.Tags,
		Variables: params.Variables,
		Content:   params.Content,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteDownloadFragment(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		ID string `json:"id"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.DownloadFragment(ctx, &pb.DownloadFragmentRequest{Id: params.ID})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteListPersonas(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Author     string `json:"author"`
		NamePrefix string `json:"name_prefix"`
		PageSize   int32  `json:"page_size"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.ListPersonas(ctx, &pb.ListPersonasRequest{
		Author:     params.Author,
		NamePrefix: params.NamePrefix,
		PageSize:   params.PageSize,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteGetPersona(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		ID string `json:"id"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.GetPersona(ctx, &pb.GetPersonaRequest{Id: params.ID})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteGetPersonaByName(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Author  string `json:"author"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.GetPersonaByName(ctx, &pb.GetPersonaByNameRequest{
		Author:  params.Author,
		Name:    params.Name,
		Version: params.Version,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteCreatePersona(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		Name        string `json:"name"`
		Author      string `json:"author"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Fragments   []struct {
			Name    string `json:"name"`
			Author  string `json:"author"`
			Version string `json:"version"`
		} `json:"fragments"`
	}
	json.Unmarshal(args, &params)

	var fragmentRefs []*pb.FragmentRef
	for _, f := range params.Fragments {
		fragmentRefs = append(fragmentRefs, &pb.FragmentRef{
			Name:    f.Name,
			Author:  f.Author,
			Version: f.Version,
		})
	}

	resp, err := s.remoteClient.CreatePersona(ctx, &pb.CreatePersonaRequest{
		Name:        params.Name,
		Author:      params.Author,
		Version:     params.Version,
		Description: params.Description,
		Fragments:   fragmentRefs,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (s *mcpServer) remoteDownloadPersona(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if err := s.checkRemote(); err != nil {
		return nil, err
	}

	var params struct {
		ID string `json:"id"`
	}
	json.Unmarshal(args, &params)

	resp, err := s.remoteClient.DownloadPersona(ctx, &pb.DownloadPersonaRequest{Id: params.ID})
	if err != nil {
		return nil, err
	}

	return resp, nil
}
