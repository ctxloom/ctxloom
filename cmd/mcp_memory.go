//go:build memory

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
	"github.com/SophisticatedContextManager/scm/internal/memory"
)

// getMemoryTools returns MCP tool definitions for memory features.
func (s *mcpServer) getMemoryTools() []mcpToolInfo {
	tools := []mcpToolInfo{
		{
			Name:        "compact_session",
			Description: "Compact current or specified session log into a distilled summary. Use this to compress a session log when context is running low.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{
						"type":        "string",
						"description": "Session ID to compact (optional, defaults to current session)",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "LLM model to use for distillation (optional, defaults to config or claude-3-haiku)",
					},
					"backend": map[string]interface{}{
						"type":        "string",
						"description": "Backend to read session from (optional, defaults to claude-code)",
					},
				},
			},
		},
		{
			Name:        "get_session_memory",
			Description: "Get the distilled memory from a previous session that was loaded at startup",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "list_sessions",
			Description: "List all sessions from the backend with their compaction status",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"backend": map[string]interface{}{
						"type":        "string",
						"description": "Backend to list sessions from (optional, defaults to claude-code)",
					},
				},
			},
		},
	}

	// Add vector search tool (available when built with vectors tag)
	tools = append(tools, getVectorTools()...)

	return tools
}

// toolCompactSession handles the compact_session MCP tool.
func (s *mcpServer) toolCompactSession(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
		Backend   string `json:"backend"`
	}
	_ = json.Unmarshal(args, &params)

	// Determine plugin and model for distillation
	plugin := s.cfg.Memory.GetCompactionPlugin()
	model := params.Model
	if model == "" {
		model = s.cfg.Memory.GetCompactionModel()
	}

	// Determine backend to read session from
	backend := params.Backend
	if backend == "" {
		backend = s.cfg.LM.GetDefaultPlugin()
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	memoryDir := filepath.Join(s.cfg.SCMDir, "memory")

	// Create compactor
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    plugin,
		Model:     model,
		Backend:   backend,
		ChunkSize: s.cfg.Memory.GetChunkSize(),
		SessionID: params.SessionID,
		WorkDir:   workDir,
		OutputDir: memoryDir,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}

	// Run compaction
	result, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("compaction failed: %w", err)
	}

	return map[string]interface{}{
		"session_id":       result.SessionID,
		"chunks_processed": result.ChunksCreated,
		"tokens_in":        result.TotalTokensIn,
		"tokens_out":       result.TotalTokensOut,
		"reduction":        fmt.Sprintf("%.0f%%", 100*(1-float64(result.TotalTokensOut)/float64(result.TotalTokensIn))),
		"duration":         result.Duration.String(),
		"output_path":      result.DistilledPath,
	}, nil
}

// toolGetSessionMemory returns the loaded session memory.
func (s *mcpServer) toolGetSessionMemory(_ context.Context, _ json.RawMessage) (interface{}, error) {
	if s.sessionMemory == "" {
		return map[string]interface{}{
			"loaded":  false,
			"message": "No session memory loaded. Enable memory.load_on_start in config and ensure distilled sessions exist.",
		}, nil
	}

	return map[string]interface{}{
		"loaded":  true,
		"content": s.sessionMemory,
		"chars":   len(s.sessionMemory),
	}, nil
}

// toolListSessions returns all sessions from the backend.
func (s *mcpServer) toolListSessions(_ context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Backend string `json:"backend"`
	}
	_ = json.Unmarshal(args, &params)

	// Determine backend
	backendName := params.Backend
	if backendName == "" {
		backendName = s.cfg.LM.GetDefaultPlugin()
	}

	backend := backends.Get(backendName)
	if backend == nil {
		return nil, fmt.Errorf("unknown backend: %s", backendName)
	}

	history := backend.History()
	if history == nil {
		return nil, fmt.Errorf("backend %q does not support session history", backendName)
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	sessions, err := history.ListSessions(workDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	// Check which sessions have been compacted
	memoryDir := filepath.Join(s.cfg.SCMDir, "memory")
	distilled, err := memory.ListDistilledSessions(memoryDir)
	if err != nil {
		distilled = nil // Non-fatal
	}
	distilledSet := make(map[string]bool)
	for _, id := range distilled {
		distilledSet[id] = true
	}

	type sessionInfo struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Entries  int    `json:"entries,omitempty"`
		Started  string `json:"started,omitempty"`
	}

	var result []sessionInfo
	for _, meta := range sessions {
		info := sessionInfo{
			ID:      meta.ID,
			Status:  "pending",
			Entries: meta.EntryCount,
		}

		if !meta.StartTime.IsZero() {
			info.Started = meta.StartTime.Format("2006-01-02 15:04")
		}

		if distilledSet[meta.ID] {
			info.Status = "compacted"
		}

		result = append(result, info)
	}

	return map[string]interface{}{
		"sessions": result,
		"count":    len(result),
		"backend":  backendName,
	}, nil
}
