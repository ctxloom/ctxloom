package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/memory"
)

// getMemoryDir returns the path to the memory directory.
// Falls back to ".ctxloom/ephemeral/memory" in the current directory if AppDir is not set.
func (s *mcpServer) getMemoryDir() string {
	if s.cfg.AppDir != "" {
		return filepath.Join(s.cfg.AppDir, "ephemeral", "memory")
	}
	// Fallback to current working directory
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".ctxloom", "ephemeral", "memory")
	}
	return filepath.Join(".ctxloom", "ephemeral", "memory")
}

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
		{
			Name:        "load_session",
			Description: "Distill and load context from a session. Use browse_session_history to find session IDs.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{
						"type":        "string",
						"description": "Session ID to load (use browse_session_history or list_sessions to find IDs)",
					},
					"backend": map[string]interface{}{
						"type":        "string",
						"description": "Backend to read session from (optional, defaults to claude-code)",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "LLM model to use for distillation if needed (optional)",
					},
				},
			},
		},
	}
	tools = append(tools, mcpToolInfo{
		Name:        "recover_session",
		Description: "Recover context from the current session after /clear. Uses the stable process ID from CTXLOOM_STAMP to find the previous session, or falls back to the most recent session.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_id": map[string]interface{}{
					"type":        "string",
					"description": "Session ID to recover. If not provided, uses most recent session.",
				},
				"backend": map[string]interface{}{
					"type":        "string",
					"description": "Backend to read session from (optional, defaults to claude-code)",
				},
				"model": map[string]interface{}{
					"type":        "string",
					"description": "LLM model to use for distillation if needed (optional)",
				},
			},
		},
	})
	tools = append(tools, mcpToolInfo{
		Name:        "get_previous_session",
		Description: "Get the previous session's distilled content by looking up the session registry. Use this to recover context from before /clear.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"model": map[string]interface{}{
					"type":        "string",
					"description": "LLM model to use for distillation if needed (optional)",
				},
			},
		},
	})
	tools = append(tools, mcpToolInfo{
		Name:        "browse_session_history",
		Description: "Browse recent sessions with AI-generated summaries. Shows sessions from the last 3 days with a brief essence of each. Use this to find and load a specific session.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"backend": map[string]interface{}{
					"type":        "string",
					"description": "Backend to list sessions from (optional, defaults to claude-code)",
				},
			},
		},
	})

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
	plugin := s.cfg.GetCompactionPlugin()
	model := params.Model
	if model == "" {
		model = s.cfg.GetCompactionModel()
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

	memoryDir := s.getMemoryDir()

	// Create compactor
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    plugin,
		Model:     model,
		Backend:   backend,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
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
	memoryDir := s.getMemoryDir()
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

// toolLoadSession distills and loads a session's content.
func (s *mcpServer) toolLoadSession(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		SessionID string `json:"session_id"`
		Backend   string `json:"backend"`
		Model     string `json:"model"`
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

	if params.SessionID == "" {
		return nil, fmt.Errorf("session_id is required. Use browse_session_history or list_sessions to find session IDs")
	}

	targetSessionID := params.SessionID

	// Check if target session has content
	session, err := history.GetSession(workDir, targetSessionID)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", targetSessionID, err)
	}
	if len(session.Entries) == 0 {
		return map[string]interface{}{
			"loaded":  false,
			"message": fmt.Sprintf("Session %s appears to be empty.", targetSessionID),
		}, nil
	}

	memoryDir := s.getMemoryDir()

	// Check if already distilled (cached)
	distilled, err := memory.LoadDistilledSession(memoryDir, targetSessionID)
	if err == nil {
		// Cached - return immediately
		return map[string]interface{}{
			"loaded":     true,
			"session_id": distilled.SessionID,
			"content":    distilled.Content,
			"was_cached": true,
			"tokens":     distilled.TokenCount,
			"created_at": distilled.CreatedAt.Format("2006-01-02 15:04:05"),
		}, nil
	}

	// Not cached - need to distill on-the-fly
	fmt.Fprintf(os.Stderr, "ctxloom: distilling session %s (this may take a moment)...\n", targetSessionID)

	// Determine model for distillation
	model := params.Model
	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    s.cfg.GetCompactionPlugin(),
		Model:     model,
		Backend:   backendName,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: targetSessionID,
		WorkDir:   workDir,
		OutputDir: memoryDir,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}

	compactResult, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("distillation failed: %w", err)
	}

	// Load the freshly distilled content
	distilled, err = memory.LoadDistilledSession(memoryDir, targetSessionID)
	if err != nil {
		return nil, fmt.Errorf("load distilled result: %w", err)
	}

	return map[string]interface{}{
		"loaded":     true,
		"session_id": compactResult.SessionID,
		"content":    distilled.Content,
		"was_cached": false,
		"duration":   compactResult.Duration.String(),
		"tokens_in":  compactResult.TotalTokensIn,
		"tokens_out": compactResult.TotalTokensOut,
		"reduction":  fmt.Sprintf("%.0f%%", 100*(1-float64(compactResult.TotalTokensOut)/float64(compactResult.TotalTokensIn))),
	}, nil
}

// toolRecoverSession recovers context from the current session after /clear.
// Uses the provided session_id or falls back to sessions[0] (most recent).
func (s *mcpServer) toolRecoverSession(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		SessionID string `json:"session_id"`
		Backend   string `json:"backend"`
		Model     string `json:"model"`
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

	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	// Use provided session_id or fall back to sessions[0]
	targetSessionID := params.SessionID
	if targetSessionID == "" {
		sessions, err := history.ListSessions(workDir)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		if len(sessions) == 0 {
			return map[string]interface{}{
				"loaded":  false,
				"message": "No sessions found.",
			}, nil
		}
		targetSessionID = sessions[0].ID
	}

	// Delegate to load_session logic
	loadArgs, _ := json.Marshal(map[string]string{
		"session_id": targetSessionID,
		"backend":    backendName,
		"model":      params.Model,
	})
	return s.toolLoadSession(ctx, loadArgs)
}

// toolBrowseSessionHistory lists recent sessions with AI-generated essences.
// Shows sessions from the last 3 days with brief summaries to help users find and load sessions.
func (s *mcpServer) toolBrowseSessionHistory(ctx context.Context, args json.RawMessage) (interface{}, error) {
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

	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	sessions, err := history.ListSessions(workDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	// Filter to last 3 days
	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	var recentSessions []backends.SessionMeta
	for _, meta := range sessions {
		if meta.StartTime.After(threeDaysAgo) {
			recentSessions = append(recentSessions, meta)
		}
	}

	if len(recentSessions) == 0 {
		return map[string]interface{}{
			"sessions": []interface{}{},
			"count":    0,
			"message":  "No sessions found in the last 3 days.",
		}, nil
	}

	memoryDir := s.getMemoryDir()
	model := s.cfg.GetCompactionModel()
	plugin := s.cfg.GetCompactionPlugin()

	type sessionWithEssence struct {
		ID        string `json:"id"`
		Started   string `json:"started"`
		Entries   int    `json:"entries"`
		Essence   string `json:"essence"`
		WasCached bool   `json:"was_cached"`
	}

	var results []sessionWithEssence
	for _, meta := range recentSessions {
		// Get the full session to generate essence
		session, err := history.GetSession(workDir, meta.ID)
		if err != nil {
			results = append(results, sessionWithEssence{
				ID:      meta.ID,
				Started: meta.StartTime.Format("2006-01-02 15:04"),
				Entries: meta.EntryCount,
				Essence: "(failed to load session)",
			})
			continue
		}

		// Generate or load cached essence
		essence, err := memory.GenerateSessionEssence(ctx, session, memory.EssenceConfig{
			Plugin:    plugin,
			Model:     model,
			MemoryDir: memoryDir,
		})

		if err != nil {
			results = append(results, sessionWithEssence{
				ID:      meta.ID,
				Started: meta.StartTime.Format("2006-01-02 15:04"),
				Entries: meta.EntryCount,
				Essence: fmt.Sprintf("(failed to generate essence: %v)", err),
			})
			continue
		}

		// Check if it was cached by comparing generated time
		wasCached := essence.GeneratedAt.Before(time.Now().Add(-1 * time.Second))

		results = append(results, sessionWithEssence{
			ID:        meta.ID,
			Started:   meta.StartTime.Format("2006-01-02 15:04"),
			Entries:   meta.EntryCount,
			Essence:   essence.Essence,
			WasCached: wasCached,
		})
	}

	return map[string]interface{}{
		"sessions": results,
		"count":    len(results),
		"message":  fmt.Sprintf("Found %d sessions from the last 3 days. Use load_session with a session_id to load one.", len(results)),
	}, nil
}

// toolGetPreviousSession finds the previous session using History.
// Returns the distilled content of the session before the current one.
func (s *mcpServer) toolGetPreviousSession(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(args, &params)

	// Get the ctxloom wrapper PID (ctxloom run/init) which is stable across /clear
	pid := findCtxloomWrapperPID()

	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	// Look up previous session using the configured default backend
	backendName := s.cfg.LM.GetDefaultPlugin()
	if backendName == "" {
		backendName = "claude-code"
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", backendName)
	}
	history := backend.History()
	if history == nil {
		return nil, fmt.Errorf("session history not available for backend %q", backendName)
	}

	prevSession, err := history.GetPreviousSession(workDir, pid)
	if err != nil {
		return nil, fmt.Errorf("lookup previous session: %w", err)
	}

	if prevSession == nil {
		return map[string]interface{}{
			"loaded":  false,
			"message": fmt.Sprintf("No previous session found for PID %d.", pid),
			"pid":     pid,
		}, nil
	}

	prevSessionID := prevSession.ID
	memoryDir := s.getMemoryDir()

	// Check if already distilled
	distilled, err := memory.LoadDistilledSession(memoryDir, prevSessionID)
	if err == nil {
		return map[string]interface{}{
			"loaded":     true,
			"session_id": distilled.SessionID,
			"content":    distilled.Content,
			"was_cached": true,
			"tokens":     distilled.TokenCount,
			"pid":        pid,
		}, nil
	}

	// Distill on demand
	fmt.Fprintf(os.Stderr, "ctxloom: distilling previous session %s for PID %d...\n", prevSessionID, pid)

	model := params.Model
	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    s.cfg.GetCompactionPlugin(),
		Model:     model,
		Backend:   backendName,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: prevSessionID,
		WorkDir:   workDir,
		OutputDir: memoryDir,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}

	compactResult, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("distillation failed: %w", err)
	}

	// Load the freshly distilled content
	distilled, err = memory.LoadDistilledSession(memoryDir, prevSessionID)
	if err != nil {
		return nil, fmt.Errorf("load distilled result: %w", err)
	}

	return map[string]interface{}{
		"loaded":     true,
		"session_id": compactResult.SessionID,
		"content":    distilled.Content,
		"was_cached": false,
		"duration":   compactResult.Duration.String(),
		"tokens_in":  compactResult.TotalTokensIn,
		"tokens_out": compactResult.TotalTokensOut,
		"pid":        pid,
	}, nil
}
