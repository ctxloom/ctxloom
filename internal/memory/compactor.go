package memory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/textutil"
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/agent"
)

const (
	// DefaultChunkTokens is the target tokens per chunk for distillation.
	DefaultChunkTokens = 8000
	// ChunkOverlapTokens is the overlap between chunks for context continuity.
	ChunkOverlapTokens = 500
	// CharsPerToken is a rough estimate for token counting.
	CharsPerToken = 4
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
	// verbatim. Best-effort: a retrieval failure just omits them.
	planFiles, _ := c.plans(ctx, harpName)
	plans := planFilesToBlocks(planFiles)

	// Convert entries to text for chunking. Plans live in separate files now, so
	// there are no in-transcript plan blocks to placeholder out.
	logText := c.sessionToText(session)
	result.TotalTokensIn = estimateTokens(logText)

	// Chunk the log, distill each chunk, then optionally re-compress.
	chunks := c.chunkText(logText, c.config.ChunkSize)
	result.ChunksCreated = len(chunks)

	combined := strings.Join(c.distillChunks(ctx, chunks), "\n\n---\n\n")
	result.TotalTokensOut = estimateTokens(combined)

	if result.TotalTokensOut > c.config.ChunkSize && len(chunks) > 1 {
		combined = c.finalCompressionPass(ctx, combined)
		result.TotalTokensOut = estimateTokens(combined)
	}

	// Pull the LLM-emitted YAML frontmatter (Phase 3.5.2). If it's
	// missing/malformed, fall through with empty summary: the picker
	// shows "(no summary)" and the user can re-run distill on demand.
	summary, cleanedBody, hadFM := parseLLMFrontmatter(strings.TrimSpace(combined))
	if !hadFM {
		fmt.Fprintln(os.Stderr, "ctxloom: warning: distillation lacks YAML frontmatter; deriving summary from body")
		cleanedBody = strings.TrimSpace(combined)
	}

	// Fall back to the first prose line when the LLM omitted a summary, so a
	// distilled session never renders as "(no summary)" in the picker.
	summary = deriveSummary(summary, cleanedBody)

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

	updateSessionIndex(harpName, session.ID, summary)

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

// distillChunks distills each chunk in order. Per CLAUDE.md fault tolerance, a
// failed chunk is warned and replaced with an HTML-comment marker rather than
// aborting the whole compaction.
func (c *Compactor) distillChunks(ctx context.Context, chunks []string) []string {
	distilled := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		fmt.Fprintf(os.Stderr, "ctxloom: compacting chunk %d/%d...\n", i+1, len(chunks))
		out, err := c.distillChunk(ctx, chunk, i+1, len(chunks))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: chunk %d failed: %v\n", i+1, err)
			distilled = append(distilled, fmt.Sprintf("<!-- Chunk %d failed: %v -->", i+1, err))
			continue
		}
		distilled = append(distilled, out)
	}
	return distilled
}

// finalCompressionPass re-distills the combined output once more (chunkNum/total
// = 0,0 signals "final pass"). On failure it warns and returns the input
// unchanged — a too-large summary beats no summary.
func (c *Compactor) finalCompressionPass(ctx context.Context, combined string) string {
	fmt.Fprintf(os.Stderr, "ctxloom: final compression pass...\n")
	final, err := c.distillChunk(ctx, combined, 0, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: final pass failed, using combined: %v\n", err)
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
// picker summary. No-op without a harp name; all failures warn, never fatal.
func updateSessionIndex(harpName, sessionID, summary string) {
	if harpName == "" {
		return
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return
	}
	if entry, _ := mgr.Find(harpName); entry != nil && entry.SessionID == "" {
		if err := mgr.BindSession(harpName, sessionID, ""); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: index bind failed: %v\n", err)
		}
	}
	if summary != "" {
		if err := mgr.SetSummary(harpName, summary); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: index summary update failed: %v\n", err)
		}
	}
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

		chunk := remaining[:chunkEnd]
		chunks = append(chunks, strings.TrimSpace(chunk))

		// Move forward, keeping some overlap for context
		advance := chunkEnd - overlapChars
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

// distillChunk sends a chunk through the LLM for distillation.
func (c *Compactor) distillChunk(ctx context.Context, chunk string, chunkNum, totalChunks int) (string, error) {
	// Build the distillation prompt
	var promptBuilder strings.Builder
	promptBuilder.WriteString(sessionDistillPrompt)

	if chunkNum > 0 && totalChunks > 1 {
		_, _ = fmt.Fprintf(&promptBuilder, "\n\nThis is chunk %d of %d from the session log.\n", chunkNum, totalChunks)
	} else if chunkNum == 0 {
		promptBuilder.WriteString("\n\nThis is a final compression pass combining previously distilled chunks.\n")
	}

	// Create plugin client using the factory
	client, err := c.clientFactory(c.config.LLM, 0)
	if err != nil {
		return "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	// Build request with model specified in options
	// SkipSetup=true for minimal startup (no hooks/skills/context)
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: fmt.Sprintf("<session_log>\n%s\n</session_log>", chunk),
		},
		Fragments: []*pb.Fragment{
			{Content: promptBuilder.String()},
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
// This is the Phase 3.6 harp-dir layout from docs/ctxloom-tasks-plan.md.
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
	// sessionID-keyed lookups continue working.
	if meta.HarpName != "" {
		harpDir, err := harpSessionDir(meta.HarpName)
		if err == nil {
			if err := os.MkdirAll(harpDir, 0o755); err == nil {
				essencePath := filepath.Join(harpDir, paths.EssenceFileName)
				if err := os.WriteFile(essencePath, docBytes, 0o644); err == nil {
					// NB: the active task store already lives at
					// <harpDir>/tasks.md (see tasks.OpenSession migration), so
					// we deliberately do NOT copy the legacy project
					// .ctxloom/tasks.md here — that read is a no-op once the
					// project file has migrated away, and would clobber the
					// live store if a stray project file lingered.
					//
					// Mirror essence to legacy path so sessionID lookups still work.
					_ = os.WriteFile(legacyPath, docBytes, 0o644)
					return essencePath, nil
				}
			}
		}
		// Fall through to legacy-only on any error above (graceful degrade).
	}

	if err := os.WriteFile(legacyPath, docBytes, 0o644); err != nil {
		return "", err
	}
	return legacyPath, nil
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
