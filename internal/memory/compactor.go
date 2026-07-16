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
	// CharsPerToken is the chars-per-token ratio, owned by internal/tokens so the
	// distillation estimate and the dry-run preview agree on one heuristic.
	CharsPerToken = tokens.CharsPerToken
	// distillConcurrency bounds how many chunks are distilled in parallel. Each
	// chunk distillation spawns its own LLM plugin subprocess, so this caps
	// concurrent subprocesses (and provider rate pressure) while still cutting
	// wall-clock from sum-of-chunks to roughly slowest-chunk × ceil(n/limit).
	distillConcurrency = 4
)

// CompactionConfig holds settings for session compaction.
type CompactionConfig struct {
	LLM             string           // LLM plugin to use for distillation (default: claude-code)
	Model           string           // Model to use within the plugin (e.g., "haiku", "sonnet")
	Backend         string           // Backend name to read session from (e.g., "claude-code")
	ChunkSize       int              // Target tokens per chunk
	SessionID       string           // Session to compact (empty = most recent)
	WorkDir         string           // Working directory for the session
	OutputDir       string           // Directory to save distilled output (defaults to .ctxloom/ephemeral/memory)
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
}

// CompactionResult holds the result of a compaction operation.
type CompactionResult struct {
	SessionID      string
	ChunksCreated  int
	TotalTokensIn  int
	TotalTokensOut int
	DistilledPath  string
	Duration       time.Duration
	Error          string
}

// Compactor handles session log compaction.
type Compactor struct {
	config        CompactionConfig
	source        pb.SessionSource
	plans         func(context.Context, string) ([]agent.PlanFile, error)
	clientFactory pb.ClientFactory
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
		source pb.SessionSource
		plans  func(context.Context, string) ([]agent.PlanFile, error)
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
		plans = reader.GetPlans
		// Tough-cloud S4: prefer ctxloom's own captured transcript over the
		// legacy per-engine scraper reader now behind it. A session-index open
		// failure (rare — a corrupt/unwritable ~/.ctxloom/sessions/index.yaml)
		// degrades to the legacy-only reader rather than failing compaction
		// outright; distillation must never block on the canonical layer.
		if store, sErr := sessions.Open(""); sErr == nil {
			source = pb.NewCanonicalFallbackSource(reader, config.WorkDir, store)
		} else {
			source = reader
		}
	}

	return &Compactor{
		config:        config,
		source:        source,
		plans:         plans,
		clientFactory: config.ClientFactory,
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
	logText := c.sessionToText(session)
	result.TotalTokensIn = estimateTokens(logText)

	// Chunk the log, distill each chunk, then optionally re-compress.
	chunks := c.chunkText(logText, c.config.ChunkSize)
	result.ChunksCreated = len(chunks)

	distilled, failedChunks := c.distillChunks(ctx, chunks)
	// A totally failed distillation (e.g. LLM backend down) would replace a
	// previously good essence with nothing but failure markers — that's data
	// loss, not graceful degradation. Abort the save and keep the old essence.
	// Partial success still saves, per the fault-tolerance philosophy.
	if len(chunks) > 0 && failedChunks == len(chunks) {
		c.warnf("distillation failed for all %d chunks; keeping previous essence", len(chunks))
		return nil, fmt.Errorf("distillation failed for all %d chunks", len(chunks))
	}
	if failedChunks > 0 {
		c.warnf("distillation failed for %d of %d chunks; summary is incomplete", failedChunks, len(chunks))
	}

	combined := strings.Join(distilled, "\n\n---\n\n")
	result.TotalTokensOut = estimateTokens(combined)

	// Any multi-chunk session needs the reduce pass: it unifies the concatenated
	// per-chunk summaries into one canonical essence (YAML frontmatter + the
	// "### Open Items" section the picker derives its summary and detail lines
	// from). Gating it on size left small multi-chunk sessions with raw map
	// output — no frontmatter, no Open Items. Single-chunk sessions already
	// produce one canonical map output, so they skip it.
	if len(chunks) > 1 {
		combined = c.finalCompressionPass(ctx, combined)
		result.TotalTokensOut = estimateTokens(combined)
	}

	// Pull the LLM-emitted YAML frontmatter (Phase 3.5.2). If it's
	// missing/malformed, fall through with empty summary: the picker
	// shows "(no summary)" and the user can re-run distill on demand.
	summary, cleanedBody, hadFM := parseLLMFrontmatter(strings.TrimSpace(combined))
	if !hadFM {
		c.warnf("distillation lacks YAML frontmatter; deriving summary from body")
		cleanedBody = strings.TrimSpace(combined)
	}

	// Fall back to the first prose line when the LLM omitted a summary, so a
	// distilled session never renders as "(no summary)" in the picker.
	summary = deriveSummary(summary, cleanedBody)

	// Extra picker lines: the leading Open Items, so a resume row shows "what +
	// what's left" instead of a lone subject. Derived from the LLM body before
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
// SessionID if set, else the current session — and rejects the
// nothing-to-do cases (no backend history support, no session, empty session).
func (c *Compactor) loadSessionToCompact(ctx context.Context) (*agent.Session, error) {
	if c.source == nil {
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
		// that just ended" (seedy-apron). Only fall back to CurrentSession when
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
			// added (FINDING #5).
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
	session.Entries = agent.MainThreadEntries(session.Entries)
	if len(session.Entries) == 0 {
		return nil, fmt.Errorf("session %s has no entries", session.ID)
	}
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
// and returns the outputs in chunk order plus how many chunks failed. Chunks are
// independent — the overlap between them is context padding, not a data
// dependency — so they distill in parallel; results are written into their own
// slice slots so order is preserved regardless of completion order. Per CLAUDE.md
// fault tolerance, a failed chunk is warned and replaced with an HTML-comment
// marker rather than aborting; the caller decides whether a total failure aborts
// the save. A failing chunk does NOT cancel its siblings.
func (c *Compactor) distillChunks(ctx context.Context, chunks []string) ([]string, int) {
	distilled := make([]string, len(chunks))
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
				failed.Add(1)
				return
			}
			distilled[i] = out
		}(i, chunk)
	}
	wg.Wait()
	return distilled, int(failed.Load())
}

// finalCompressionPass merges the per-chunk distillations into one coherent
// essence using the dedicated reduce prompt (which knows its input is already-
// distilled partial summaries to unify, not a raw transcript to re-summarize,
// and re-asserts the mandatory YAML frontmatter + identifier preservation). On
// failure it warns and returns the input unchanged — a too-large summary beats
// no summary.
func (c *Compactor) finalCompressionPass(ctx context.Context, combined string) string {
	c.progressf("ctxloom: final compression pass...\n")
	final, err := c.runDistill(ctx, sessionDistillReducePrompt, combined)
	if err != nil {
		c.warnf("final pass failed, using combined: %v", err)
		return combined
	}
	return final
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
	if entry, _ := mgr.Find(harpName); entry != nil && entry.SessionID == "" {
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
	// Prefer the canonical transcript's size (tough-cloud S4): once a harp has
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
func (c *Compactor) sessionToText(session *agent.Session) string {
	var builder strings.Builder
	for _, entry := range session.Entries {
		appendEntryText(&builder, entry)
	}
	return builder.String()
}

// truncateForSummary caps an argument/output string at 500 bytes with an
// ellipsis, keeping summary text compact.
func truncateForSummary(s string) string {
	if len(s) > 500 {
		return textutil.TruncateBytes(s, 500) + "..."
	}
	return s
}

// appendEntryText renders one session entry as a markdown section.
func appendEntryText(builder *strings.Builder, entry agent.SessionEntry) {
	switch entry.Type {
	case agent.EntryTypeUser:
		builder.WriteString("## User\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case agent.EntryTypeAssistant:
		builder.WriteString("## Assistant\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case agent.EntryTypeToolUse:
		_, _ = fmt.Fprintf(builder, "## Tool Call: %s\n", entry.ToolName)
		if len(entry.ToolInput) > 0 {
			_, _ = fmt.Fprintf(builder, "Arguments: %s\n", truncateForSummary(string(entry.ToolInput)))
		}
		builder.WriteString("\n")

	case agent.EntryTypeToolResult:
		_, _ = fmt.Fprintf(builder, "## Tool Result: %s\n", entry.ToolName)
		builder.WriteString(truncateForSummary(entry.ToolOutput))
		if entry.IsError {
			builder.WriteString(" [ERROR]")
		}
		builder.WriteString("\n\n")

	case agent.EntryTypeSystem:
		_, _ = fmt.Fprintf(builder, "## System: %s\n\n", entry.Content)
	}
}

// chunkText splits text into chunks of approximately targetTokens size.
// It tries to break at natural boundaries (## headers).
func (c *Compactor) chunkText(text string, targetTokens int) []string {
	targetChars := targetTokens * CharsPerToken
	overlapChars := ChunkOverlapTokens * CharsPerToken

	if len(text) <= targetChars {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		chunkEnd := targetChars
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
		advance := chunkEnd - overlapChars
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
			SkipSetup:      true,           // Minimal mode for distillation
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

	return strings.TrimSpace(stdout.String()), nil
}

// distilledMeta is the YAML front-matter stored at the top of every
// distilled session .md file. Programmatic readers (e.g. the rectifier's
// staleness check, the resume picker) consume these fields without
// parsing the body.
type distilledMeta struct {
	SessionID   string    `yaml:"session_id"`
	HarpName    string    `yaml:"harp_name,omitempty"`
	DistilledAt time.Time `yaml:"distilled_at"`
	SourcePath  string    `yaml:"source_path,omitempty"`
	SourceMtime time.Time `yaml:"source_mtime,omitempty"`
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

	// Legacy outputDir for sessionID lookups (load_session, etc.).
	outputDir := c.config.OutputDir
	if outputDir == "" {
		outputDir = ".ctxloom/sessions"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	legacyPath := filepath.Join(outputDir, sessionID+".md")

	// Phase 3.6 harp-dir layout: when a harp name is known, that's the
	// primary write target; the legacy path is also written so existing
	// sessionID-keyed lookups continue working. Every fallthrough to
	// legacy-only warns: the essence.md the picker and SessionStart hook
	// read was NOT written, and silence would hide that.
	if meta.HarpName != "" {
		if essencePath, ok := c.saveEssence(meta.HarpName, legacyPath, docBytes); ok {
			return essencePath, nil
		}
	}

	// All writes are atomic (temp file + rename): a crash mid-write must not
	// truncate the essence that recovery and the resume picker depend on.
	if err := iox.WriteFileAtomic(legacyPath, docBytes, 0o644); err != nil {
		return "", err
	}
	return legacyPath, nil
}

// saveEssence writes the harp-dir essence.md plus its legacy mirror. Returns
// ok=false (after warning) when the harp-dir write failed and the caller
// should degrade to the legacy-only layout.
func (c *Compactor) saveEssence(harpName, legacyPath string, docBytes []byte) (string, bool) {
	harpDir, err := harpSessionDir(harpName)
	if err != nil {
		c.warnf("resolve harp dir for %s: %v; writing legacy layout only", harpName, err)
		return "", false
	}
	if err := os.MkdirAll(harpDir, 0o755); err != nil {
		c.warnf("create harp dir %s: %v; writing legacy layout only", harpDir, err)
		return "", false
	}
	essencePath := filepath.Join(harpDir, paths.EssenceFileName)
	if err := iox.WriteFileAtomic(essencePath, docBytes, 0o644); err != nil {
		c.warnf("write essence %s: %v; writing legacy layout only", essencePath, err)
		return "", false
	}
	// NB: the active task store already lives at <harpDir>/tasks.md (see
	// tasks.OpenSession migration), so we deliberately do NOT copy the legacy
	// project .ctxloom/tasks.md here — that read is a no-op once the project
	// file has migrated away, and would clobber the live store if a stray
	// project file lingered.
	//
	// Mirror essence to legacy path so sessionID lookups still work.
	if err := iox.WriteFileAtomic(legacyPath, docBytes, 0o644); err != nil {
		c.warnf("mirror essence to %s: %v", legacyPath, err)
	}
	return essencePath, true
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
	SourcePath  string
	SourceMtime time.Time
	SourceSize  int64
	EntryCount  int
	TokensIn    int
	TokensOut   int
	PlanBlocks  int
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
		SourcePath:  meta.SourcePath,
		SourceMtime: meta.SourceMtime,
		SourceSize:  meta.SourceSize,
		EntryCount:  meta.EntryCount,
		TokensIn:    meta.TokensIn,
		TokensOut:   meta.TokensOut,
		PlanBlocks:  meta.PlanBlocks,
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

// estimateTokens provides a rough token count estimate.
func estimateTokens(text string) int {
	return tokens.Estimate(text)
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
// frontmatter the picker needs, and forbids dropping file paths / plan-block
// references / session IDs during the merge.
var sessionDistillReducePrompt = resources.MustGetPromptText("session-distill-reduce")
