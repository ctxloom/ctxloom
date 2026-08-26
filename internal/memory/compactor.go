package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
	"github.com/ctxloom/ctxloom/resources"
)

const (
	// DefaultChunkTokens is the target tokens per chunk for distillation.
	DefaultChunkTokens = 8000
	// ChunkOverlapTokens is the overlap between chunks for context continuity.
	ChunkOverlapTokens = 500
	// MinOverlappingChunkTokens is derived from ChunkOverlapTokens rather than
	// chosen, and it bounds one specific hazard: chunkText advances by
	// (chunk - overlap) per step, so a chunk size only a little larger than the
	// overlap advances by almost nothing and one transcript explodes into orders
	// of magnitude more chunks — each spawning its own LLM plugin subprocess.
	// At twice the overlap the advance is at least half a chunk, which bounds
	// the chunk count at roughly twice the ideal.
	//
	// The hazard is confined to the open band (ChunkOverlapTokens,
	// MinOverlappingChunkTokens). At or below the overlap the advance goes
	// non-positive and chunkText already degrades to no overlap at all, which is
	// safe — small chunk sizes are legitimate and are left exactly as
	// configured. Only the band is corrected.
	MinOverlappingChunkTokens = ChunkOverlapTokens * 2
	// BytesPerToken is the bytes-per-token ratio, owned by internal/tokens so
	// the distillation estimate and the dry-run preview agree on one
	// heuristic. Bytes, not characters: chunkText slices by byte offset, and
	// the two readings diverge by 2-4x on non-ASCII text.
	BytesPerToken = tokens.BytesPerToken
	// distillConcurrency bounds how many chunks are distilled in parallel. Each
	// chunk distillation spawns its own LLM plugin subprocess, so this caps
	// concurrent subprocesses (and provider rate pressure) while still cutting
	// wall-clock from sum-of-chunks to roughly slowest-chunk × ceil(n/limit).
	distillConcurrency = 4

	// MaxEssenceChars is the hard ceiling, in characters, on the distilled
	// essence body Compact will save or hand back to a caller. It is the
	// backstop against a pipeline that "succeeds" (every LLM call exits 0)
	// but fails to actually COMPRESS: recover_session was once found
	// returning a ~381,000-char essence — the map step produced per-chunk
	// output that was never meaningfully smaller than its input, and the
	// reduce pass that was supposed to unify it into one bounded summary
	// either wasn't enough or (see finalCompressionPass) failed and fell back
	// to the un-reduced concatenation. Either way, the caller got something
	// indistinguishable from a raw transcript passthrough. Compact now
	// refuses to write or return a body over this bound — see its use in
	// Compact and finalCompressionPass. ~100,000 chars is ~25,000 tokens at
	// this package's BytesPerToken estimate, comfortably under the ~25k-token
	// ceiling common MCP clients (including Claude Code's own tool-result
	// cap) enforce on a single tool result, with headroom for the JSON
	// envelope wrapping it.
	MaxEssenceChars = 100_000
)

// CompactionConfig holds settings for session compaction.
type CompactionConfig struct {
	LLM   string // LLM plugin to use for distillation (default: claude-code)
	Model string // Model to use within the plugin (e.g., "haiku", "sonnet")
	// Env is the resolved LLM label's config-declared environment
	// (llm.configs.<label>.env), forwarded onto every distillation request.
	//
	// It exists because it was MISSING: runDistill built its RunOptions with no
	// Env at all, while every other RunStart caller forwards it (internal/cli/
	// run.go's llmEnvFor -> st.runEnv). llm.configs.<label>.env is the
	// documented home for a backend's credentials, so a distiller whose key
	// lived there ran unconfigured and behaved as though nothing was set —
	// silently, since an unconfigured backend does not error.
	Env             map[string]string
	Backend         string           // Backend name to read session from (e.g., "claude-code")
	ChunkSize       int              // Target tokens per chunk
	SessionID       string           // Session to compact (empty = most recent)
	WorkDir         string           // Working directory for the session
	OutputDir       string           // TEST SEAM ONLY: redirects per-rotation essences somewhere a test can read. No production caller sets it; empty files them under the harp's own segments dir (rotationEssencePath).
	HarpName        string           // Harp name for harp-dir layout writes. Empty falls back to CTXLOOM_SESSION_HARP env var so the in-LLM compact_session path still works without explicit plumbing.
	ClientFactory   pb.ClientFactory // Factory for creating LLM clients (default: pb.DefaultClientFactory())
	BackendOverride agent.Backend    // Optional: inject backend directly for testing (bypasses registry)
	// Progress receives human-readable distillation progress. It belongs to
	// the CALLER because only the caller knows whether it has anywhere safe to
	// put it: a CLI owns its terminal, while the coordinator's host-relay
	// handlers run inside the session-owning process whose stderr is the live
	// TUI the harness is drawing on. Writing progress there corrupts the
	// display, so a caller with no safe sink leaves this nil and the progress
	// is discarded.
	Progress io.Writer
	// PreloadedSession, when set, is returned directly by loadSessionToCompact
	// instead of resolving a session id through source/CurrentSession. Used by
	// container-harp distill: the SessionStart bind hook runs inside the
	// container and never reaches the host's session index, so the host only
	// knows the mounted transcript path, not a session id. The caller loads the
	// session by path itself and hands it in here.
	PreloadedSession *agent.Session
	// IncludeThinking, when true, includes agent.EntryTypeThinking entries in
	// the text handed to distillation. Default false: thinking is
	// the model's scratch work, verbose and not decision-bearing, and is
	// exactly the wrong thing to spend a compacted context window on — see
	// appendEntryText. The escape hatch exists for someone debugging a
	// reasoning failure who genuinely wants the model's route preserved in the
	// essence; it is not exposed as a CLI/MCP flag yet (deliberately deferred,
	// narrow use case — wire one if the need becomes real).
	IncludeThinking bool
}

// CompactionResult holds the result of a compaction operation.
type CompactionResult struct {
	SessionID      string
	ChunksCreated  int
	TotalTokensIn  int
	TotalTokensOut int
	DistilledPath  string
	Duration       time.Duration
	// Selection reports what selectForDistill removed before any LLM call.
	// Without it a small essence from a well-filtered transcript and one from a
	// distiller that said nothing are indistinguishable at the output.
	Selection SelectionStats
}

// Compactor handles session log compaction.
type Compactor struct {
	config        CompactionConfig
	source        pb.SessionSource
	plans         func(context.Context, string) ([]agent.PlanFile, error)
	clientFactory pb.ClientFactory
	// sourceErr records why source is nil, when the reason is a failure rather
	// than an unsupported backend. Without it every nil source reads as "this
	// backend has no session history", which for the retired-scraper backends
	// -- the default one included -- is both wrong and unactionable.
	sourceErr error
}

// memoryHistorySource adapts an in-process SessionHistory to pb.SessionSource.
// It backs the BackendOverride test seam (unit-testing compaction logic against
// a fake transcript store); production reads go over gRPC via pb.SessionReader.
type memoryHistorySource struct {
	history agent.SessionHistory
	workDir string
}

func (s memoryHistorySource) GetSession(_ context.Context, id string) (*agent.Session, error) {
	return s.history.GetSession(s.workDir, id)
}
func (s memoryHistorySource) ListSessions(_ context.Context) ([]agent.SessionMeta, error) {
	return s.history.ListSessions(s.workDir)
}
func (s memoryHistorySource) CurrentSession(_ context.Context) (*agent.Session, error) {
	return s.history.GetCurrentSession(s.workDir)
}

// NewCompactor creates a new compactor with the given config.
func NewCompactor(config CompactionConfig) (*Compactor, error) {
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultChunkTokens
	}
	// config.compaction_chunks is user-settable and is the only input the fixed
	// ChunkOverlapTokens is ever weighed against. A value inside the band is a
	// cost bomb rather than a small chunk: see MinOverlappingChunkTokens. Raise
	// it and say so, rather than silently spawning thousands of plugin
	// subprocesses for one session.
	if config.ChunkSize > ChunkOverlapTokens && config.ChunkSize < MinOverlappingChunkTokens {
		if config.Progress != nil {
			clidiag.Fwarn(config.Progress, "ctxloom",
				"compaction chunk size %d sits just above the %d-token chunk overlap, which would split one session into thousands of near-duplicate chunks; using %d",
				config.ChunkSize, ChunkOverlapTokens, MinOverlappingChunkTokens)
		}
		config.ChunkSize = MinOverlappingChunkTokens
	}
	if config.Backend == "" {
		config.Backend = "claude-code"
	}
	if config.LLM == "" {
		config.LLM = "claude-code"
	}
	if config.ClientFactory == nil {
		config.ClientFactory = pb.DefaultClientFactory()
	}

	// Resolve the transcript source. Production reads go over gRPC via the
	// agent server (pb.SessionReader) so the same path serves a remote agent and
	// ctxloom never parses backend files in-process. Tests inject a backend whose
	// in-process SessionHistory is adapted to the same SessionSource contract.
	var (
		source    pb.SessionSource
		sourceErr error
		plans     func(context.Context, string) ([]agent.PlanFile, error)
	)
	if config.BackendOverride != nil {
		if h := config.BackendOverride.History(); h != nil {
			source = memoryHistorySource{history: h, workDir: config.WorkDir}
		}
		// h == nil leaves source nil → loadSessionToCompact reports "no history".
		// Tests read plan files directly from the (isolated) session dir.
		plans = func(_ context.Context, harp string) ([]agent.PlanFile, error) {
			return pb.ReadPlanFiles(harp), nil
		}
	} else {
		reader := pb.NewSessionReaderWithFactory(config.Backend, 0, config.ClientFactory)
		// GetPlans reads *.plan.md straight from the harp's ctxloom session dir
		// (internal/lm/grpc/plans.go) — it never touches Backend.History(), so it
		// works identically whether or not config.Backend still has a legacy
		// scraper reader.
		plans = reader.GetPlans
		// S4: prefer ctxloom's own captured transcript over the
		// legacy per-engine scraper reader now behind it. A session-index open
		// failure (rare — a corrupt/unwritable ~/.ctxloom/sessions/index.yaml)
		// degrades to the legacy-only reader rather than failing compaction
		// outright; distillation must never block on the canonical layer.
		//
		// S5: config.Backend may be a retired-scraper backend
		// (codex/kiro/claude-code — scraper deleted outright). Such a
		// backend's plugin-side History() is now nil, so `reader` used as the
		// legacy leg would only ever error; pass nil instead so
		// CanonicalFallbackSource serves canonical-only and never makes that
		// doomed round trip.
		var legacy pb.SessionSource
		if !pb.IsRetiredScraperBackend(config.Backend) {
			legacy = reader
		}
		store, sErr := sessions.Open("")
		switch {
		case sErr == nil:
			source = pb.NewCanonicalFallbackSource(legacy, config.WorkDir, store)
		case legacy != nil:
			source = legacy
		default:
			// A retired-scraper backend has no legacy leg, so the canonical
			// layer is the ONLY transcript source and its index failing to open
			// is the whole reason there is nothing to read. Carry that reason:
			// the alternative is telling the user their backend does not support
			// session history, which is a different problem with a different
			// remedy and is not what happened.
			sourceErr = fmt.Errorf("session index unavailable: %w", sErr)
		}
	}

	return &Compactor{
		config:        config,
		source:        source,
		plans:         plans,
		clientFactory: config.ClientFactory,
		sourceErr:     sourceErr,
	}, nil
}

// Compact performs compaction on a session.
func (c *Compactor) Compact(ctx context.Context) (*CompactionResult, error) {
	start := time.Now()
	result := &CompactionResult{}

	session, err := c.loadSessionToCompact(ctx)
	if err != nil {
		return nil, err
	}
	result.SessionID = session.ID
	harpName := c.resolveHarpName()

	// Stamp the source transcript's byte size now — right after the entries were
	// read and before the slow distill — so the fingerprint best matches the
	// content actually distilled. If the live session appends during the distill,
	// the next staleness check sees live > stamped and correctly re-distills.
	sourceSize := transcriptSize(harpName)

	// Plans are the session's own .plan.md documents, read from its ctxloom
	// session directory and served by the agent server — not mined from the
	// transcript. They bypass the LLM compression pass and are re-attached
	// verbatim. Best-effort: a retrieval failure warns and omits them.
	planFiles, err := c.plans(ctx, harpName)
	if err != nil {
		c.warnf("plan retrieval failed, omitting plan blocks: %v", err)
	}
	plans := planFilesToBlocks(planFiles)

	// Convert entries to text for chunking. Plans live in separate files now, so
	// there are no in-transcript plan blocks to placeholder out.
	sel := selectForDistill(session.Entries)
	if n := c.repairResults(ctx, sel); n > 0 {
		c.progressf("ctxloom: recovered %d finding(s) from uncommented results...\n", n)
	}
	logText := c.renderEntries(sel.Entries)
	result.Selection = sel.Stats
	result.TotalTokensIn = tokens.Estimate(logText)

	// A session with zero main-thread entries has nothing to
	// distill — see isEmptySession for why "zero entries" (not a byte/token
	// floor) is the bright line. Skip the whole chunk/map/reduce pipeline
	// (no plugin subprocess spawned at all) and persist a trivial dump
	// instead, so the picker/resume flow still finds a valid essence.
	if isEmptySession(session.Entries) || rendersToNothing(logText) {
		return c.dumpEmptySession(session, harpName, sourceSize, plans, result, start)
	}

	// Chunk the log, distill each chunk, then optionally re-compress.
	chunks := chunkText(logText, c.config.ChunkSize)
	result.ChunksCreated = len(chunks)

	distilled, failedChunks, chunkCause := c.distillChunks(ctx, chunks)
	// A totally failed distillation (e.g. LLM backend down) would replace a
	// previously good essence with nothing but failure markers — that's data
	// loss, not graceful degradation. Abort the save and keep the old essence.
	// Partial success still saves, per the fault-tolerance philosophy.
	if len(chunks) > 0 && failedChunks == len(chunks) {
		c.warnf("distillation failed for all %d chunks; keeping previous essence", len(chunks))
		return nil, fmt.Errorf("distillation failed for all %d chunks: %w", len(chunks), chunkCause)
	}
	if failedChunks > 0 {
		c.warnf("distillation failed for %d of %d chunks; summary is incomplete", failedChunks, len(chunks))
	}

	combined := strings.Join(distilled, "\n\n---\n\n")
	result.TotalTokensOut = tokens.Estimate(combined)

	// Any multi-chunk session needs the reduce pass: it unifies the concatenated
	// per-chunk summaries into one canonical essence (YAML frontmatter + the
	// "### Open Items" section the picker derives its summary and detail lines
	// from). Gating it on size left small multi-chunk sessions with raw map
	// output — no frontmatter, no Open Items. Single-chunk sessions already
	// produce one canonical map output, so they skip it.
	if len(chunks) > 1 {
		reduced, rerr := c.finalCompressionPass(ctx, combined)
		if rerr != nil {
			// finalCompressionPass only returns an error when the reduce call
			// failed AND its un-reduced input was itself over MaxEssenceChars
			// (see its doc comment) — a genuine "cannot bound the output"
			// case, not a normal degrade. Fail loud rather than save/return
			// the oversized, un-reduced combined text.
			return nil, rerr
		}
		combined = reduced
		result.TotalTokensOut = tokens.Estimate(combined)
	}

	// Pull the LLM-emitted YAML frontmatter (Phase 3.5.2). If it's
	// missing/malformed, fall through with empty summary: the picker
	// shows "(no summary)" and the user can re-run distill on demand.
	summary, cleanedBody, hadFM := parseLLMFrontmatter(strings.TrimSpace(combined))
	if !hadFM {
		c.warnf("distillation lacks YAML frontmatter; deriving summary from body")
		cleanedBody = strings.TrimSpace(combined)
	}

	// Fail-loud backstop: even a "successful" pipeline (every
	// LLM call exited 0, reduce ran without error) can still hand back
	// something enormous if the model didn't actually compress — this is the
	// general case finalCompressionPass's own check above only covers for its
	// specific failure mode. Never save or return a body over the bound; the
	// caller (e.g. recover_session) must see an honest failure instead of an
	// unbounded payload.
	if len(cleanedBody) > MaxEssenceChars {
		return nil, fmt.Errorf("distilled essence for session %s is %d chars, over the %d-char bound (MaxEssenceChars); refusing to save or return an unbounded summary", session.ID, len(cleanedBody), MaxEssenceChars)
	}

	return c.finishDistill(session, harpName, sourceSize, plans, result, summary, cleanedBody, start)
}

// isEmptySession reports whether a session has no main-thread content at all
// (zero entries — loadSessionToCompact has already dropped sidechain entries
// via agent.MainThreadEntries, so this is the post-filter count). Zero
// entries is the bright line, not a byte/token floor: a genuinely tiny but
// real exchange — a single "hello" with no reply (TestCompact_
// DeliversSystemPromptUnderSkipSetup), or a two-line "Hello, how are you?" /
// "I'm doing well" round trip (TestCompact_WithMockClient) — renders to well
// under 20 estimated tokens, so any threshold generous enough to spare those
// real conversations would spare essentially everything; it would not be a
// usable "skip the pipeline" signal. "Zero entries" has no such false
// positive: nothing was ever said, so there is nothing to condense, and
// spawning a plugin subprocess (map) plus a reduce pass to summarize an
// empty transcript is pure waste — one of the surfaces plausibly implicated
// in tart-aqua's final-pass timeout.
func isEmptySession(entries []agent.SessionEntry) bool {
	return len(entries) == 0
}

// rendersToNothing reports whether the text sessionToText produced for the
// session is empty — the same "nothing was ever said" state isEmptySession
// describes, reached with entries present.
//
// This is NOT the byte/token floor isEmptySession argues against, and the
// distinction is the whole point: a floor would have to guess how small a real
// conversation can be, while this is exact. Entries can render to nothing for
// reasons that have nothing to do with how much was said — a session whose only
// main-thread entries are `thinking`, which appendEntryText suppresses by
// policy, or entries carrying a type this renderer has no case
// for. Without this check the pipeline chunks the empty string into one chunk
// and spawns an LLM plugin subprocess to summarize a transcript containing
// nothing, then writes whatever the model invents over the session's essence.
func rendersToNothing(logText string) bool {
	return strings.TrimSpace(logText) == ""
}

// emptySessionPlaceholder is the body written for a session with zero
// main-thread entries, so the saved essence is never a literal empty string
// (a blank file would look indistinguishable from a write failure to a
// human skimming ~/.ctxloom/sessions/<harp>/essence.md).
const emptySessionPlaceholder = "_(empty session — no conversation content to distill)_"

// dumpEmptySession is the short-circuit for isEmptySession: it skips
// chunking, per-chunk map distillation, and the reduce pass entirely — no
// LLM plugin subprocess is spawned — and persists a trivial but valid
// essence (any plan files still re-attached verbatim) via the same
// saveDistilled/updateSessionIndex plumbing normal distillation uses, so a
// later `session list` / resume picker sees a well-formed entry rather than
// a hole. Returns success: an empty session is not a failure, just nothing
// to compact.
func (c *Compactor) dumpEmptySession(session *agent.Session, harpName string, sourceSize int64, plans []PlanBlock, result *CompactionResult, start time.Time) (*CompactionResult, error) {
	label := harpName
	if label == "" {
		label = session.ID
	}
	// An empty session must never REPLACE work that was already distilled. The
	// dump writes a 54-byte placeholder through the same atomic saveDistilled
	// the real pipeline uses, and deriveSummary turns that placeholder into a
	// non-empty summary, so it also overwrote the index summary. Re-distills
	// are automatic (the staleness path) and a session can read as empty for
	// reasons that have nothing to do with its essence — a reaped transcript,
	// an all-sidechain log — so this fired on real, populated sessions.
	//
	// Keeping the existing essence is the right outcome, not an error: an empty
	// session is still not a failure, there is simply nothing better to write.
	if path, ok := c.existingEssence(session.ID, harpName); ok {
		c.warnf("session %s is empty but already has a distilled essence; keeping %s", label, path)
		result.ChunksCreated = 0
		result.TotalTokensOut = result.TotalTokensIn
		result.DistilledPath = path
		result.Duration = time.Since(start)
		return result, nil
	}

	c.progressf("ctxloom: session %s empty — dumped without distillation\n", label)

	result.ChunksCreated = 0
	result.TotalTokensOut = result.TotalTokensIn // verbatim dump: no compression ran

	return c.finishDistill(session, harpName, sourceSize, plans, result, "", emptySessionPlaceholder, start)
}

// rotationEssencePath returns where THIS session's per-rotation essence lives:
// ~/.ctxloom/sessions/<harp>/segments/<sessionID>.md, beside that rotation's
// canonical segment. A caller's OutputDir overrides the directory, which is how
// a test pins the write somewhere it can read.
//
// It needs a harp because the essence is harp-owned: a rotation is a step in
// ONE harp's lineage, and there is no meaningful place to file a rotation of no
// harp. The empty-harp case returns an error rather than falling back to a
// project directory — a second home for the same fact, keyed differently, that
// nothing reconciles.
func (c *Compactor) rotationEssencePath(harpName, sessionID string) (string, error) {
	if c.config.OutputDir != "" {
		return filepath.Join(c.config.OutputDir, sessionID+".md"), nil
	}
	if harpName == "" {
		return "", fmt.Errorf("no harp for session %s: a distilled essence is filed under its harp's lineage, so one must be bound before distilling", sessionID)
	}
	return paths.ResolveHarpSegmentEssencePath(harpName, sessionID)
}

// existingEssence reports whether a distilled essence already exists for this
// session, checking the harp's current essence first (the primary write target)
// and then this rotation's own essence under segments/, mirroring
// saveDistilled's own precedence so the two can't disagree about where the
// essence lives.
func (c *Compactor) existingEssence(sessionID, harpName string) (string, bool) {
	if harpName != "" {
		if essencePath, err := paths.HarpEssencePath(harpName); err == nil {
			if st, err := os.Stat(essencePath); err == nil && st.Size() > 0 {
				return essencePath, true
			}
		}
	}
	rotationPath, err := c.rotationEssencePath(harpName, sessionID)
	if err != nil {
		return "", false
	}
	if st, err := os.Stat(rotationPath); err == nil && st.Size() > 0 {
		return rotationPath, true
	}
	return "", false
}

// finishDistill assembles the picker summary + Open-Items detail, re-attaches
// plan blocks, and persists the distilled artifact plus session-index entry.
// Shared by the normal compaction path (cleanedBody is the LLM's combined,
// possibly-reduced output) and dumpEmptySession (cleanedBody is the trivial
// placeholder) so both produce an identically-shaped on-disk essence.
func (c *Compactor) finishDistill(session *agent.Session, harpName string, sourceSize int64, plans []PlanBlock, result *CompactionResult, frontmatterSummary, cleanedBody string, start time.Time) (*CompactionResult, error) {
	// Fall back to the first prose line when there's no frontmatter summary,
	// so a distilled session never renders as "(no summary)" in the picker.
	summary := deriveSummary(frontmatterSummary, cleanedBody)

	// Extra picker lines: the leading Open Items, so a resume row shows "what +
	// what's left" instead of a lone subject. Derived from the body before
	// plan blocks are re-attached.
	detail := buildPickerDetail(cleanedBody)

	body := assembleBody(cleanedBody, plans)

	// Save distilled output
	distilledPath, err := c.saveDistilled(session.ID, body, distilledMeta{
		EntryCount: len(session.Entries),
		TokensIn:   result.TotalTokensIn,
		TokensOut:  result.TotalTokensOut,
		PlanBlocks: len(plans),
		Summary:    summary,
		HarpName:   harpName,
		SourceSize: sourceSize,
	})
	if err != nil {
		return nil, fmt.Errorf("save distilled: %w", err)
	}
	result.DistilledPath = distilledPath

	c.updateSessionIndex(harpName, session.ID, summary, detail, sourceSize)

	result.Duration = time.Since(start)
	return result, nil
}

// loadSessionToCompact resolves the session to compact — the configured
// SessionID if set, else the current session — and rejects the true
// nothing-to-do cases (no backend history support, no session found). An
// empty session (found, but zero main-thread entries) is NOT rejected here:
// it's a valid session for Compact to short-circuit to a dump via
// isEmptySession, not a lookup failure.
func (c *Compactor) loadSessionToCompact(ctx context.Context) (*agent.Session, error) {
	if c.source == nil {
		if c.sourceErr != nil {
			return nil, c.sourceErr
		}
		return nil, fmt.Errorf("backend %q does not support session history", c.config.Backend)
	}

	if c.config.PreloadedSession != nil {
		return c.config.PreloadedSession, nil
	}

	explicitSessionID := c.config.SessionID != ""
	sessionID := c.config.SessionID
	if !explicitSessionID {
		// Identity-first: when a harp is known, its session-index binding is the
		// exact transcript for the current session — set once at bind time and
		// never touched again. CurrentSession's mtime-newest pick is unreliable
		// here because a backend rewrites/touches a transcript file when a
		// session is resumed, so "newest by mtime" is not reliably "the session
		// that just ended". Only fall back to CurrentSession when
		// there's no harp (e.g. a bare `ctxloom memory compact` outside any
		// tracked session) or it isn't bound yet (bind hook never fired).
		sessionID = c.identityBoundSessionID()
	}

	var session *agent.Session
	var err error
	if sessionID != "" {
		session, err = c.source.GetSession(ctx, sessionID)
		if err != nil {
			if explicitSessionID {
				// The caller (e.g. `session distill <harp>`) asked for exactly
				// this session — never silently substitute another one.
				return nil, fmt.Errorf("get session %s: %w", sessionID, err)
			}
			// The identity-bound id came from the index, not the caller, and its
			// transcript no longer exists (rotated/deleted — a stale index
			// entry). Recovery must never block (CLAUDE.md fault tolerance): fall
			// through to CurrentSession, the same genuine last resort the
			// empty-SessionID path always used before identity-first binding was
			// added.
			session, err = c.source.CurrentSession(ctx)
			if err != nil {
				return nil, fmt.Errorf("get current session: %w", err)
			}
		}
	} else {
		session, err = c.source.CurrentSession(ctx)
		if err != nil {
			return nil, fmt.Errorf("get current session: %w", err)
		}
	}

	if session == nil {
		return nil, fmt.Errorf("no session found")
	}
	// Distillation reflects the conversation the user had: subagent-interior
	// (sidechain) entries are attribution data for viewers, not essence input.
	// A session with zero main-thread entries (including an all-sidechain
	// session, which filters down to none) is not an error here:
	// Compact's isEmptySession check short-circuits it to a plain dump rather
	// than failing, since "nothing to distill" is not "nothing was found".
	session.Entries = agent.MainThreadEntries(session.Entries)
	return session, nil
}

// progressf reports distillation progress to the caller's sink, or nowhere
// when the caller supplied none. Best-effort: progress never blocks or fails
// the distillation. The line is formatted into ONE buffer and emitted as a
// single Write, so concurrent chunk progress doesn't interleave mid-line.
func (c *Compactor) progressf(format string, args ...any) {
	if c.config.Progress == nil {
		return
	}
	_, _ = io.WriteString(c.config.Progress, fmt.Sprintf(format, args...))
}

// warnf reports a non-fatal degradation to the SAME caller-owned sink as
// progress, and for the same reason: clidiag.Warn writes to the process's
// stderr, which on the host-relay path is the terminal the harness is drawing
// its TUI on. Every degradation the compactor reports here is also carried in
// the result the caller gets back — failed chunks as inline markers, a failed
// final pass as the unreduced body — so a caller with no sink loses a
// convenience, not information.
func (c *Compactor) warnf(format string, args ...any) {
	if c.config.Progress == nil {
		return
	}
	clidiag.Fwarn(c.config.Progress, "ctxloom", format, args...)
}

// distillChunks distills the chunks concurrently (bounded by distillConcurrency)
// and returns the outputs in chunk order, how many chunks failed, and a
// representative cause when any did (nil otherwise). Chunks are
// independent — the overlap between them is context padding, not a data
// dependency — so they distill in parallel; results are written into their own
// slice slots so order is preserved regardless of completion order. Per CLAUDE.md
// fault tolerance, a failed chunk is warned and replaced with an HTML-comment
// marker rather than aborting; the caller decides whether a total failure aborts
// the save. A failing chunk does NOT cancel its siblings.
func (c *Compactor) distillChunks(ctx context.Context, chunks []string) ([]string, int, error) {
	distilled := make([]string, len(chunks))
	// Per-chunk causes, kept by index. The aggregate "all N chunks failed"
	// error is the only thing most callers ever see, and a cause-free one is
	// undiagnosable: warnf is the sole other channel and it is silently
	// dropped whenever Progress is nil, which is exactly the MCP relay case.
	causes := make([]error, len(chunks))
	var failed atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, distillConcurrency)
	total := len(chunks)

	for i, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{} // acquire a slot; blocks once distillConcurrency are in flight
		go func(i int, chunk string) {
			defer wg.Done()
			defer func() { <-sem }()
			c.progressf("ctxloom: compacting chunk %d/%d...\n", i+1, total)
			out, err := c.distillChunk(ctx, chunk, i+1, total)
			if err != nil {
				c.warnf("chunk %d failed: %v", i+1, err)
				distilled[i] = fmt.Sprintf("<!-- Chunk %d failed: %v -->", i+1, err)
				causes[i] = err
				failed.Add(1)
				return
			}
			distilled[i] = out
		}(i, chunk)
	}
	wg.Wait()
	// Lowest-indexed cause, not first-to-fail: a representative error that is
	// the same on every re-run of the same failure, so the message a user
	// reports is the message the next reader reproduces.
	var cause error
	for i, e := range causes {
		if e != nil {
			cause = fmt.Errorf("chunk %d: %w", i+1, e)
			break
		}
	}
	return distilled, int(failed.Load()), cause
}

// finalCompressionPass merges the per-chunk distillations into one coherent
// essence using the dedicated reduce prompt (which knows its input is already-
// distilled partial summaries to unify, not a raw transcript to re-summarize,
// and re-asserts the mandatory YAML frontmatter + identifier preservation).
//
// On failure, the old behavior was to warn and return combined unchanged — "a
// too-large summary beats no summary". That is exactly the fail-open
// bug: the reduce pass is the ONE step that unifies a multi-chunk session into
// a bounded essence, so falling back to its un-reduced input on failure means
// falling back to something that can be arbitrarily large (roughly
// chunk-count × per-chunk-summary-size). That reasoning still holds when
// combined is small — a failed reduce on a short summary is still worth
// returning unreduced — so the fallback stays, but only up to MaxEssenceChars.
// Past that, returning combined would just be a slower-motion version of the
// same passthrough, so this fails loud instead.
func (c *Compactor) finalCompressionPass(ctx context.Context, combined string) (string, error) {
	c.progressf("ctxloom: final compression pass...\n")
	final, err := c.runDistill(ctx, sessionDistillReducePrompt, combined)
	if err != nil {
		if len(combined) > MaxEssenceChars {
			return "", fmt.Errorf("final compression pass failed (%w) and the un-reduced input is %d chars, over the %d-char bound (MaxEssenceChars) — refusing to fall back to an unbounded summary", err, len(combined), MaxEssenceChars)
		}
		c.warnf("final pass failed, using combined: %v", err)
		return combined, nil
	}
	return final, nil
}

// assembleBody re-attaches the verbatim plan blocks after the LLM summary so
// they survive distillation unmodified, with a blank line between when both are
// present.
func assembleBody(cleanedBody string, plans []PlanBlock) string {
	body := cleanedBody
	if section := RenderPlans(plans); section != "" {
		if body != "" {
			body += "\n\n"
		}
		body += section
	}
	return body
}

// resolveHarpName returns the harp name keying picker/index entries: the config
// field wins over the CTXLOOM_SESSION_HARP env var so `ctxloom session distill
// <harp>` can override without mutating process env.
func (c *Compactor) resolveHarpName() string {
	// An explicit SessionID naming a DIFFERENT, genuinely-existing
	// harp than the caller's own must route output (essence, index bind,
	// summary) to THAT harp — otherwise compacting someone else's session
	// silently writes the essence into, and overwrites the summary of, the
	// CALLER's own harp entry. Checked against the session index rather than
	// harp.Validate's shape rule: harp.Validate is deliberately permissive
	// (any single path component passes), so it cannot tell a real harp name
	// apart from an opaque engine-native session id — only "does this
	// identify a harp that actually exists" can. An id that doesn't resolve
	// to an existing harp (a native session id, a typo, a bare `ctxloom
	// memory compact` with no coordinator context) falls back to the
	// caller's own harp exactly as before this fix.
	if c.config.SessionID != "" && c.config.HarpName != "" && c.config.SessionID != c.config.HarpName {
		if mgr, err := sessions.Open(""); err == nil {
			if entry, _ := mgr.Find(c.config.SessionID); entry != nil {
				return c.config.SessionID
			}
		}
	}
	if c.config.HarpName != "" {
		return c.config.HarpName
	}
	return os.Getenv("CTXLOOM_SESSION_HARP")
}

// identityBoundSessionID returns the session id bound to this compactor's harp
// in the session index, or "" when there is no harp, no index entry, the entry
// isn't bound yet (SessionStart hasn't fired), or the entry's recorded backend
// doesn't match the one this compactor is reading from (a bound id is only
// valid within its own backend's transcript store). Best-effort: an index read
// failure degrades to "" so distillation still proceeds via the mtime-based
// CurrentSession fallback rather than erroring — recovery must never block
// (CLAUDE.md fault tolerance).
func (c *Compactor) identityBoundSessionID() string {
	harpName := c.resolveHarpName()
	if harpName == "" {
		return ""
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return ""
	}
	entry, _ := mgr.Find(harpName)
	if entry == nil || entry.SessionID == "" {
		return ""
	}
	if entry.Backend != "" && entry.Backend != c.config.Backend {
		return ""
	}
	return entry.SessionID
}

// updateSessionIndex best-effort records the session ID against the harp (so a
// later `ctxloom session distill <harp>` finds the transcript) and updates the
// picker summary, detail lines, and source-size staleness fingerprint. No-op
// without a harp name; all failures warn, never fatal.
func (c *Compactor) updateSessionIndex(harpName, sessionID, summary string, detail []string, sourceSize int64) {
	if harpName == "" {
		return
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return
	}
	// The bind goes to sessions.Manager rather than operations.BindSession
	// deliberately: routing it through the operations façade would make this
	// domain package depend on the orchestration layer above it (and on
	// everything that layer pulls in). Manager.BindSession re-checks
	// first-bind-wins under the index file lock, so the façade's caller-side
	// guards are defence in depth here, not the thing preventing a clobber.
	// The ONE guard that is not redundant is the read failure below.
	entry, ferr := mgr.Find(harpName)
	switch {
	case ferr != nil:
		// A transient index-read failure is otherwise indistinguishable from
		// "no entry for this harp", and both fall through to no bind at all.
		// The bind is first-bind-wins and is never retried, so a harp that
		// misses it has no session id for the rest of its life and every later
		// distill/resume fails with "no session bound".
		c.warnf("read session index for %s: %v (session id not recorded)", harpName, ferr)
	case entry != nil && entry.SessionID == "":
		if err := mgr.BindSession(harpName, sessionID, ""); err != nil {
			c.warnf("index bind failed: %v", err)
		}
	}
	// Guarded on a non-empty summary so a failed distill (no frontmatter) never
	// clobbers a previously good summary; the size fingerprint rides along with
	// it. The essence.md frontmatter carries SourceSize unconditionally, so the
	// authoritative staleness check (loadOrDistillSession) works even here.
	if summary != "" {
		if err := mgr.SetSummary(harpName, summary, detail, sourceSize); err != nil {
			c.warnf("index summary update failed: %v", err)
		}
	}
}

// transcriptSize returns the byte size of the harp's bound transcript, or 0 when
// it can't be determined (no harp, no bound path, or stat failure). This is the
// staleness fingerprint stamped into the essence and index: `session list`, the
// resume picker, and loadOrDistillSession stat the same TranscriptPath and flag
// the essence out of date once the transcript has grown past it. Best-effort and
// read-only per the fault-tolerance philosophy — an unresolvable path degrades
// to "no fingerprint", never an error.
func transcriptSize(harpName string) int64 {
	if harpName == "" {
		return 0
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return 0
	}
	entry, _ := mgr.Find(harpName)
	if entry == nil {
		return 0
	}
	// Prefer the canonical transcript's size (S4): once a harp has
	// one, that is the file Compact actually distilled from (NewCompactor's
	// CanonicalFallbackSource), so the staleness fingerprint must be stamped
	// against IT, not the legacy engine file — otherwise Entry.SourceStale
	// compares the essence to a source it was never distilled from.
	path := entry.CanonicalTranscriptPath
	if path == "" {
		path = entry.TranscriptPath
	}
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		// Unlike the branches above (no harp, no index, no path
		// bound at all — all ordinary "nothing to fingerprint" states), a
		// BOUND path that can't be stat'd is a real, surprising degradation
		// (deleted/rotated/permission-denied). Silently returning 0 here
		// permanently zeroed the staleness fingerprint with no diagnostic —
		// warn, naming the harp and path, so an operator has something to
		// act on.
		clidiag.Warn("ctxloom", "transcript size: harp %q: stat %s: %v — staleness fingerprint stamped as 0", harpName, path, err)
		return 0
	}
	return info.Size()
}

// maxPickerDetailLines caps the Open Items shown under a picker row. With the
// subject line that's up to 5 lines per session, enough to disambiguate a
// resume target without the picker growing unwieldy.
const maxPickerDetailLines = 4

// buildPickerDetail extracts the leading bullets of the body's "### Open Items"
// section as extra picker lines (the "what's left to do" the resume picker cares
// about most). Each returned line is normalized to a single line capped at 80
// bytes; the "- " bullet marker is preserved for readability. Returns nil when
// the body has no Open Items section.
func buildPickerDetail(body string) []string {
	lines := strings.Split(body, "\n")
	inOpen := false
	var detail []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			// A heading ends the Open Items section once we're inside it; before
			// that, look for the Open Items heading specifically.
			if inOpen {
				break
			}
			if strings.Contains(strings.ToLower(t), "open items") {
				inOpen = true
			}
			continue
		}
		if !inOpen || t == "" {
			continue
		}
		if !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") {
			continue
		}
		detail = append(detail, firstLineSummary(t))
		if len(detail) >= maxPickerDetailLines {
			break
		}
	}
	return detail
}

// sessionToText converts a session to readable text for distillation. Plans
// live in separate .plan.md files (re-attached verbatim from RenderPlans), so
// the transcript text is rendered straight through.
func (c *Compactor) sessionToText(session *agent.Session) (string, SelectionStats) {
	sel := selectForDistill(session.Entries)
	return c.renderEntries(sel.Entries), sel.Stats
}

// renderEntries writes already-selected entries as the text handed to
// distillation. Split from selection so the LLM repair pass (repairResults)
// has somewhere to sit between the two: it rewrites entries, and rendering
// must see the rewritten ones.
func (c *Compactor) renderEntries(entries []agent.SessionEntry) string {
	var builder strings.Builder
	for _, entry := range entries {
		appendEntryText(&builder, entry, c.config.IncludeThinking)
	}
	return builder.String()
}

// repairResults recovers a finding for each large tool result the agent never
// commented on, and writes it into the entry in place of the excerpt.
//
// Bounded by the same concurrency limit as chunk distillation, because each
// repair spawns its own plugin subprocess. A repair that fails leaves the
// entry exactly as selection rendered it -- shape line plus excerpt -- so the
// worst case is the deterministic behaviour, never an empty result. That is
// why no error is returned: there is no failure mode here that should abort a
// compaction which would otherwise succeed.
func (c *Compactor) repairResults(ctx context.Context, sel Selection) int {
	if len(sel.Repairs) == 0 {
		return 0
	}
	var recovered atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, distillConcurrency)

	for _, r := range sel.Repairs {
		wg.Add(1)
		sem <- struct{}{}
		go func(r ResultRepair) {
			defer wg.Done()
			defer func() { <-sem }()
			finding, err := c.recoverFinding(ctx, r)
			if err != nil {
				c.warnf("finding recovery failed for %s: %v", r.ToolName, err)
				return
			}
			if finding == "" {
				return
			}
			sel.Entries[r.Index].ToolOutput = resultShape(r.Body) + " recovered finding: " + finding
			recovered.Add(1)
		}(r)
	}
	wg.Wait()
	return int(recovered.Load())
}

// recoverFinding asks the fast model what one unreflected result told the
// agent. An answer of "no conclusion available" -- the prompt's own escape
// hatch -- is reported as no finding, so a model with nothing to say leaves
// the deterministic excerpt standing rather than replacing it with a
// confident nothing.
func (c *Compactor) recoverFinding(ctx context.Context, r ResultRepair) (string, error) {
	content := fmt.Sprintf("<tool_call>\n%s %s\n</tool_call>\n\n<tool_result>\n%s\n</tool_result>",
		r.ToolName, r.Intent, r.Body)
	out, err := c.runDistill(ctx, resultFindingPrompt, content)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if strings.EqualFold(out, noConclusionAvailable) {
		return "", nil
	}
	return out, nil
}

// truncateForSummary caps an argument/output string at 500 bytes with an
// ellipsis, keeping summary text compact. Ellipsize reserves the ellipsis from
// the budget, so 500 is the real cap rather than 500 plus three.
func truncateForSummary(s string) string {
	return textutil.Ellipsize(s, 500)
}

// appendEntryText renders one session entry as a markdown section.
//
// EntryTypeThinking is deliberately excluded by default (the
// switch below has no case for it unless includeThinking is set, so it falls
// through the switch and contributes nothing). This used to be an accident of
// the switch simply predating the entry type; it is now an explicit policy.
// Thinking is the model's scratch work — verbose, and the model talking to
// itself — not the conclusions a compacted context should spend tokens on.
// Suppressing it here (at SELECTION time, in distill/compact) rather than at
// capture time is deliberate: the canonical transcript is the durable record
// cross-engine resume reads, and dropping thinking there would be an
// unrecoverable loss the IR2/IR3 fidelity work explicitly guards against.
// includeThinking (CompactionConfig.IncludeThinking) is the debugging escape
// hatch for someone who wants the model's reasoning preserved in the essence.
func appendEntryText(builder *strings.Builder, entry agent.SessionEntry, includeThinking bool) {
	switch entry.Type {
	case agent.EntryTypeUser:
		builder.WriteString("## User\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case agent.EntryTypeAssistant:
		builder.WriteString("## Assistant\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case agent.EntryTypeThinking:
		if !includeThinking {
			return
		}
		builder.WriteString("## Thinking\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case agent.EntryTypeToolUse:
		_, _ = fmt.Fprintf(builder, "## Tool Call: %s\n", entry.ToolName)
		if len(entry.ToolInput) > 0 {
			_, _ = fmt.Fprintf(builder, "Arguments: %s\n", truncateForSummary(renderToolArgs(entry.ToolInput)))
		}
		builder.WriteString("\n")

	case agent.EntryTypeToolResult:
		_, _ = fmt.Fprintf(builder, "## Tool Result: %s\n", entry.ToolName)
		// An ERROR keeps its body whole: errors measured at a fraction of a
		// percent of transcript bytes while carrying the highest signal per
		// byte in the file -- what broke, and what the fix had to answer to.
		//
		// Every other result is reduced to its SHAPE. A uniform truncation of
		// the body cost roughly a quarter of the rendered transcript to
		// deliver severed fragments -- half a table, the first lines of a
		// grep -- which is neither the information nor a summary of it. The
		// shape line is smaller AND says more, because "47 lines" is a fact
		// where the first 500 bytes of 47 lines is an artifact. The content
		// itself is re-derivable: the call above names what was asked.
		if entry.IsError {
			builder.WriteString(truncateForSummary(entry.ToolOutput))
			builder.WriteString(" [ERROR]")
		} else {
			// selectForDistill already reduced this to a shape line, or to a
			// shape line plus an excerpt where nothing was said about it.
			builder.WriteString(entry.ToolOutput)
		}
		builder.WriteString("\n\n")

	case agent.EntryTypeSystem:
		_, _ = fmt.Fprintf(builder, "## System: %s\n\n", entry.Content)
	}
}

// chunkText splits text into chunks of approximately targetTokens size.
// It tries to break at natural boundaries (## headers). Both inputs arrive as
// parameters and no compactor state is consulted, so this is a package
// function rather than a method: nothing about a chunking decision depends on
// which Compactor asked for it.
func chunkText(text string, targetTokens int) []string {
	// Sizes come from tokens.Budget, not from multiplying the ratio here.
	// Multiplying was a second implementation of the heuristic living outside
	// the package that owns it, and one no real tokenizer could satisfy: a
	// tokenizer is not invertible by multiplication, so a substitution inside
	// internal/tokens would have left this call site computing against the old
	// ratio while every other surface moved.
	targetBytes := tokens.Budget(targetTokens)
	overlapBytes := tokens.Budget(ChunkOverlapTokens)

	if len(text) <= targetBytes {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		chunkEnd := targetBytes
		if chunkEnd > len(remaining) {
			chunkEnd = len(remaining)
		}

		// Try to find a good break point (## header)
		if chunkEnd < len(remaining) {
			// Look for ## within the last 20% of the chunk
			searchStart := chunkEnd - (chunkEnd / 5)
			searchText := remaining[searchStart:chunkEnd]

			// Find the last ## in the search region
			lastHeader := strings.LastIndex(searchText, "\n## ")
			if lastHeader >= 0 {
				chunkEnd = searchStart + lastHeader + 1 // +1 to include the newline
			}
		}

		// Never cut mid-rune: back the boundary off to the nearest UTF-8 rune
		// start (the textutil.TruncateBytes technique). A mid-rune split makes
		// the chunk invalid UTF-8, which fails proto3 string marshaling and
		// silently turns the chunk into a failure marker — content loss.
		if end := len(textutil.TruncateBytes(remaining, chunkEnd)); end > 0 {
			chunkEnd = end
		}

		chunk := remaining[:chunkEnd]
		chunks = append(chunks, strings.TrimSpace(chunk))

		// The final chunk reaches the end of the text: stop here. Advancing by
		// chunkEnd-overlap would re-enter the loop with the pure-overlap tail
		// and emit it again as a duplicate chunk.
		if chunkEnd == len(remaining) {
			break
		}

		// Move forward, keeping some overlap for context. The advance point
		// must also land on a rune boundary, or the next chunk would start
		// with the trailing bytes of a split rune.
		advance := chunkEnd - overlapBytes
		if advance > 0 {
			if a := len(textutil.TruncateBytes(remaining, advance)); a > 0 {
				advance = a
			}
		}
		if advance <= 0 {
			advance = chunkEnd
		}
		if advance >= len(remaining) {
			break
		}
		remaining = remaining[advance:]
	}

	return chunks
}

// distillChunk distills one transcript chunk (the map step), tagging the prompt
// with the chunk's position so the LLM knows it sees a slice of a larger log.
func (c *Compactor) distillChunk(ctx context.Context, chunk string, chunkNum, totalChunks int) (string, error) {
	var promptBuilder strings.Builder
	promptBuilder.WriteString(sessionDistillPrompt)
	if chunkNum > 0 && totalChunks > 1 {
		_, _ = fmt.Fprintf(&promptBuilder, "\n\nThis is chunk %d of %d from the session log.\n", chunkNum, totalChunks)
	}
	return c.runDistill(ctx, promptBuilder.String(), chunk)
}

// runDistill executes one LLM distillation call: a fresh plugin subprocess given
// systemPrompt as its instruction fragment and content wrapped in <session_log>
// as its prompt, run one-shot in minimal mode. Both the map (distillChunk) and
// reduce (finalCompressionPass) passes go through here so the request shape stays
// in one place.
func (c *Compactor) runDistill(ctx context.Context, systemPrompt, content string) (string, error) {
	// Create plugin client using the factory
	client, err := c.clientFactory(c.config.LLM, "", 0)
	if err != nil {
		return "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	// SkipSetup=true keeps distillation minimal (no hooks/commands/context), but
	// the server delivers req.Fragments to the backend only via Setup — which
	// SkipSetup bypasses. So the instructions must travel in the prompt itself,
	// ahead of the transcript; sent as a Fragment they'd be silently dropped and
	// the model would just answer the <session_log> conversationally.
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: fmt.Sprintf("%s\n\n<session_log>\n%s\n</session_log>", systemPrompt, content),
		},
		Options: &pb.RunOptions{
			PermissionMode: agent.PermissionBypass.String(),
			Mode:           pb.ExecutionMode_ONESHOT,
			Model:          c.config.Model, // e.g., "haiku", "sonnet"
			// The resolved label's configured env. This was MISSING entirely
			// while every other RunStart caller forwarded it, so a distiller
			// whose credentials live in llm.configs.<label>.env ran
			// unconfigured — silently, because an unconfigured backend does not
			// error. SkipSetup below makes this the only channel that can carry
			// it: Setup, which would otherwise deliver configuration, is
			// bypassed.
			Env:       c.config.Env,
			SkipSetup: true, // Minimal mode for distillation
		},
	}

	// Execute
	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, req, nil, &stdout, &stderr, nil)
	if err != nil {
		return "", err
	}

	if exitCode != 0 {
		return "", fmt.Errorf("LLM exited with code %d: %s", exitCode, stderr.String())
	}

	// Exit 0 with nothing on stdout is a FAILED distillation, not an empty one.
	// Counted as success it lands in the chunk slice as "", the all-chunks-failed
	// abort never fires because nothing was marked failed, and the empty result
	// is written straight over a previously good essence.md.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("LLM exited 0 but produced no output: %s", strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// distilledMeta is the YAML front-matter stored at the top of every
// distilled session .md file. Programmatic readers (e.g. the rectifier's
// staleness check, the resume picker) consume these fields without
// parsing the body.
type distilledMeta struct {
	SessionID   string    `yaml:"session_id"`
	HarpName    string    `yaml:"harp_name,omitempty"`
	DistilledAt time.Time `yaml:"distilled_at"`
	// SourceSize is the backend transcript's byte size at distill time — the
	// staleness fingerprint. loadOrDistillSession stats the live transcript and
	// re-distills when it has moved past this; the resume picker badges the row
	// "out of date". Append-only transcripts only grow, so a size change is a
	// reliable "this essence covers an earlier slice" signal. Zero when the
	// transcript path couldn't be resolved or statted (graceful: no staleness).
	SourceSize int64 `yaml:"source_size,omitempty"`
	EntryCount int   `yaml:"entry_count"`
	TokensIn   int   `yaml:"tokens_in,omitempty"`
	TokensOut  int   `yaml:"tokens_out,omitempty"`
	PlanBlocks int   `yaml:"plan_blocks"`
	// Summary is the one-line essence emitted by the LLM in its own YAML
	// frontmatter; see parseLLMFrontmatter. Empty when distillation produced
	// no valid frontmatter (graceful degrade: picker shows "no summary").
	Summary string `yaml:"summary,omitempty"`
}

// parseLLMFrontmatter peels a leading YAML block off the LLM-produced
// distillation, returning the summary value and the body sans frontmatter.
// On any failure (no leading ---, no closing ---, malformed YAML), returns
// ("", original, false) so callers can fall back without corrupting output.
// Summary is trimmed and capped at 80 chars per the prompt spec.
func parseLLMFrontmatter(out string) (summary, body string, ok bool) {
	trimmed := strings.TrimLeft(out, " \t\r\n")
	if !strings.HasPrefix(trimmed, "---\n") {
		return "", out, false
	}
	rest := trimmed[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", out, false
	}
	block := rest[:end]
	var parsed struct {
		Summary string `yaml:"summary"`
	}
	if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
		return "", out, false
	}
	bodyText := strings.TrimLeft(rest[end+len("\n---"):], "\r\n")
	return firstLineSummary(parsed.Summary), bodyText, true
}

// deriveSummary returns the picker one-liner for a distillation: the LLM's
// frontmatter summary when present, otherwise the first non-empty, non-heading
// line of the body. Both are reduced to a single line capped at 80 bytes so a
// session with any content never renders as "(no summary)".
func deriveSummary(frontmatterSummary, body string) string {
	if s := firstLineSummary(frontmatterSummary); s != "" {
		return s
	}
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return firstLineSummary(t)
	}
	return ""
}

// firstLineSummary trims s to its first non-empty line and caps it at 80 bytes,
// matching the picker-summary spec.
func firstLineSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 80 {
		s = textutil.TruncateBytes(s, 80)
	}
	return s
}

// saveDistilled writes the distilled session as markdown with YAML
// front-matter. Path resolution:
//
//   - If meta.HarpName is set, write to ~/.ctxloom/sessions/<harp>/essence.md
//     and ALSO write a sessionID-keyed pointer (a thin index reference, not
//     the body) under the legacy outputDir so existing callers that look up
//     by sessionID continue to work.
//   - Otherwise, fall back to the legacy <outputDir>/<sessionID>.md layout.
//
// This is the Phase 3.6 harp-dir layout from the ctxloom-tasks plan.
// Also writes a frozen task snapshot copy of <projectDir>/.ctxloom/tasks.md
// to <harpDir>/tasks.md when a harp dir is in play and a tasks file exists.
func (c *Compactor) saveDistilled(sessionID, body string, meta distilledMeta) (string, error) {
	// The floor: never write an empty distillation. The write is atomic and
	// replaces the previous essence.md, so an empty body is not a degraded
	// result — it is silent destruction of the only distilled record of a
	// session. Refusing here backstops every route into this function, not just
	// the empty-LLM-output one that was found.
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("refusing to write an empty distillation for session %s: it would replace any existing essence with nothing", sessionID)
	}

	meta.SessionID = sessionID
	meta.DistilledAt = time.Now().UTC()

	frontmatter, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal frontmatter: %w", err)
	}

	var doc strings.Builder
	doc.WriteString("---\n")
	doc.Write(frontmatter)
	doc.WriteString("---\n\n")
	doc.WriteString("# Session summary\n\n")
	doc.WriteString(strings.TrimSpace(body))
	doc.WriteString("\n")
	docBytes := []byte(doc.String())

	// Two writes, both under the harp: essence.md is the harp's CURRENT
	// distillation, and segments/<sessionID>.md is THIS rotation's, keyed
	// beside the canonical segment it was distilled from. essence.md is
	// overwritten by every distill, so without the second write a harp's
	// earlier rotations leave no distilled record at all.
	rotationPath, err := c.rotationEssencePath(meta.HarpName, sessionID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(rotationPath), 0o755); err != nil {
		return "", err
	}

	return c.saveEssence(meta.HarpName, rotationPath, docBytes)
}

// saveEssence writes the harp's current essence.md and this rotation's
// segments/<sessionID>.md, returning the current essence's path.
//
// The harp-dir write is no longer allowed to degrade. It used to warn and fall
// back to a project-rooted copy, which meant the file the picker and the
// SessionStart hook actually read could silently not exist while the command
// still reported success. There is nowhere else to file a harp's essence, so a
// failure here is the whole operation failing.
func (c *Compactor) saveEssence(harpName, rotationPath string, docBytes []byte) (string, error) {
	harpDir, err := harpSessionDir(harpName)
	if err != nil {
		return "", fmt.Errorf("resolve harp dir for %s: %w", harpName, err)
	}
	if err := os.MkdirAll(harpDir, 0o755); err != nil {
		return "", fmt.Errorf("create harp dir %s: %w", harpDir, err)
	}
	essencePath, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return "", fmt.Errorf("resolve essence path for %s: %w", harpName, err)
	}
	if err := iox.WriteFileAtomic(essencePath, docBytes, 0o644); err != nil {
		return "", fmt.Errorf("write essence %s: %w", essencePath, err)
	}
	// NB: the active task store already lives at <harpDir>/tasks.md (see
	// tasks.OpenSession migration), so we deliberately do NOT copy the legacy
	// project .ctxloom/tasks.md here — that read is a no-op once the project
	// file has migrated away, and would clobber the live store if a stray
	// project file lingered.
	//
	// This rotation's own copy, so a later /clear does not erase the record of
	// what THIS session was about when essence.md is overwritten.
	if err := iox.WriteFileAtomic(rotationPath, docBytes, 0o644); err != nil {
		c.warnf("write rotation essence %s: %v", rotationPath, err)
	}
	return essencePath, nil
}

// harpSessionDir returns ~/.ctxloom/sessions/<harp>/. Errors when home
// can't be resolved; the caller falls back to legacy layout in that case.
// Delegates to paths.HarpDir so the task store and the compactor resolve
// the same root.
func harpSessionDir(harpName string) (string, error) {
	return paths.HarpDir(harpName)
}

// DistilledSession is the loaded form of a distilled session .md file:
// front-matter fields plus the full markdown body (everything after
// the closing "---").
type DistilledSession struct {
	SessionID   string
	DistilledAt time.Time
	SourceSize  int64
	TokensOut   int
	Body        string
}

// LoadDistilledSession reads <sessionsDir>/<sessionID>.md.
func LoadDistilledSession(sessionsDir, sessionID string) (*DistilledSession, error) {
	path := filepath.Join(sessionsDir, sessionID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDistilledMarkdown(data)
}

// parseDistilledMarkdown extracts front-matter + body from a distilled .md.
func parseDistilledMarkdown(data []byte) (*DistilledSession, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("distilled file missing front-matter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("distilled file has unterminated front-matter")
	}
	var meta distilledMeta
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &meta); err != nil {
		return nil, fmt.Errorf("parse front-matter: %w", err)
	}
	body := strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
	return &DistilledSession{
		SessionID:   meta.SessionID,
		DistilledAt: meta.DistilledAt,
		SourceSize:  meta.SourceSize,
		TokensOut:   meta.TokensOut,
		Body:        body,
	}, nil
}

// ListDistilledSessions returns the IDs of every distilled .md file
// directly under sessionsDir.
func ListDistilledSessions(sessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".md") {
			sessions = append(sessions, strings.TrimSuffix(name, ".md"))
		}
	}
	return sessions, nil
}

// sessionDistillPrompt is the system prompt for session distillation.
// Phase 3.5.2: requires a leading YAML frontmatter block carrying a
// one-line summary so the resume picker can render row summaries without
// a second LLM call. Body sections are ordered with Open Items first
// to optimize the resume use case ("what do I need to pick up?").
var sessionDistillPrompt = resources.MustGetPromptText("session-distill")

// sessionDistillReducePrompt drives the final compression pass. Unlike the map
// prompt it tells the model its input is already-distilled partial summaries to
// merge and dedupe (not a raw transcript), re-asserts the mandatory YAML
// frontmatter the picker needs, and forbids dropping file paths / session IDs
// during the merge.
var sessionDistillReducePrompt = resources.MustGetPromptText("session-distill-reduce")

// resultFindingPrompt recovers the finding an agent never stated for a large
// tool result. See repairResults.
var resultFindingPrompt = resources.MustGetPromptText("result-finding")

// noConclusionAvailable is the exact escape hatch result-finding.md instructs
// the model to emit when a result supports no conclusion. It is a constant so
// the prompt and the code that reads its answer cannot drift apart.
const noConclusionAvailable = "no conclusion available"
