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

	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/fragments"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server over stdio",
	Long: `Run scm as an MCP (Model Context Protocol) server over stdio.

This allows AI agents to interact with scm functionality using standard MCP tool calls.

Available tools:
  - list_fragments: List available context fragments
  - get_fragment: Get a fragment's content by name
  - list_profiles: List configured profiles
  - get_profile: Get a profile's configuration
  - set_profile: Set the default profile for this session
  - assemble_context: Assemble context from profile/fragments/tags
  - list_prompts: List saved prompts
  - get_prompt: Get a prompt's content by name

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
		reader: bufio.NewReader(os.Stdin),
		writer: os.Stdout,
	}

	return server.run(ctx)
}

type mcpServer struct {
	reader         *bufio.Reader
	writer         io.Writer
	cfg            *config.Config
	sessionProfile string // Override profile for this session
}

// fragmentLoader returns a fragment loader configured for the current config source.
func (s *mcpServer) fragmentLoader(opts ...fragments.LoaderOption) *fragments.Loader {
	allOpts := []fragments.LoaderOption{
		fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()),
		fragments.WithSuppressWarnings(true),
	}
	if s.cfg.IsEmbedded() {
		allOpts = append(allOpts, fragments.WithFS(s.cfg.GetFragmentFS()))
	}
	allOpts = append(allOpts, opts...)
	return fragments.NewLoader(s.cfg.GetFragmentDirs(), allOpts...)
}

// promptLoader returns a prompt loader configured for the current config source.
func (s *mcpServer) promptLoader(opts ...fragments.LoaderOption) *fragments.Loader {
	allOpts := []fragments.LoaderOption{
		fragments.WithSuppressWarnings(true),
	}
	if s.cfg.IsEmbedded() {
		allOpts = append(allOpts, fragments.WithFS(s.cfg.GetPromptFS()))
	}
	allOpts = append(allOpts, opts...)
	return fragments.NewLoader(s.cfg.GetPromptDirs(), allOpts...)
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
			Name:        "list_profiles",
			Description: "List all configured profiles with their descriptions",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
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
			Name:        "set_profile",
			Description: "Set the default profile for this session. Affects subsequent assemble_context calls.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Profile name to use as default",
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
	case "list_profiles":
		result, err = s.toolListProfiles(params.Arguments)
	case "get_profile":
		result, err = s.toolGetProfile(params.Arguments)
	case "assemble_context":
		result, err = s.toolAssembleContext(params.Arguments)
	case "list_prompts":
		result, err = s.toolListPrompts(params.Arguments)
	case "get_prompt":
		result, err = s.toolGetPrompt(params.Arguments)
	case "set_profile":
		result, err = s.toolSetProfile(params.Arguments)
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
	// Unmarshal errors are non-fatal - use defaults for optional params
	_ = json.Unmarshal(args, &params)

	loader := s.fragmentLoader()

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

	loader := s.fragmentLoader()

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

func (s *mcpServer) toolListProfiles(args json.RawMessage) (interface{}, error) {
	type profileEntry struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Tags        []string `json:"tags,omitempty"`
	}

	var result []profileEntry
	for name, profile := range s.cfg.Profiles {
		result = append(result, profileEntry{
			Name:        name,
			Description: profile.Description,
			Tags:        profile.Tags,
		})
	}

	return map[string]interface{}{
		"profiles": result,
		"count":    len(result),
		"defaults": s.cfg.Defaults.Profiles,
	}, nil
}

func (s *mcpServer) toolGetProfile(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	profile, exists := s.cfg.Profiles[params.Name]
	if !exists {
		return nil, fmt.Errorf("profile not found: %s", params.Name)
	}

	return map[string]interface{}{
		"name":        params.Name,
		"description": profile.Description,
		"parents":     profile.Parents,
		"tags":        profile.Tags,
		"fragments":   profile.Fragments,
		"variables":   profile.Variables,
		"generators":  profile.Generators,
	}, nil
}

func (s *mcpServer) toolSetProfile(args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Verify profile exists
	if _, exists := s.cfg.Profiles[params.Name]; !exists {
		return nil, fmt.Errorf("profile not found: %s", params.Name)
	}

	s.sessionProfile = params.Name

	return map[string]interface{}{
		"profile": params.Name,
		"message": fmt.Sprintf("Session profile set to %q", params.Name),
	}, nil
}

func (s *mcpServer) toolAssembleContext(args json.RawMessage) (interface{}, error) {
	var params struct {
		Profile   string   `json:"profile"`
		Fragments []string `json:"fragments"`
		Tags      []string `json:"tags"`
	}
	// Unmarshal errors are non-fatal - use defaults for optional params
	_ = json.Unmarshal(args, &params)

	loader := s.fragmentLoader()

	var allFragments []string
	profileVars := make(map[string]string)

	profileName := params.Profile
	var profileNames []string
	if profileName == "" && len(params.Fragments) == 0 && len(params.Tags) == 0 {
		// Use session profile if set, otherwise config defaults
		if s.sessionProfile != "" {
			profileNames = []string{s.sessionProfile}
		} else {
			profileNames = s.cfg.Defaults.Profiles
		}
		allFragments = append(allFragments, s.cfg.Defaults.Fragments...)
	} else if profileName != "" {
		profileNames = []string{profileName}
	}

	// Process all profiles
	for _, pName := range profileNames {
		// Resolve profile with inheritance
		profile, err := config.ResolveProfile(s.cfg.Profiles, pName)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve profile %s: %w", pName, err)
		}

		if len(profile.Tags) > 0 {
			taggedInfos, err := loader.ListByTags(profile.Tags)
			if err != nil {
				return nil, fmt.Errorf("failed to list fragments by profile tags: %w", err)
			}
			for _, info := range taggedInfos {
				allFragments = append(allFragments, info.Name)
			}
		}

		allFragments = append(allFragments, profile.Fragments...)
		for k, v := range profile.Variables {
			profileVars[k] = v
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

	seen := collections.NewSet[string]()
	var uniqueFragments []string
	for _, f := range allFragments {
		if !seen.Has(f) {
			seen.Add(f)
			uniqueFragments = append(uniqueFragments, f)
		}
	}

	var contextContent string
	if len(uniqueFragments) > 0 {
		var err error
		contextContent, err = loader.LoadMultipleWithVars(uniqueFragments, profileVars)
		if err != nil {
			return nil, fmt.Errorf("failed to load fragments: %w", err)
		}
	}

	return map[string]interface{}{
		"profiles":         profileNames,
		"fragments_loaded": uniqueFragments,
		"context":          contextContent,
	}, nil
}

func (s *mcpServer) toolListPrompts(args json.RawMessage) (interface{}, error) {
	loader := s.promptLoader(fragments.WithPreferDistilled(false))

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

	loader := s.promptLoader(fragments.WithPreferDistilled(s.cfg.Defaults.ShouldUseDistilled()))

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
