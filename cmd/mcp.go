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

	"mlcm/internal/config"
	"mlcm/internal/fragments"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server over stdio",
	Long: `Run mlcm as an MCP (Model Context Protocol) server over stdio.

This allows AI agents to interact with mlcm functionality using standard MCP tool calls.

Available tools:
  - list_fragments: List available context fragments
  - get_fragment: Get a fragment's content by name
  - list_personas: List configured personas
  - get_persona: Get a persona's configuration
  - set_persona: Set the default persona for this session
  - assemble_context: Assemble context from persona/fragments/tags
  - list_prompts: List saved prompts
  - get_prompt: Get a prompt's content by name

Example:
  mlcm mcp`,
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
		reader: bufio.NewReader(os.Stdin),
		writer: os.Stdout,
	}

	return server.run(ctx)
}

type mcpServer struct {
	reader         *bufio.Reader
	writer         io.Writer
	cfg            *config.Config
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
		"defaults": s.cfg.Defaults.Personas,
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
		"parents":     persona.Parents,
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
	var personaNames []string
	if personaName == "" && len(params.Fragments) == 0 && len(params.Tags) == 0 {
		// Use session persona if set, otherwise config defaults
		if s.sessionPersona != "" {
			personaNames = []string{s.sessionPersona}
		} else {
			personaNames = s.cfg.Defaults.Personas
		}
		allFragments = append(allFragments, s.cfg.Defaults.Fragments...)
	} else if personaName != "" {
		personaNames = []string{personaName}
	}

	// Process all personas
	for _, pName := range personaNames {
		// Resolve persona with inheritance
		persona, err := config.ResolvePersona(s.cfg.Personas, pName)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve persona %s: %w", pName, err)
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
		"personas":         personaNames,
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
