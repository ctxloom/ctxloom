package memory

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/iox"
	"github.com/ctxloom/shared/textutil"
)

const (
	// DefaultChunkTokens is the target tokens per chunk for distillation.
	DefaultChunkTokens = 8000
	// ChunkOverlapTokens is the overlap between chunks for context continuity.
	ChunkOverlapTokens = 500
	// CharsPerToken is a rough estimate for token counting.
	CharsPerToken = 4
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
		source = reader
		plans = reader.GetPlans
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

	// Plans are the session's own .plan.md documents, read from its ctxloom
	// session directory and served by the agent server — not mined from the
	// transcript. They bypass the LLM compression pass and are re-attached
	// verbatim. Best-effort: a retrieval failure warns and omits them.
	planFiles, err := c.plans(ctx, harpName)
	if err != nil {
		clidiag.Warn("ctxloom", "plan retrieval failed, omitting plan blocks: %v", err)
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
		clidiag.Warn("ctxloom", "distillation failed for all %d chunks; keeping previous essence", len(chunks))
		return nil, fmt.Errorf("distillation failed for all %d chunks", len(chunks))
	}
	if failedChunks > 0 {
		clidiag.Warn("ctxloom", "distillation failed for %d of %d chunks; summary is incomplete", failedChunks, len(chunks))
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
		clidiag.Warn("ctxloom", "distillation lacks YAML frontmatter; deriving summary from body")
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
	})
	if err != nil {
		return nil, fmt.Errorf("save distilled: %w", err)
	}
	result.DistilledPath = distilledPath

	updateSessionIndex(harpName, session.ID, summary, detail)

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

	var session *agent.Session
	var err error
	if c.config.SessionID != "" {
		session, err = c.source.GetSession(ctx, c.config.SessionID)
		if err != nil {
			return nil, fmt.Errorf("get session %s: %w", c.config.SessionID, err)
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
	if len(session.Entries) == 0 {
		return nil, fmt.Errorf("session %s has no entries", session.ID)
	}
	return session, nil
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
			// Fprintf formats into one buffer and emits a single Write, so
			// concurrent chunk progress lines don't interleave mid-line.
			fmt.Fprintf(os.Stderr, "ctxloom: compacting chunk %d/%d...\n", i+1, total)
			out, err := c.distillChunk(ctx, chunk, i+1, total)
			if err != nil {
				clidiag.Warn("ctxloom", "chunk %d failed: %v", i+1, err)
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
	fmt.Fprintf(os.Stderr, "ctxloom: final compression pass...\n")
	final, err := c.runDistill(ctx, sessionDistillReducePrompt, combined)
	if err != nil {
		clidiag.Warn("ctxloom", "final pass failed, using combined: %v", err)
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

// updateSessionIndex best-effort records the session ID against the harp (so a
// later `ctxloom session distill <harp>` finds the transcript) and updates the
// picker summary and detail lines. No-op without a harp name; all failures warn,
// never fatal.
func updateSessionIndex(harpName, sessionID, summary string, detail []string) {
	if harpName == "" {
		return
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return
	}
	if entry, _ := mgr.Find(harpName); entry != nil && entry.SessionID == "" {
		if err := mgr.BindSession(harpName, sessionID, ""); err != nil {
			clidiag.Warn("ctxloom", "index bind failed: %v", err)
		}
	}
	if summary != "" {
		if err := mgr.SetSummary(harpName, summary, detail); err != nil {
			clidiag.Warn("ctxloom", "index summary update failed: %v", err)
		}
	}
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

	// SkipSetup=true keeps distillation minimal (no hooks/skills/context), but
	// the server delivers req.Fragments to the backend only via Setup — which
	// SkipSetup bypasses. So the instructions must travel in the prompt itself,
	// ahead of the transcript; sent as a Fragment they'd be silently dropped and
	// the model would just answer the <session_log> conversationally.
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: fmt.Sprintf("%s\n\n<session_log>\n%s\n</session_log>", systemPrompt, content),
		},
		Options: &pb.RunOptions{
			AutoApprove: true,
			Mode:        pb.ExecutionMode_ONESHOT,
			Model:       c.config.Model, // e.g., "haiku", "sonnet"
			SkipSetup:   true,           // Minimal mode for distillation
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
	EntryCount  int       `yaml:"entry_count"`
	TokensIn    int       `yaml:"tokens_in,omitempty"`
	TokensOut   int       `yaml:"tokens_out,omitempty"`
	PlanBlocks  int       `yaml:"plan_blocks"`
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
		clidiag.Warn("ctxloom", "resolve harp dir for %s: %v; writing legacy layout only", harpName, err)
		return "", false
	}
	if err := os.MkdirAll(harpDir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "create harp dir %s: %v; writing legacy layout only", harpDir, err)
		return "", false
	}
	essencePath := filepath.Join(harpDir, paths.EssenceFileName)
	if err := iox.WriteFileAtomic(essencePath, docBytes, 0o644); err != nil {
		clidiag.Warn("ctxloom", "write essence %s: %v; writing legacy layout only", essencePath, err)
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
		clidiag.Warn("ctxloom", "mirror essence to %s: %v", legacyPath, err)
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
	return len(text) / CharsPerToken
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
