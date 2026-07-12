package cli

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
	"github.com/ctxloom/ctxloom/internal/sessions"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
}

type loadSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Backend-native session ID (UUID). Either session_id or harp_name is required."`
	HarpName  string `json:"harp_name,omitempty" jsonschema:"Harp-named session reference (e.g. \"swift-amber-falcon\") from ~/.ctxloom/sessions/index.yaml. Resolved to a session_id via the index; if both are passed, harp_name wins."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

type recoverSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to recover. If not provided, uses most recent session."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
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
}

// getSessionsDir returns the directory holding distilled session .md files.
// Persistent (not ephemeral) because distilled summaries are a per-repo cache
// the user benefits from across restarts.
func (s *ctxServer) getSessionsDir() string {
	return paths.ProjectSessionsDir(s.cfg.AppDir)
}

// This and the sibling registerXTools functions share a duplicate shape by
// construction (a run of mcp.AddTool calls). Their tool descriptions are
// independent content; a change to one implies nothing about the others.
// reprise:accept-drift
func (s *ctxServer) registerMemoryTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "compact_session",
			Description: "Pre-bake a session's persisted log into a distilled summary on disk, for recovery in a LATER session. This reads the stored transcript and spawns its own LLM calls; it does NOT touch the live conversation and frees no context. It is not a substitute for the harness's native compaction — for live context pressure, use that. Call this before ending or clearing a session whose essence should survive it.",
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
			Description: "Distill and load an EARLIER session's content — the most recent session BEFORE the active one for this working directory, resolved via the session registry (cross-agent aware; falls back to the second-most-recent transcript). For inspecting a prior session. NOT the post-/clear path: /clear keeps the SAME session alive, so to recover context wiped by /clear use recover_session instead.",
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
		// The CALLER's harp (credential-derived on the coordinator's HTTP
		// surface) — never the compactor's ambient env fallback, which would
		// key a child's compaction under the host process's session.
		HarpName: s.self.Harp,
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
	return s.loadOrDistillSession(ctx, in.SessionID, in.Backend, in.Model, false)
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
		// Identity-first: the active harp's own session-index binding is the exact
		// transcript for the current session — set once, at bind time, and never
		// touched again. An mtime-position pick is unreliable here because Claude
		// Code (and other backends) rewrite/touch a transcript file when a session
		// is resumed, so "newest by mtime" is not reliably "the session that just
		// ended" (seedy-apron). Only fall back to the mtime pick when the harp is
		// unbound (the SessionStart bind hook never fired) or its bound backend
		// doesn't match the one being read from.
		activeEntry, _ := operations.GetSession(s.self.Harp)
		targetSessionID = recoverTargetSessionID(activeEntry, backendName, nil, fileExists)
		if targetSessionID == "" {
			sessionsList, err := pb.NewSessionReader(backendName, 0).ListSessions(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("list sessions: %w", err)
			}
			if len(sessionsList) == 0 {
				return nil, &loadSessionResult{Loaded: false, Message: "No sessions found."}, nil
			}
			targetSessionID = recoverTargetSessionID(nil, backendName, sessionsList, fileExists)
		}
	}

	// Recover targets the live current session, which is still growing. Normally
	// staleness is decided by the transcript's byte size (reuse the cache when it
	// hasn't moved), but when that size can't be determined recover errs toward
	// re-distilling — a cached essence from an earlier /clear in this same session
	// covers only an earlier slice. redistillWhenUnknown=true encodes that bias.
	return s.loadOrDistillSession(ctx, targetSessionID, backendName, in.Model, true)
}

// recoverTargetSessionID resolves which session recover_session should target
// when the caller didn't pass one explicitly. It prefers the active harp's own
// index-bound session id (identity, exact) over mtimeSessions[0] (a
// mtime-position pick that a resumed/touched transcript elsewhere can
// invalidate) — UNLESS the bound entry's transcript no longer exists on disk
// (rotated/deleted, a stale index entry left over from an earlier session):
// trusting a dead id would skip the mtime listing and return an id nothing
// can load, so a stale binding degrades to the mtime fallback exactly like an
// unbound harp (FINDING #4). transcriptExists is injected (production passes
// fileExists) so this stays pure for testability; mtimeSessions is assumed
// most-recent-first (every backend's ListSessions ordering). Called twice by
// the handler: first with mtimeSessions=nil (identity-only probe, so the —
// potentially subprocess-spawning — mtime listing is skipped whenever
// identity resolves), then with activeEntry=nil once the listing has
// actually been fetched.
func recoverTargetSessionID(activeEntry *sessions.Entry, backendName string, mtimeSessions []agent.SessionMeta, transcriptExists func(string) bool) string {
	if activeEntry != nil && activeEntry.SessionID != "" &&
		(activeEntry.Backend == "" || activeEntry.Backend == backendName) &&
		(activeEntry.TranscriptPath == "" || transcriptExists(activeEntry.TranscriptPath)) {
		return activeEntry.SessionID
	}
	if len(mtimeSessions) > 0 {
		return mtimeSessions[0].ID
	}
	return ""
}

func (s *ctxServer) handleGetPreviousSession(ctx context.Context, _ *mcp.CallToolRequest, in getPreviousSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	backendName := s.cfg.GetDefaultLLM()

	// Index-authoritative: ctxloom decides which session is previous and which
	// agent produced it (cross-agent aware); the owning agent server materializes
	// it. Best-effort — a lookup error degrades to the positional fallback.
	sessionID := ""
	// Identity comes from the caller (credential-derived on the coordinator's
	// HTTP surface, env-derived on stdio) — never a raw env read, so a child
	// calling through the shared coordinator server resolves ITS previous
	// session, not the host process's.
	if ref, rerr := operations.ResolvePreviousSession(workDir, s.self.Harp); rerr == nil && ref != nil {
		sessionID = ref.SessionID
		if ref.Backend != "" {
			backendName = ref.Backend
		}
	}

	if !backends.Exists(backendName) {
		return nil, nil, fmt.Errorf("backend %q not found", backendName)
	}

	// Fallback for pre-binding history (no index entry): pick the newest
	// transcript in the agent's own store that ISN'T the active session's own
	// bound id (when known), rather than assuming the active session is
	// positionally first (metas[0]) and blindly taking metas[1] as "previous" —
	// a transcript touched elsewhere (e.g. by a resume) can out-rank the active
	// session by mtime and shift every position by one, so a blind index-1 pick
	// can return the active session itself or an unrelated foreign transcript.
	if sessionID == "" {
		metas, lerr := pb.NewSessionReader(backendName, 0).ListSessions(ctx)
		if lerr != nil {
			// A flaky transcript listing must not turn a recovery convenience into a
			// tool error: warn and fall through to the "no previous session" result.
			clidiag.Warn("ctxloom", "list previous sessions: %v", lerr)
		}
		activeSessionID := ""
		if activeEntry, aerr := operations.GetSession(s.self.Harp); aerr == nil && activeEntry != nil {
			activeSessionID = activeEntry.SessionID
		}
		sessionID = previousSessionFromMtime(activeSessionID, metas)
	}

	if sessionID == "" {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: "No previous session found for this project.",
		}, nil
	}

	return s.loadOrDistillSession(ctx, sessionID, backendName, in.Model, false)
}

// previousSessionFromMtime is the last-resort pick for get_previous_session
// when the session index has no authoritative "previous" entry (pre-binding
// history, or a project the index has never seen): the newest-by-mtime
// transcript other than activeSessionID (when known), instead of assuming the
// active session sits at position 0. Pure for testability; metas is assumed
// most-recent-first. Returns "" when nothing remains (e.g. the store holds
// only the active session, or is empty).
//
// When activeSessionID is unknown (""), metas[0] cannot be ruled out — during
// a live session it in fact IS the active session, since a growing transcript
// ranks newest by mtime. Returning it would hand the caller their own live
// session as "previous" (strictly worse than the old positional metas[1]
// pick this replaced). So the no-active-known case assumes metas[0] is the
// active session and returns metas[1] (the second-newest) instead — never
// metas[0].
func previousSessionFromMtime(activeSessionID string, metas []agent.SessionMeta) string {
	if activeSessionID == "" {
		if len(metas) < 2 {
			return ""
		}
		return metas[1].ID
	}
	for _, m := range metas {
		if m.ID == activeSessionID {
			continue
		}
		return m.ID
	}
	return ""
}

// handleBrowseSessionHistory and its input/result types removed in Phase 4 Lever A.
// Use ctxloom://sessions/recent for the AI-friendly summary view.

// loadOrDistillSession is the shared body for load_session, recover_session,
// and get_previous_session. It reuses the cached distilled essence when it is
// still current and otherwise re-distills on demand, then loads what was just
// written. Staleness is decided by the source transcript's byte size: the size
// stamped at distill time (essence frontmatter) versus the live transcript file.
//
//   - cache is current (size unchanged)  -> return the cache
//   - cache is stale (size changed)      -> re-distill from the full transcript
//   - staleness can't be determined      -> redistillWhenUnknown decides:
//     recover_session re-distills (the live session may have grown past the
//     cache); load_session / get_previous_session keep the cache (a finished
//     session rarely changes, so spending an LLM call on it isn't worth it).
func (s *ctxServer) loadOrDistillSession(ctx context.Context, sessionID, backendName, model string, redistillWhenUnknown bool) (*mcp.CallToolResult, *loadSessionResult, error) {
	source, backendName, err := resolveSessionSource(s.cfg, backendName)
	if err != nil {
		return nil, nil, err
	}

	if sessionID == "" {
		return nil, nil, fmt.Errorf("session_id is required; list sessions via the ctxloom://sessions/recent resource to find one")
	}

	session, err := source.GetSession(ctx, sessionID)
	if err != nil {
		// Degrade a lookup failure to a usable "couldn't load" message rather than a
		// tool error — recovery must never block the agent (CLAUDE.md).
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("Couldn't load session %s: %v", sessionID, err),
		}, nil
	}
	if len(session.Entries) == 0 {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("Session %s appears to be empty.", sessionID),
		}, nil
	}

	sessionsDir := s.getSessionsDir()

	// Resolve the harp that owns this session so its plan files
	// (~/.ctxloom/sessions/<harp>/) are read for the RIGHT session — not the
	// active one — when distilling a previous or cross-agent session, and so the
	// staleness check stats the harp's bound transcript.
	harp, _ := operations.HarpForSession(sessionID)
	transcriptPath := ""
	if harp != "" {
		if entry, _ := operations.GetSession(harp); entry != nil {
			transcriptPath = entry.TranscriptPath
		}
	}

	// Cached path: reuse the essence when the transcript hasn't moved past it.
	if cached, stampedSize := loadCachedDistilledSession(sessionsDir, sessionID); cached != nil {
		stale, known := sessions.TranscriptStale(transcriptPath, stampedSize)
		if (known && !stale) || (!known && !redistillWhenUnknown) {
			return nil, cached, nil
		}
		// stale, or indeterminate with a re-distill bias: fall through.
	}

	// workDir feeds CompactionConfig for compatibility; the gRPC transcript read
	// is self-situated and ignores it.
	workDir, _ := os.Getwd()
	result, err := s.distillSession(ctx, sessionID, backendName, model, workDir, sessionsDir, harp)
	return nil, result, err
}

// loadCachedDistilledSession returns a result from an already-distilled session
// on disk plus the transcript byte size stamped into its frontmatter (the
// staleness fingerprint), or (nil, 0) when none is cached.
func loadCachedDistilledSession(sessionsDir, sessionID string) (*loadSessionResult, int64) {
	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err != nil {
		return nil, 0
	}
	return &loadSessionResult{
		Loaded:    true,
		SessionID: distilled.SessionID,
		Content:   distilled.Body,
		WasCached: true,
		Tokens:    distilled.TokensOut,
		CreatedAt: distilled.DistilledAt.Format("2006-01-02 15:04:05"),
	}, distilled.SourceSize
}

// distillSession compacts a session and returns the freshly-distilled result.
// harp keys the session's plan files; pass "" to fall back to the active harp.
func (s *ctxServer) distillSession(ctx context.Context, sessionID, backendName, model, workDir, sessionsDir, harp string) (*loadSessionResult, error) {
	fmt.Fprintf(os.Stderr, "ctxloom: distilling session %s (this may take a moment)...\n", sessionID)

	if model == "" {
		model = s.cfg.GetCompactionModel()
	}

	// Recovery must never block the agent (CLAUDE.md): a compactor/distill failure
	// degrades to a usable "couldn't distill" message rather than a tool error.
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:       s.cfg.GetCompactionLLM(),
		Model:     model,
		Backend:   backendName,
		ChunkSize: s.cfg.GetCompactionChunkSize(),
		SessionID: sessionID,
		WorkDir:   workDir,
		OutputDir: sessionsDir,
		HarpName:  harp,
	})
	if err != nil {
		return &loadSessionResult{Loaded: false, Message: fmt.Sprintf("Couldn't start distillation for session %s: %v", sessionID, err)}, nil
	}

	compactResult, err := compactor.Compact(ctx)
	if err != nil {
		return &loadSessionResult{Loaded: false, Message: fmt.Sprintf("Distillation failed for session %s: %v", sessionID, err)}, nil
	}

	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err != nil {
		return &loadSessionResult{Loaded: false, Message: fmt.Sprintf("Distilled session %s but couldn't read it back: %v", sessionID, err)}, nil
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
	}, nil
}
