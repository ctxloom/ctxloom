package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// reductionPct formats the in→out token reduction as a percentage, guarding
// the in==0 case (a zero-input distill) that would otherwise divide by zero
// and render as "NaN%" or "+Inf%".
func reductionPct(in, out int) string {
	if in <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*(1-float64(out)/float64(in)))
}

// Memory-tool input types. All session-targeting tools accept an optional
// session_id and an optional backend override. The defaults come from cfg
// (cfg.GetDefaultLLM() for backend, current session for ID).

type compactSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to compact (defaults to current session)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation (defaults to config or claude-3-haiku)"`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to claude-code)"`
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

// loadSessionResult covers both the "loaded" success shape and the
// "empty session" / "not found" shapes. The fields that only appear on
// success use omitempty so the wire format stays compatible with the
// legacy map-based response.
type loadSessionResult struct {
	Loaded    bool   `json:"loaded"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Content   string `json:"content,omitempty"`
	WasCached bool   `json:"was_cached,omitempty"`
	Tokens    int    `json:"tokens,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Reduction string `json:"reduction,omitempty"`
	Duration  string `json:"duration,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

// getSessionsDir returns the directory holding distilled session .md files.
// Persistent (not ephemeral) because distilled summaries are a per-repo cache
// the user benefits from across restarts.
func (s *ctxServer) getSessionsDir() string {
	return paths.ProjectSessionsDir(s.cfg.AppDir)
}

func (s *ctxServer) registerMemoryTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "compact_session",
			Description: "Compact current or specified session log into a distilled summary. Use this to compress a session log when context is running low.",
		},
		s.handleCompactSession)

	// Phase 4 Lever A: list_sessions and browse_session_history moved to
	// resources. Use ctxloom://sessions (all projects) or
	// ctxloom://sessions/recent (cwd-filtered, AI-friendly summary).

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "load_session",
			Description: "Distill and load context from a session. Accepts either session_id (backend UUID) or harp_name (human-readable). For names, see ctxloom://sessions/recent.",
		},
		s.handleLoadSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "recover_session",
			Description: "Recover context from the current session after /clear. Resolves the most recent session transcript for this working directory and distills it (no session id needed; pass one to target a specific session).",
		},
		s.handleRecoverSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_previous_session",
			Description: "Get the previous session's distilled content by looking up the session registry. Use this to recover context from before /clear.",
		},
		s.handleGetPreviousSession)
}

func (s *ctxServer) handleCompactSession(ctx context.Context, _ *mcp.CallToolRequest, in compactSessionInput) (*mcp.CallToolResult, *compactSessionResult, error) {
	plugin := s.cfg.GetCompactionLLM()
	model := in.Model
	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	backend := in.Backend
	if backend == "" {
		backend = s.cfg.GetDefaultLLM()
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	sessionsDir := s.getSessionsDir()

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:       plugin,
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
		Reduction:       reductionPct(result.TotalTokensIn, result.TotalTokensOut),
		Duration:        result.Duration.String(),
		OutputPath:      result.DistilledPath,
	}, nil
}

// handleListSessions and listSessionsInput / listSessionsResult removed
// in Phase 4 Lever A — replaced by the ctxloom://sessions resource.

func (s *ctxServer) handleLoadSession(ctx context.Context, _ *mcp.CallToolRequest, in loadSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	if in.HarpName != "" {
		// Harp-native path: read ~/.ctxloom/sessions/<harp>/essence.md
		// directly. No backend-history detour, no SessionID binding step.
		// If the file is missing the user can run `ctxloom session distill`
		// (when that subcommand lands) or just compact again.
		return s.loadHarpEssence(in.HarpName)
	}
	if in.SessionID == "" {
		return nil, nil, fmt.Errorf("either session_id or harp_name is required")
	}
	return s.loadOrDistillSession(ctx, in.SessionID, in.Backend, in.Model, 0)
}

// loadHarpEssence reads ~/.ctxloom/sessions/<harp>/essence.md and returns
// it as a loadSessionResult. The harp-dir layout (Phase 3.6) is keyed by
// the human-readable harp name, so this path is independent of backend
// session UUIDs. Errors when the harp is unknown to the index or its
// essence.md doesn't exist (compact_session hasn't run for this harp yet).
func (s *ctxServer) loadHarpEssence(harpName string) (*mcp.CallToolResult, *loadSessionResult, error) {
	entry, err := operations.GetSession(harpName)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup harp %q: %w", harpName, err)
	}
	if entry == nil {
		return nil, nil, fmt.Errorf("harp not found in index: %q", harpName)
	}
	essencePath, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return nil, nil, fmt.Errorf("home dir: %w", err)
	}
	data, err := os.ReadFile(essencePath)
	if err != nil {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("No distilled essence for %s yet. Run `ctxloom session distill %s` or compact_session to generate one.", harpName, harpName),
		}, nil
	}
	return nil, &loadSessionResult{
		Loaded:    true,
		SessionID: entry.SessionID, // may be empty; not load-bearing
		Content:   string(data),
		WasCached: true,
		CreatedAt: entry.StartedAt.Format("2006-01-02 15:04:05"),
	}, nil
}

func (s *ctxServer) handleRecoverSession(ctx context.Context, _ *mcp.CallToolRequest, in recoverSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	backendName := in.Backend
	if backendName == "" {
		backendName = s.cfg.GetDefaultLLM()
	}
	if !backends.Exists(backendName) {
		return nil, nil, fmt.Errorf("unknown backend: %s", backendName)
	}

	targetSessionID := in.SessionID
	if targetSessionID == "" {
		sessions, err := pb.NewSessionReader(backendName, 0).ListSessions(ctx)
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
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	backendName := s.cfg.GetDefaultLLM()
	if backendName == "" {
		backendName = "claude-code"
	}

	// Index-authoritative: ctxloom decides which session is previous and which
	// agent produced it (cross-agent aware); the owning agent server materializes
	// it. Best-effort — a lookup error degrades to the positional fallback.
	sessionID := ""
	if ref, rerr := operations.ResolvePreviousSession(workDir, os.Getenv("CTXLOOM_SESSION_HARP")); rerr == nil && ref != nil {
		sessionID = ref.SessionID
		if ref.Backend != "" {
			backendName = ref.Backend
		}
	}

	if !backends.Exists(backendName) {
		return nil, nil, fmt.Errorf("backend %q not found", backendName)
	}

	// Fallback for pre-binding history (no index entry): the second-most-recent
	// transcript in the agent's own store is the previous one (the most recent is
	// the active session).
	if sessionID == "" {
		metas, lerr := pb.NewSessionReader(backendName, 0).ListSessions(ctx)
		if lerr != nil {
			return nil, nil, fmt.Errorf("lookup previous session: %w", lerr)
		}
		if len(metas) >= 2 {
			sessionID = metas[1].ID
		}
	}

	if sessionID == "" {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: "No previous session found for this project.",
		}, nil
	}

	return s.loadOrDistillSession(ctx, sessionID, backendName, in.Model, 0)
}

// handleBrowseSessionHistory and its input/result types removed in Phase 4 Lever A.
// Use ctxloom://sessions/recent for the AI-friendly summary view.

// loadOrDistillSession is the shared body for load_session, recover_session,
// and get_previous_session. It returns the cached distilled content if
// available; otherwise it runs the compactor on-demand and then loads what
// was just written. The pid argument is only set by get_previous_session so
// the response can carry it back; for the other callers it's zero and the
// PID field stays omitted.
func (s *ctxServer) loadOrDistillSession(ctx context.Context, sessionID, backendName, model string, pid int) (*mcp.CallToolResult, *loadSessionResult, error) {
	source, backendName, err := resolveSessionSource(s.cfg, backendName)
	if err != nil {
		return nil, nil, err
	}

	if sessionID == "" {
		return nil, nil, fmt.Errorf("session_id is required; list sessions via the ctxloom://sessions/recent resource to find one")
	}

	session, err := source.GetSession(ctx, sessionID)
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
	if cached := loadCachedDistilledSession(sessionsDir, sessionID, pid); cached != nil {
		return nil, cached, nil
	}

	// workDir feeds CompactionConfig for compatibility; the gRPC transcript read
	// is self-situated and ignores it.
	workDir, _ := os.Getwd()
	result, err := s.distillSession(ctx, sessionID, backendName, model, workDir, sessionsDir, pid)
	return nil, result, err
}

// loadCachedDistilledSession returns a result from an already-distilled session
// on disk, or nil when none is cached.
func loadCachedDistilledSession(sessionsDir, sessionID string, pid int) *loadSessionResult {
	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err != nil {
		return nil
	}
	return &loadSessionResult{
		Loaded:    true,
		SessionID: distilled.SessionID,
		Content:   distilled.Body,
		WasCached: true,
		Tokens:    distilled.TokensOut,
		CreatedAt: distilled.DistilledAt.Format("2006-01-02 15:04:05"),
		PID:       pid,
	}
}

// distillSession compacts a session and returns the freshly-distilled result.
func (s *ctxServer) distillSession(ctx context.Context, sessionID, backendName, model, workDir, sessionsDir string, pid int) (*loadSessionResult, error) {
	fmt.Fprintf(os.Stderr, "ctxloom: distilling session %s (this may take a moment)...\n", sessionID)

	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:       s.cfg.GetCompactionLLM(),
		Model:     model,
		Backend:   backendName,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: sessionID,
		WorkDir:   workDir,
		OutputDir: sessionsDir,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}

	compactResult, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("distillation failed: %w", err)
	}

	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load distilled result: %w", err)
	}

	return &loadSessionResult{
		Loaded:    true,
		SessionID: compactResult.SessionID,
		Content:   distilled.Body,
		WasCached: false,
		Duration:  compactResult.Duration.String(),
		TokensIn:  compactResult.TotalTokensIn,
		TokensOut: compactResult.TotalTokensOut,
		Reduction: reductionPct(compactResult.TotalTokensIn, compactResult.TotalTokensOut),
		PID:       pid,
	}, nil
}
