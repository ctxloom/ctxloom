package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/transcript"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// compactEntryFn is operations.CompactEntry behind a package var so a
// caller's wiring — notably the context bound a long-running distillation is
// handed — can be observed in a test (mcp_tools_memory_budget_test.go).
// Production never reassigns it; operations must not inherit this
// observation-only test seam, so it lives beside its sole caller
// (distillMissingForList) rather than moving with CompactEntry itself.
var compactEntryFn = operations.CompactEntry

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

type listSessionsInput struct {
	AllProjects    bool `json:"all_projects,omitempty" jsonschema:"List sessions from every project instead of only the current working directory's project (mirrors session list --all)"`
	DistillMissing bool `json:"distill_missing,omitempty" jsonschema:"Distill sessions whose essence is missing or stale before listing, so every row carries a title. Runs the compactor out of band; canonical-transcript sessions distill, legacy-only sessions are skipped."`
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

// sessionSummary is one row of list_sessions: the harp name to pass to
// load_session, its backend, the distilled title (empty when never distilled),
// the last-activity timestamp (local, second granularity — same format the CLI
// listing uses), and whether an essence exists on disk.
type sessionSummary struct {
	Harp         string `json:"harp"`
	Backend      string `json:"backend"`
	Title        string `json:"title"`
	LastActivity string `json:"last_activity"`
	Distilled    bool   `json:"distilled"`
}

type listSessionsResult struct {
	Sessions []sessionSummary `json:"sessions"`
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

// Session-memory tool descriptions. Constants because the RUNNER surface
// registers the same tools as host relays (mcp_runner.go) and must advertise
// byte-identical text.
const (
	compactSessionDesc     = "Distil a session's PERSISTED transcript into a summary on disk, for a LATER session to pick up. Reads the stored log in a separate process; it does NOT touch your live conversation and frees no context in it. Do NOT call this because you are running low on context — for that, use your harness's native compaction. This exists precisely so that a context-starved agent never has to write its own summary: it runs out of band, against the full transcript, with a fresh budget. Normally you do not call it at all — it runs on shutdown, at startup for historical sessions, and on recovery after a /clear. Call it explicitly only to force an essence before ending a session."
	listSessionsDesc       = "List harp-named sessions with their title, backend, last-activity time, and whether they're distilled — the menu you pick a harp from to hand to load_session. Defaults to the current working directory's project; set all_projects to span every project. Set distill_missing to compact title-less or stale sessions first so every row shows a title."
	loadSessionDesc        = "Distill and load context from a session. Accepts either session_id (backend UUID) or harp_name (human-readable). For names, see ctxloom://sessions/recent."
	recoverSessionDesc     = "Recover context from the current session after /clear. Resolves the most recent session transcript for this working directory and distills it (no session id needed; pass one to target a specific session)."
	getPreviousSessionDesc = "Distill and load an EARLIER session's content — the most recent session BEFORE the active one for this working directory, resolved via the session registry (cross-agent aware; falls back to the second-most-recent transcript). For inspecting a prior session. NOT the post-/clear path: /clear keeps the SAME session alive, so to recover context wiped by /clear use recover_session instead."
)

// This and the sibling registerXTools functions share a duplicate shape by
// construction (a run of mcp.AddTool calls). Their tool descriptions are
// independent content; a change to one implies nothing about the others.
// reprise:accept-drift
func (s *ctxServer) registerMemoryTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "compact_session",
			Description: compactSessionDesc,
		},
		s.handleCompactSession)

	// browse_session_history remains a resource (ctxloom://sessions/recent).
	// list_sessions is back as a TOOL, not a resource: a caller deciding which
	// harp to resume needs to name it in a following load_session call, and
	// distill_missing has a side effect (it runs the compactor) — neither fits
	// the read-only resource model.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_sessions",
			Description: listSessionsDesc,
		},
		s.handleListSessions)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "load_session",
			Description: loadSessionDesc,
		},
		s.handleLoadSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "recover_session",
			Description: recoverSessionDesc,
		},
		s.handleRecoverSession)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_previous_session",
			Description: getPreviousSessionDesc,
		},
		s.handleGetPreviousSession)
}

// withDistillBudget bounds ctx to the relay's distill budget when it carries
// no deadline of its own, and returns it unchanged when it does.
//
// On the host-relay path a handler runs on the COORDINATOR's base context,
// which has no deadline: the caller's budget bounds only how long it WAITS,
// never the work. Left unbounded, one wedged LLM subprocess runs forever —
// holding a singleflight entry open, or a listing. mcpschema.DistillBudget is
// one number deliberately covering both sides of the relay, so a host can
// never outlive its caller's patience by design; this is where the host side
// of that contract is applied. A caller that already carries a deadline keeps
// it: re-bounding would EXTEND someone who asked for less.
func withDistillBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, mcpschema.DistillBudget)
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

	ctx, cancel := withDistillBudget(ctx)
	defer cancel()

	sessionsDir := paths.ProjectSessionsDir(s.cfg.GetAppDir())

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

// handleListSessions returns the harp-session menu a caller picks from before
// load_session. Default scope is the server's working-directory project;
// all_projects spans every project. Both paths return rows enriched and
// activity-sorted by operations.ListSessionsForProject / ListAllSessions —
// same ordering and formatting as `ctxloom session list`.
//
// distill_missing compacts title-less/stale rows first. Unlike the CLI, the
// long-lived MCP server must NOT chdir, so it calls compactEntry in place:
// canonical-transcript and preloaded-transcript sessions distill
// cwd-independently; a legacy session that needs the cwd-bound engine reader
// is skipped with a warning rather than corrupting the server's cwd.
func (s *ctxServer) handleListSessions(ctx context.Context, _ *mcp.CallToolRequest, in listSessionsInput) (*mcp.CallToolResult, *listSessionsResult, error) {
	loadEntries := func() ([]sessions.Entry, error) {
		if in.AllProjects {
			return operations.ListAllSessions()
		}
		workDir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		return operations.ListSessionsForProject(workDir)
	}

	entries, err := loadEntries()
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}

	if in.DistillMissing {
		s.distillMissingForList(ctx, entries)
		// Re-read so freshly-written summaries render in the returned rows.
		if refreshed, rerr := loadEntries(); rerr == nil {
			entries = refreshed
		}
	}

	rows := make([]sessionSummary, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		_, distilled := operations.SessionEssenceInfo(e.HarpName, e, s.cfg.GetAppDir())
		last := e.LastActivity
		if last.IsZero() {
			last = sessions.ActivityTime(*e)
		}
		rows = append(rows, sessionSummary{
			Harp:         e.HarpName,
			Backend:      e.Backend,
			Title:        e.Summary,
			LastActivity: last.Local().Format("2006-01-02 15:04:05"),
			Distilled:    distilled,
		})
	}
	return nil, &listSessionsResult{Sessions: rows}, nil
}

// distillMissingForList compacts every entry whose essence is missing or stale,
// in place (no chdir — see handleListSessions). Per-entry failures are warned
// and skipped: a session that can't be distilled here (e.g. a legacy session
// whose engine reader needs the cwd we deliberately don't change) must not fail
// the whole listing.
func (s *ctxServer) distillMissingForList(ctx context.Context, entries []sessions.Entry) {
	ctx, cancel := withDistillBudget(ctx)
	defer cancel()
	for i := range entries {
		e := &entries[i]
		_, distilled := operations.SessionEssenceInfo(e.HarpName, e, s.cfg.GetAppDir())
		stale, known := e.SourceStale()
		knownStale := known && stale
		if distilled && !knownStale {
			continue // fresh essence already present
		}
		if _, err := compactEntryFn(ctx, e, s.cfg, "", io.Discard); err != nil {
			clidiag.Warn("ctxloom", "list_sessions: could not distill %s: %v", e.HarpName, err)
		}
	}
}

func (s *ctxServer) handleLoadSession(ctx context.Context, _ *mcp.CallToolRequest, in loadSessionInput) (*mcp.CallToolResult, *loadSessionResult, error) {
	if in.HarpName != "" {
		// Harp-native path: read ~/.ctxloom/sessions/<harp>/essence.md
		// directly. No backend-history detour, no SessionID binding step.
		// If the file is missing the user can run `ctxloom session distill`
		// or just compact again.
		return s.loadHarpEssence(in.HarpName)
	}
	if in.SessionID == "" {
		return nil, nil, fmt.Errorf("either session_id or harp_name is required")
	}
	return s.loadOrDistillSession(ctx, in.SessionID, in.Backend, in.Model, policyArchived)
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
		// Only an ABSENT essence means "never distilled". Every other read
		// fault (permissions, a directory in its place, an I/O error) names a
		// file that exists and cannot be read, for which re-distilling is the
		// wrong remedy: the compaction would spend an LLM budget writing to
		// the same unreadable path and the caller would loop.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &loadSessionResult{
				Loaded:  false,
				Message: fmt.Sprintf("No distilled essence for %s yet. Run `ctxloom session distill %s` or compact_session to generate one.", harpName, harpName),
			}, nil
		}
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("The distilled essence for %s exists but could not be read (%s): %v. Re-distilling will not help until that path is readable.", harpName, essencePath, err),
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

	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}

	targetSessionID := in.SessionID
	if targetSessionID == "" {
		// Identity-first: the active harp's own session-index binding is the exact
		// transcript for the current session — set once, at bind time, and never
		// touched again. An mtime-position pick is unreliable here because Claude
		// Code (and other backends) rewrite/touch a transcript file when a session
		// is resumed, so "newest by mtime" is not reliably "the session that just
		// ended". Only fall back to the mtime pick when the harp is
		// unbound (the SessionStart bind hook never fired) or its bound backend
		// doesn't match the one being read from.
		activeEntry, _ := operations.GetSession(s.self.Harp)
		targetSessionID = recoverTargetSessionID(activeEntry, backendName, nil, regularFileExists)
		if targetSessionID == "" {
			source, _, serr := operations.ResolveSessionSource(s.cfg, backendName, workDir)
			if serr != nil {
				return nil, nil, serr
			}
			sessionsList, err := source.ListSessions(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("list sessions: %w", err)
			}
			if len(sessionsList) == 0 {
				return nil, &loadSessionResult{Loaded: false, Message: "No sessions found."}, nil
			}
			targetSessionID = recoverTargetSessionID(nil, backendName, sessionsList, regularFileExists)
		}
	}

	// Recover targets the live current session, which is still growing. Normally
	// staleness is decided by the transcript's byte size (reuse the cache when it
	// hasn't moved), but when that size can't be determined recover errs toward
	// re-distilling — a cached essence from an earlier /clear in this same session
	// covers only an earlier slice. redistillWhenUnknown=true encodes that bias.
	return s.loadOrDistillSession(ctx, targetSessionID, backendName, in.Model, policyLive)
}

// recoverTargetSessionID resolves which session recover_session should target
// when the caller didn't pass one explicitly. It prefers the active harp's own
// index-bound session id (identity, exact) over mtimeSessions[0] (a
// mtime-position pick that a resumed/touched transcript elsewhere can
// invalidate) — UNLESS the bound entry's transcript no longer exists on disk
// (rotated/deleted, a stale index entry left over from an earlier session):
// trusting a dead id would skip the mtime listing and return an id nothing
// can load, so a stale binding degrades to the mtime fallback exactly like an
// unbound harp. transcriptExists is injected (production passes
// regularFileExists) so this stays pure for testability; mtimeSessions is assumed
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

// regularFileExists reports whether p is an existing regular file (not a
// directory) — recoverTargetSessionID's production transcriptExists
// predicate. Deliberately NOT the same function as
// operations.SessionEssenceInfo's inlined existence check (identical logic,
// different call site, different reason to exist): unifying file-existence
// predicates across unrelated call sites is exactly the drift wave-brief §4
// warns about when the semantics might quietly diverge later.
func regularFileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
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
		// Canonical/ACP previous session: no backend session id was ever bound
		// (an ACP engine records only the harp's own captured transcript), so
		// there is nothing for a backend store to reassemble. Materialize it by
		// harp — the backend GetSession path can't read a canonical transcript,
		// and ref.Backend here may be an ACP engine that isn't a registered
		// backend at all, so it must not reach the backends.Exists gate below.
		if ref.SessionID == "" && ref.Harp != "" {
			return s.previousSessionByHarp(ctx, ref.Harp, in.Model)
		}
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
		source, _, serr := operations.ResolveSessionSource(s.cfg, backendName, workDir)
		if serr != nil {
			return nil, nil, serr
		}
		metas, lerr := source.ListSessions(ctx)
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

	return s.loadOrDistillSession(ctx, sessionID, backendName, in.Model, policyArchived)
}

// previousSessionByHarp materializes a canonical/ACP previous session — one
// with no backend SessionID, whose only source is the harp's own captured
// transcript and whose essence lives at ~/.ctxloom/sessions/<harp>/essence.md
// (NOT the legacy sessionID-keyed <sessionsDir>/<id>.md the backend path reads
// via LoadDistilledSession). It mirrors loadOrDistillSession's cache-then-
// distill shape, keyed by harp instead of session id:
//
//   - fresh essence on disk (source transcript hasn't grown past it) -> reuse
//   - missing or stale essence                                        -> distill
//
// Distillation goes through compactEntry, which resolves the canonical
// transcript by HarpName and writes essence under the harp dir — fixing the
// old bug where a canonical session's essence was written to a sessionID-keyed
// path under the project workdir. get_previous_session keeps an
// indeterminate cache (a finished session rarely changes), the same bias the
// backend path applies with redistillWhenUnknown=false.
//
// model is the caller's per-call override, forwarded to compactEntry ("" uses
// cfg.GetCompactionModel()). Honoring it ONLY on the backend path meant an
// explicit override was silently ignored whenever the previous session was
// canonical/ACP — the caller got a distill from a model it did not ask for,
// with no indication.
func (s *ctxServer) previousSessionByHarp(ctx context.Context, harp, model string) (*mcp.CallToolResult, *loadSessionResult, error) {
	entry, err := operations.GetSession(harp)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup harp %q: %w", harp, err)
	}
	if entry == nil {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: "No previous session found for this project.",
		}, nil
	}

	// Cached path: reuse the essence unless the source transcript has grown past
	// the size stamped at distill time. SourceStale compares against the
	// canonical transcript (enriched onto the entry by operations.GetSession/
	// Find), the same file compactEntry distills from.
	if data, rerr := operations.ReadHarpEssence(harp); rerr == nil {
		stale, known := entry.SourceStale()
		knownStale := known && stale
		if !knownStale {
			return nil, &loadSessionResult{
				Loaded:    true,
				SessionID: entry.SessionID, // empty for ACP; not load-bearing
				Content:   string(data),
				WasCached: true,
				CreatedAt: entry.StartedAt.Format("2006-01-02 15:04:05"),
			}, nil
		}
	}

	// Bound the work so a wedged LLM subprocess can't hold the singleflight
	// entry open forever (see withDistillBudget).
	ctx, cancel := withDistillBudget(ctx)
	defer cancel()
	// The model is part of the key, exactly as the backend path keys on it
	// (sessionID\x00backend\x00model): two concurrent calls asking for
	// DIFFERENT models must not collapse into one distill and hand both callers
	// a result from whichever model happened to win.
	res, err := s.singleflightDistill(harp+"\x00canonical\x00"+model, func() (*loadSessionResult, error) {
		if _, derr := operations.CompactEntry(ctx, entry, s.cfg, model, io.Discard); derr != nil {
			return &loadSessionResult{
				Loaded:  false,
				Message: fmt.Sprintf("Couldn't distill previous session %s: %v", harp, derr),
			}, nil
		}
		data, rerr := operations.ReadHarpEssence(harp)
		if rerr != nil {
			return &loadSessionResult{
				Loaded:  false,
				Message: fmt.Sprintf("Distilled %s but couldn't read its essence back: %v", harp, rerr),
			}, nil
		}
		return &loadSessionResult{
			Loaded:    true,
			SessionID: entry.SessionID,
			Content:   string(data),
			WasCached: false,
			CreatedAt: entry.StartedAt.Format("2006-01-02 15:04:05"),
		}, nil
	})
	return nil, res, err
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

// sessionLoadPolicy distinguishes loading a session that is STILL BEING WRITTEN
// from loading one that is finished. Both differences below follow from that one
// fact, which is why they travel together rather than as two loose booleans at a
// call site — the second bool argument is where "which flag was which" bugs live.
type sessionLoadPolicy struct {
	// RedistillWhenUnknown re-distills when transcript staleness cannot be
	// determined, rather than trusting the cached essence.
	RedistillWhenUnknown bool
	// LiveTranscript re-converts a vendor transcript that has ALREADY been
	// converted, instead of accepting the existing canonical file as current.
	LiveTranscript bool
}

// policyLive loads the session the caller is sitting in — recover_session, after
// a /clear. It is the only policy that pays to re-read a vendor transcript it has
// already read, because it is the only one whose source can have grown since.
var policyLive = sessionLoadPolicy{RedistillWhenUnknown: true, LiveTranscript: true}

// policyArchived loads a session that is over — load_session and
// get_previous_session. A finished session's transcript cannot change, so the
// cached essence and the existing canonical transcript are both trusted; spending
// an LLM call or a full re-conversion on one buys nothing.
var policyArchived = sessionLoadPolicy{}

// loadOrDistillSession is the shared body for load_session, recover_session,
// and get_previous_session. It reuses the cached distilled essence when it is
// still current and otherwise re-distills on demand, then loads what was just
// written. Staleness is decided by the source transcript's byte size: the size
// stamped at distill time (essence frontmatter) versus the live transcript file.
//
//   - cache is current (size unchanged)  -> return the cache
//   - cache is stale (size changed)      -> re-distill from the full transcript
//   - staleness can't be determined      -> policy.RedistillWhenUnknown decides:
//     recover_session re-distills (the live session may have grown past the
//     cache); load_session / get_previous_session keep the cache (a finished
//     session rarely changes, so spending an LLM call on it isn't worth it).
//
// A session whose transcript was never captured is CONVERTED on demand rather
// than refused. Interactive sessions have no live tee, so their canonical
// transcript is produced by reading the engine's own store back afterward — and
// requiring the user to import the vendor transcript by hand first made the tool fail
// at exactly the moment it was reached for.
func (s *ctxServer) loadOrDistillSession(ctx context.Context, sessionID, backendName, model string, policy sessionLoadPolicy) (*mcp.CallToolResult, *loadSessionResult, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}
	source, backendName, err := operations.ResolveSessionSource(s.cfg, backendName, workDir)
	if err != nil {
		return nil, nil, err
	}

	if sessionID == "" {
		return nil, nil, fmt.Errorf("session_id is required; list sessions via the ctxloom://sessions/recent resource to find one")
	}

	// A LIVE session's canonical transcript is re-read from the engine's own
	// store BEFORE it is loaded, not merely when it is missing. This is the
	// second-/recover case and it fails silently without this line: the first
	// recovery converts the transcript as of that moment, so every later one
	// finds a canonical file already present, reads it, and returns a session
	// frozen at the first recovery — complete-looking, confidently wrong, and
	// indistinguishable from a working recovery by anything downstream.
	refreshFailed := false
	if policy.LiveTranscript {
		if harp := sessionHarpForID(sessionID); harp != "" {
			if _, herr := healCanonicalTranscript(ctx, harp, true); herr != nil {
				// The stored transcript may still be readable; a refresh failure
				// costs freshness, not the recovery. It does cost the right to
				// call the cached essence current, though — see the cache branch.
				refreshFailed = true
				clidiag.Warn("ctxloom", "refresh canonical transcript for %s: %v", harp, herr)
			}
		}
	}

	session, err := source.GetSession(ctx, sessionID)
	if err != nil {
		// Degrade a lookup failure to a usable "couldn't load" message rather than a
		// tool error — recovery must never block the agent (CLAUDE.md).
		//
		// A harp with no captured canonical transcript is an INTERACTIVE session:
		// the structured tee cannot reach a pty, so the engine's own store is the
		// only record and has to be read back. Convert it here and retry once. This
		// used to return a message telling the user to run `ctxloom session
		// backfill <harp>` themselves — a correct instruction that arrived at the
		// worst possible moment, since the caller reaching for /recover has just
		// lost the context in which to act on it.
		var noCanon *transcript.NoCanonicalTranscriptError
		if errors.As(err, &noCanon) {
			healed, herr := healCanonicalTranscript(ctx, noCanon.Harp, policy.LiveTranscript)
			if !healed {
				return nil, &loadSessionResult{
					Loaded:  false,
					Message: noCaptureMessage(sessionID, noCanon.Harp, err, herr),
				}, nil
			}
			session, err = source.GetSession(ctx, sessionID)
		}
		if err != nil {
			return nil, &loadSessionResult{
				Loaded:  false,
				Message: fmt.Sprintf("Couldn't load session %s: %v", sessionID, err),
			}, nil
		}
	}
	if len(session.Entries) == 0 {
		return nil, &loadSessionResult{
			Loaded:  false,
			Message: fmt.Sprintf("Session %s appears to be empty.", sessionID),
		}, nil
	}

	sessionsDir := paths.ProjectSessionsDir(s.cfg.GetAppDir())

	// Resolve the harp that owns this session so its plan files
	// (~/.ctxloom/sessions/<harp>/) are read for the RIGHT session — not the
	// active one — when distilling a previous or cross-agent session, and so the
	// staleness check stats the harp's bound transcript.
	harp := sessionHarpForID(sessionID)
	transcriptPath := ""
	if harp != "" {
		if entry, _ := operations.GetSession(harp); entry != nil {
			// Prefer the canonical transcript: once captured,
			// that's the file Compact actually distilled from, so staleness must
			// compare against it, not the legacy engine file (see
			// memory.transcriptSize's matching preference).
			transcriptPath = entry.TranscriptPath
			if entry.CanonicalTranscriptPath != "" {
				transcriptPath = entry.CanonicalTranscriptPath
			}
		}
	}

	// Cached path: reuse the essence when the transcript hasn't moved past it —
	// UNLESS the cached body itself is over MaxEssenceChars. That should never
	// happen for anything Compact wrote since the size cap was added (Compact
	// now refuses to save an oversized body at all), but an essence written by
	// an older binary, or by some other writer, could still be sitting on
	// disk; treating it as unusable and falling through to a fresh
	// (now-bounded) distill is the fail-loud backstop for recover_session
	// specifically — a cache hit is exactly the path that would otherwise
	// hand back stale, oversized content with no compaction pipeline in the
	// loop at all to catch it.
	if cached, stampedSize := loadCachedDistilledSession(sessionsDir, sessionID); cached != nil {
		stale, known := sessions.TranscriptStale(transcriptPath, stampedSize)
		withinBound := len(cached.Content) <= memory.MaxEssenceChars
		// A live refresh that FAILED forfeits the cache hit. The size the
		// essence is being compared against was measured on a canonical
		// transcript this call could not bring up to date, so "the transcript
		// hasn't moved" describes the stale copy, not the session — and
		// answering was_cached=true off it is a recovery that silently covers
		// only whatever slice the last successful refresh captured.
		if withinBound && !refreshFailed && ((known && !stale) || (!known && !policy.RedistillWhenUnknown)) {
			return nil, cached, nil
		}
		// stale, oversized, unrefreshable, or indeterminate with a re-distill
		// bias: fall through.
	}

	// workDir (resolved above) feeds CompactionConfig for compatibility; the
	// gRPC transcript read is self-situated and ignores it.
	result, err := s.distillSession(ctx, sessionID, backendName, model, workDir, sessionsDir, harp)
	return nil, result, err
}

// healCanonicalTranscript converts harp's vendor-native transcript into the
// canonical one the session readers expect, so a caller never has to run
// a manual vendor-transcript import first.
//
// live selects WHICH conversion. A finished session gets the idempotent one,
// which is a no-op once a canonical transcript exists; a live session gets the
// refreshing one, which replaces it, because its source keeps growing. Reported
// as (converted, err) rather than swallowed: the caller distinguishes "nothing
// could be converted" (an unregistered backend, no locatable vendor store) from
// "conversion was attempted and failed", and those need different messages.
func healCanonicalTranscript(ctx context.Context, harp string, live bool) (bool, error) {
	if harp == "" {
		return false, nil
	}
	entry, err := operations.GetSession(harp)
	if err != nil || entry == nil {
		return false, err
	}
	if live {
		return operations.RefreshVendorTranscript(ctx, *entry)
	}
	return operations.ConvertVendorTranscript(ctx, *entry)
}

// noCaptureMessage explains a session that could not be read even after a
// conversion was attempted on the caller's behalf.
//
// It deliberately does NOT tell the reader to import the transcript by hand:
// that is the command just tried for them, so repeating it as advice would send
// someone to re-run the thing that already failed. What is actionable is WHY —
// an engine ctxloom has no reader for, or a vendor store that is not where the
// index says.
func noCaptureMessage(sessionID, harp string, readErr, convertErr error) string {
	if convertErr != nil {
		return fmt.Sprintf(
			"No transcript could be read for session %s (harp %q): converting this engine's own transcript failed: %v",
			sessionID, harp, convertErr)
	}
	return fmt.Sprintf(
		"No transcript was captured for session %s (harp %q): %v. Nothing could be "+
			"converted from the engine's own store either — this engine has no "+
			"transcript reader in ctxloom, or its transcript is not where the session "+
			"index records it.",
		sessionID, harp, readErr)
}

// sessionHarpForID resolves an id the session tools accept in EITHER spelling
// — a harp, or the backend-native session id bound to one — to the harp that
// owns it, or "" when it names neither.
//
// grpc.CanonicalFallbackSource.GetSession already resolves both spellings, so
// every id-keyed lookup standing beside it has to as well. Resolving only the
// backend-native spelling (operations.HarpForSession, which matches on
// Entry.SessionID and cannot match a harp) meant a HARP-addressed call found no
// harp at all — and a harp is exactly what the callers hand over:
// transcript.CanonicalHistory.ListSessions sets meta.ID to the harp, and
// `memory list` prints it, so `memory show <that value>` and recover's own
// mtime fallback both arrive here spelled as a harp.
//
// Two things then failed, silently and in opposite directions. The live
// refresh above was skipped, so a still-growing session was served from a
// canonical transcript nobody re-read. And transcriptPath below stayed empty,
// which makes TranscriptStale report "can't tell" — under policyArchived's
// RedistillWhenUnknown=false that is a permanent cache hit, so an archived
// session's essence could never be detected as out of date at all.
func sessionHarpForID(id string) string {
	if id == "" {
		return ""
	}
	// Harp-first, matching CanonicalFallbackSource.GetSession's own ordering:
	// harps are ctxloom's three-word names and backend ids are engine-native
	// UUIDs, so the two never collide and the cheap direct lookup is safe.
	if entry, _ := operations.GetSession(id); entry != nil {
		return id
	}
	harp, _ := operations.HarpForSession(id)
	return harp
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

// singleflightDistill runs fn unless an identical distillation is already in
// flight, in which case it waits for that one and shares its result. Two things
// make the dedupe load-bearing rather than an optimization: the host's handler
// runs on the coordinator's base context, so a caller whose budget expires does
// NOT stop the distillation it started; and the natural reflex to a failed
// recover is an immediate retry. Without this, that retry re-distills every
// chunk through the LLM a second time, concurrently with the first.
//
// A nil group (bare stdio server) runs fn directly — undeduped, but correct.
func (s *ctxServer) singleflightDistill(key string, fn func() (*loadSessionResult, error)) (*loadSessionResult, error) {
	if s.distill == nil {
		return fn()
	}
	v, err, _ := s.distill.Do(key, func() (any, error) { return fn() })
	if err != nil {
		return nil, err
	}
	res, _ := v.(*loadSessionResult)
	return res, nil
}

// distillSession compacts a session and returns the freshly-distilled result.
// harp keys the session's plan files; pass "" to fall back to the active harp.
//
// Progress is deliberately NOT written to stderr here: on the host-relay path
// this runs inside the session-owning process, whose stderr is the terminal the
// harness is drawing its TUI on.
func (s *ctxServer) distillSession(ctx context.Context, sessionID, backendName, model, workDir, sessionsDir, harp string) (*loadSessionResult, error) {
	if model == "" {
		model = s.cfg.GetCompactionModel()
	}
	// Bound the work to the same budget the relay grants the caller, so one
	// wedged LLM subprocess cannot hold the singleflight entry open forever
	// and queue every later recover of this session behind it (see
	// withDistillBudget).
	ctx, cancel := withDistillBudget(ctx)
	defer cancel()
	return s.singleflightDistill(sessionID+"\x00"+backendName+"\x00"+model, func() (*loadSessionResult, error) {
		return s.distillSessionOnce(ctx, sessionID, backendName, model, workDir, sessionsDir, harp)
	})
}

// distillSessionOnce is the distillation proper, behind singleflightDistill.
func (s *ctxServer) distillSessionOnce(ctx context.Context, sessionID, backendName, model, workDir, sessionsDir, harp string) (*loadSessionResult, error) {
	// Recovery must never block the agent (CLAUDE.md): a compactor/distill failure
	// degrades to a usable "couldn't distill" message rather than a tool error.
	makeCompactor := s.compactorFactory
	if makeCompactor == nil {
		makeCompactor = memory.NewCompactor
	}
	compactor, err := makeCompactor(memory.CompactionConfig{
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

	// Read back under the key Compact actually WROTE, not the one the caller
	// passed: Compact resolves the session to its harp (result.SessionID =
	// session.ID) and saveDistilled keys the mirror off that. Reading by the
	// caller's id made a successful distillation report "couldn't read it back"
	// for every session whose vendor id differs from its harp — the essence was
	// on disk the whole time, under a name this lookup never asked for.
	distilled, err := memory.LoadDistilledSession(sessionsDir, compactResult.SessionID)
	if err != nil {
		return &loadSessionResult{Loaded: false, Message: fmt.Sprintf("Distilled session %s but couldn't read it back: %v", compactResult.SessionID, err)}, nil
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
