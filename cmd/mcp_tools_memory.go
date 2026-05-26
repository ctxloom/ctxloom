package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// Memory-tool input types. All session-targeting tools accept an optional
// session_id and an optional backend override. The defaults come from cfg
// (cfg.LM.GetDefaultPlugin() for backend, current session for ID).

type compactSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to compact (defaults to current session)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation (defaults to config or claude-3-haiku)"`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to claude-code)"`
}

type listSessionsInput struct {
	Backend string `json:"backend,omitempty" jsonschema:"Backend to list sessions from (defaults to claude-code)"`
}

type loadSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Backend-native session ID (UUID). Either session_id or harp_name is required."`
	HarpName  string `json:"harp_name,omitempty" jsonschema:"Harp-named session reference (e.g. \"swift-amber-falcon\") from ~/.ctxloom/sessions/index.yaml. Resolved to a session_id via the index; if both are passed, harp_name wins."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to claude-code)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

type recoverSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to recover. If not provided, uses most recent session."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to claude-code)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

type browseSessionHistoryInput struct {
	Backend string `json:"backend,omitempty" jsonschema:"Backend to list sessions from (defaults to claude-code)"`
}

type getPreviousSessionInput struct {
	Model string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

// Result types. Each handler returns a different shape, so they each have
// a dedicated struct rather than sharing a generic "memory response" type.

type compactSessionResult struct {
	SessionID       string `json:"session_id"`
	ChunksProcessed int    `json:"chunks_processed"`
	TokensIn        int    `json:"tokens_in"`
	TokensOut       int    `json:"tokens_out"`
	Reduction       string `json:"reduction"`
	Duration        string `json:"duration"`
	OutputPath      string `json:"output_path"`
}

type sessionInfo struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Entries int    `json:"entries,omitempty"`
	Started string `json:"started,omitempty"`
}

type listSessionsResult struct {
	Sessions []sessionInfo `json:"sessions"`
	Count    int           `json:"count"`
	Backend  string        `json:"backend"`
}

// loadSessionResult covers both the "loaded" success shape and the
// "empty session" / "not found" shapes. The fields that only appear on
// success use omitempty so the wire format stays compatible with the
// legacy map-based response.
type loadSessionResult struct {
	Loaded     bool   `json:"loaded"`
	Message    string `json:"message,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Content    string `json:"content,omitempty"`
	WasCached  bool   `json:"was_cached,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	TokensIn   int    `json:"tokens_in,omitempty"`
	TokensOut  int    `json:"tokens_out,omitempty"`
	Reduction  string `json:"reduction,omitempty"`
	Duration   string `json:"duration,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	PID        int    `json:"pid,omitempty"`
}

type sessionWithEssence struct {
	ID        string `json:"id"`
	Started   string `json:"started"`
	Entries   int    `json:"entries"`
	Essence   string `json:"essence"`
	WasCached bool   `json:"was_cached"`
}

type browseSessionHistoryResult struct {
	Sessions []sessionWithEssence `json:"sessions"`
	Count    int                  `json:"count"`
	Message  string               `json:"message,omitempty"`
}

// getSessionsDir returns the directory holding distilled session .md files.
// Persistent (not ephemeral) because distilled summaries are a per-repo cache
// the user benefits from across restarts.
func (s *ctxServer) getSessionsDir() string {
	if s.cfg.AppDir != "" {
		return filepath.Join(s.cfg.AppDir, "sessions")
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".ctxloom", "sessions")
	}
	return filepath.Join(".ctxloom", "sessions")
}

// getMemoryDir is the legacy ephemeral memory dir, used only by the
// session-essence cache (browse_session_history). Removed once that
// path is replaced by reading .md headlines directly.
func (s *ctxServer) getMemoryDir() string {
	if s.cfg.AppDir != "" {
		return filepath.Join(s.cfg.AppDir, "ephemeral", "memory")
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".ctxloom", "ephemeral", "memory")
	}
	return filepath.Join(".ctxloom", "ephemeral", "memory")
}

func (s *ctxServer) registerMemoryTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "compact_session",
			Description: "Compact current or specified session log into a distilled summary. Use this to compress a session log when context is running low.",
		},
		s.handleCompactSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_sessions",
			Description: "List all sessions from the backend with their compaction status",
		},
		s.handleListSessions)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "load_session",
			Description: "Distill and load context from a session. Use browse_session_history to find session IDs.",
		},
		s.handleLoadSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "recover_session",
			Description: "Recover context from the current session after /clear. Uses the stable process ID from CTXLOOM_STAMP to find the previous session, or falls back to the most recent session.",
		},
		s.handleRecoverSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "browse_session_history",
			Description: "Browse recent sessions with AI-generated summaries. Shows sessions from the last 3 days with a brief essence of each. Use this to find and load a specific session.",
		},
		s.handleBrowseSessionHistory)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_previous_session",
			Description: "Get the previous session's distilled content by looking up the session registry. Use this to recover context from before /clear.",
		},
		s.handleGetPreviousSession)
}

func (s *ctxServer) handleCompactSession(ctx context.Context, _ *mcp.CallToolRequest, in compactSessionInput) (*mcp.CallToolResult, *compactSessionResult, error) {
	plugin := s.cfg.GetCompactionPlugin()
	model := in.Model
	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	backend := in.Backend
	if backend == "" {
		backend = s.cfg.LM.GetDefaultPlugin()
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	sessionsDir := s.getSessionsDir()

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    plugin,
		Model:     model,
		Backend:   backend,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: in.SessionID,
		WorkDir:   workDir,
		OutputDir: sessionsDir,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create compactor: %w", err)
	}

	result, err := compactor.Compact(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("compaction failed: %w", err)
	}

	return nil, &compactSessionResult{
		SessionID:       result.SessionID,
		ChunksProcessed: result.ChunksCreated,
		TokensIn:        result.TotalTokensIn,
		TokensOut:       result.TotalTokensOut,
		Reduction:       fmt.Sprintf("%.0f%%", 100*(1-float64(result.TotalTokensOut)/float64(result.TotalTokensIn))),
		Duration:        result.Duration.String(),
		OutputPath:      result.DistilledPath,
	}, nil
}

func (s *ctxServer) handleListSessions(_ context.Context, _ *mcp.CallToolRequest, in listSessionsInput) (*mcp.CallToolResult, *listSessionsResult, error) {
	backendName := in.Backend
	if backendName == "" {
		backendName = s.cfg.LM.GetDefaultPlugin()
	}

	backend := backends.Get(backendName)
	if backend == nil {
		return nil, nil, fmt.Errorf("unknown backend: %s", backendName)
	}

	history := backend.History()
	if history == nil {
		return nil, nil, fmt.Errorf("backend %q does not support session history", backendName)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	sessions, err := history.ListSessions(workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}

	sessionsDir := s.getSessionsDir()
	distilled, err := memory.ListDistilledSessions(sessionsDir)
	if err != nil {
		distilled = nil // Non-fatal — sessions still listable as "pending"
	}
	distilledSet := make(map[string]bool)
	for _, id := range distilled {
		distilledSet[id] = true
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

	return nil, &listSessionsResult{
		Sessions: result,
		Count:    len(result),
		Backend:  backendName,
	}, nil
}

func (s *ctxServer) handleLoadSession(ctx context.Context, _ *mcp.CallToolRequest, in loadSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	sessionID := in.SessionID
	if in.HarpName != "" {
		resolved, err := resolveHarpToSessionID(in.HarpName)
		if err != nil {
			return nil, nil, err
		}
		sessionID = resolved
	}
	if sessionID == "" {
		return nil, nil, fmt.Errorf("either session_id or harp_name is required")
	}
	return s.loadOrDistillSession(ctx, sessionID, in.Backend, in.Model, 0)
}

// resolveHarpToSessionID looks up a harp name in ~/.ctxloom/sessions/index.yaml
// and returns the bound session_id. Errors when the harp is unknown or
// when the entry exists but hasn't been bound yet (i.e., the spawned LLM's
// MCP server hasn't called initialize for that harp).
func resolveHarpToSessionID(harpName string) (string, error) {
	mgr, err := sessions.Open("")
	if err != nil {
		return "", fmt.Errorf("session index: %w", err)
	}
	entry, err := mgr.Find(harpName)
	if err != nil {
		return "", fmt.Errorf("lookup harp %q: %w", harpName, err)
	}
	if entry == nil {
		return "", fmt.Errorf("harp not found in index: %q", harpName)
	}
	if entry.SessionID == "" {
		return "", fmt.Errorf("harp %q is pending (no backend session ID bound yet)", harpName)
	}
	return entry.SessionID, nil
}

func (s *ctxServer) handleRecoverSession(ctx context.Context, _ *mcp.CallToolRequest, in recoverSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	backendName := in.Backend
	if backendName == "" {
		backendName = s.cfg.LM.GetDefaultPlugin()
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return nil, nil, fmt.Errorf("unknown backend: %s", backendName)
	}
	history := backend.History()
	if history == nil {
		return nil, nil, fmt.Errorf("backend %q does not support session history", backendName)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	targetSessionID := in.SessionID
	if targetSessionID == "" {
		sessions, err := history.ListSessions(workDir)
		if err != nil {
			return nil, nil, fmt.Errorf("list sessions: %w", err)
		}
		if len(sessions) == 0 {
			return nil, &loadSessionResult{Loaded: false, Message: "No sessions found."}, nil
		}
		targetSessionID = sessions[0].ID
	}

	return s.loadOrDistillSession(ctx, targetSessionID, backendName, in.Model, 0)
}

func (s *ctxServer) handleGetPreviousSession(ctx context.Context, _ *mcp.CallToolRequest, in getPreviousSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	pid := findCtxloomWrapperPID()

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	backendName := s.cfg.LM.GetDefaultPlugin()
	if backendName == "" {
		backendName = "claude-code"
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return nil, nil, fmt.Errorf("backend %q not found", backendName)
	}
	history := backend.History()
	if history == nil {
		return nil, nil, fmt.Errorf("session history not available for backend %q", backendName)
	}

	prevSession, err := history.GetPreviousSession(workDir, pid)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup previous session: %w", err)
	}
	if prevSession == nil {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("No previous session found for PID %d.", pid),
			PID:     pid,
		}, nil
	}

	return s.loadOrDistillSession(ctx, prevSession.ID, backendName, in.Model, pid)
}

func (s *ctxServer) handleBrowseSessionHistory(ctx context.Context, _ *mcp.CallToolRequest, in browseSessionHistoryInput) (*mcp.CallToolResult, *browseSessionHistoryResult, error) {
	backendName := in.Backend
	if backendName == "" {
		backendName = s.cfg.LM.GetDefaultPlugin()
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return nil, nil, fmt.Errorf("unknown backend: %s", backendName)
	}
	history := backend.History()
	if history == nil {
		return nil, nil, fmt.Errorf("backend %q does not support session history", backendName)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	sessions, err := history.ListSessions(workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}

	// 3-day window is the legacy default and balances "useful recent
	// context" against the cost of essence generation across older logs.
	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	var recentSessions []backends.SessionMeta
	for _, meta := range sessions {
		if meta.StartTime.After(threeDaysAgo) {
			recentSessions = append(recentSessions, meta)
		}
	}

	if len(recentSessions) == 0 {
		return nil, &browseSessionHistoryResult{
			Sessions: []sessionWithEssence{},
			Count:    0,
			Message:  "No sessions found in the last 3 days.",
		}, nil
	}

	memoryDir := s.getMemoryDir()
	model := s.cfg.GetCompactionModel()
	plugin := s.cfg.GetCompactionPlugin()

	var results []sessionWithEssence
	for _, meta := range recentSessions {
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

		// An essence "older than ~1 second" must have come from cache —
		// freshly generated essences carry a near-now timestamp.
		wasCached := essence.GeneratedAt.Before(time.Now().Add(-1 * time.Second))

		results = append(results, sessionWithEssence{
			ID:        meta.ID,
			Started:   meta.StartTime.Format("2006-01-02 15:04"),
			Entries:   meta.EntryCount,
			Essence:   essence.Essence,
			WasCached: wasCached,
		})
	}

	return nil, &browseSessionHistoryResult{
		Sessions: results,
		Count:    len(results),
		Message:  fmt.Sprintf("Found %d sessions from the last 3 days. Use load_session with a session_id to load one.", len(results)),
	}, nil
}

// loadOrDistillSession is the shared body for load_session, recover_session,
// and get_previous_session. It returns the cached distilled content if
// available; otherwise it runs the compactor on-demand and then loads what
// was just written. The pid argument is only set by get_previous_session so
// the response can carry it back; for the other callers it's zero and the
// PID field stays omitted.
func (s *ctxServer) loadOrDistillSession(ctx context.Context, sessionID, backendName, model string, pid int) (*mcp.CallToolResult, *loadSessionResult, error) {
	if backendName == "" {
		backendName = s.cfg.LM.GetDefaultPlugin()
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return nil, nil, fmt.Errorf("unknown backend: %s", backendName)
	}
	history := backend.History()
	if history == nil {
		return nil, nil, fmt.Errorf("backend %q does not support session history", backendName)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	if sessionID == "" {
		return nil, nil, fmt.Errorf("session_id is required. Use browse_session_history or list_sessions to find session IDs")
	}

	session, err := history.GetSession(workDir, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	if len(session.Entries) == 0 {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("Session %s appears to be empty.", sessionID),
			PID:     pid,
		}, nil
	}

	sessionsDir := s.getSessionsDir()

	// Cached path: the session was already distilled, return immediately.
	if distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID); err == nil {
		return nil, &loadSessionResult{
			Loaded:    true,
			SessionID: distilled.SessionID,
			Content:   distilled.Body,
			WasCached: true,
			Tokens:    distilled.TokensOut,
			CreatedAt: distilled.DistilledAt.Format("2006-01-02 15:04:05"),
			PID:       pid,
		}, nil
	}

	fmt.Fprintf(os.Stderr, "ctxloom: distilling session %s (this may take a moment)...\n", sessionID)

	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    s.cfg.GetCompactionPlugin(),
		Model:     model,
		Backend:   backendName,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: sessionID,
		WorkDir:   workDir,
		OutputDir: sessionsDir,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create compactor: %w", err)
	}

	compactResult, err := compactor.Compact(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("distillation failed: %w", err)
	}

	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("load distilled result: %w", err)
	}

	return nil, &loadSessionResult{
		Loaded:    true,
		SessionID: compactResult.SessionID,
		Content:   distilled.Body,
		WasCached: false,
		Duration:  compactResult.Duration.String(),
		TokensIn:  compactResult.TotalTokensIn,
		TokensOut: compactResult.TotalTokensOut,
		Reduction: fmt.Sprintf("%.0f%%", 100*(1-float64(compactResult.TotalTokensOut)/float64(compactResult.TotalTokensIn))),
		PID:       pid,
	}, nil
}
