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

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/textutil"
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
	Plugin          string           // LLM plugin to use for distillation (default: claude-code)
	Model           string           // Model to use within the plugin (e.g., "haiku", "sonnet")
	Backend         string           // Backend name to read session from (e.g., "claude-code")
	ChunkSize       int              // Target tokens per chunk
	SessionID       string           // Session to compact (empty = most recent)
	WorkDir         string           // Working directory for the session
	OutputDir       string           // Directory to save distilled output (defaults to .ctxloom/ephemeral/memory)
	HarpName        string           // Harp name for harp-dir layout writes. Empty falls back to CTXLOOM_SESSION_HARP env var so the in-LLM compact_session path still works without explicit plumbing.
	ClientFactory   pb.ClientFactory // Factory for creating LLM clients (default: pb.DefaultClientFactory())
	BackendOverride backends.Backend // Optional: inject backend directly for testing (bypasses registry)
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
	backend       backends.Backend
	clientFactory pb.ClientFactory
}

// NewCompactor creates a new compactor with the given config.
func NewCompactor(config CompactionConfig) (*Compactor, error) {
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultChunkTokens
	}
	if config.Backend == "" {
		config.Backend = "claude-code"
	}
	if config.Plugin == "" {
		config.Plugin = "claude-code"
	}
	if config.ClientFactory == nil {
		config.ClientFactory = pb.DefaultClientFactory()
	}

	// Use injected backend if provided (for testing), otherwise use registry
	backend := config.BackendOverride
	if backend == nil {
		backend = backends.Get(config.Backend)
		if backend == nil {
			return nil, fmt.Errorf("unknown backend: %s", config.Backend)
		}
	}

	return &Compactor{
		config:        config,
		backend:       backend,
		clientFactory: config.ClientFactory,
	}, nil
}

// Compact performs compaction on a session.
func (c *Compactor) Compact(ctx context.Context) (*CompactionResult, error) {
	start := time.Now()
	result := &CompactionResult{}

	session, err := c.loadSessionToCompact()
	if err != nil {
		return nil, err
	}
	result.SessionID = session.ID

	// Extract plan blocks up front so they bypass the LLM compression pass
	// and are re-attached verbatim to the final output.
	plans := ExtractPlans(session)

	// Convert entries to text for chunking, with placeholders replacing
	// plan-bearing entries so the model doesn't try to summarize them.
	logText := c.sessionToText(session, indexBlocksByEntry(plans))
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
		fmt.Fprintln(os.Stderr, "ctxloom: warning: distillation lacks YAML frontmatter; summary will be empty")
		cleanedBody = strings.TrimSpace(combined)
	}

	body := assembleBody(cleanedBody, plans)
	harpName := c.resolveHarpName()

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
	writePlansAux(harpName, plans)

	result.Duration = time.Since(start)
	return result, nil
}

// loadSessionToCompact resolves the session to compact — the configured
// SessionID if set, else the current session — and rejects the
// nothing-to-do cases (no backend history support, no session, empty session).
func (c *Compactor) loadSessionToCompact() (*backends.Session, error) {
	history := c.backend.History()
	if history == nil {
		return nil, fmt.Errorf("backend %q does not support session history", c.config.Backend)
	}

	var session *backends.Session
	var err error
	if c.config.SessionID != "" {
		session, err = history.GetSession(c.config.WorkDir, c.config.SessionID)
		if err != nil {
			return nil, fmt.Errorf("get session %s: %w", c.config.SessionID, err)
		}
	} else {
		session, err = history.GetCurrentSession(c.config.WorkDir)
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

// writePlansAux writes plans.md under the harp dir (Phase 3.6) as a grep-able
// redundant copy of the plans already embedded in essence.md. No-op without a
// harp name or plans; best-effort (errors ignored).
func writePlansAux(harpName string, plans []PlanBlock) {
	if harpName == "" || len(plans) == 0 {
		return
	}
	dir, err := harpSessionDir(harpName)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "plans.md"), []byte(RenderPlans(plans)), 0o644)
	}
}

// sessionToText converts a session to readable text for distillation.
// Entries that hold plan content are replaced with placeholders so the
// summary LLM doesn't try to paraphrase them; the verbatim plan blocks
// are appended to the final output separately.
func (c *Compactor) sessionToText(session *backends.Session, plansByEntry map[int]PlanBlock) string {
	var builder strings.Builder

	for i, entry := range session.Entries {
		if block, ok := plansByEntry[i]; ok {
			_, _ = fmt.Fprintf(&builder, "## Tool Call: %s\n%s\n\n", entry.ToolName, PlaceholderForBlock(block))
			continue
		}
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
func appendEntryText(builder *strings.Builder, entry backends.SessionEntry) {
	switch entry.Type {
	case backends.EntryTypeUser:
		builder.WriteString("## User\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case backends.EntryTypeAssistant:
		builder.WriteString("## Assistant\n")
		builder.WriteString(entry.Content)
		builder.WriteString("\n\n")

	case backends.EntryTypeToolUse:
		_, _ = fmt.Fprintf(builder, "## Tool Call: %s\n", entry.ToolName)
		if len(entry.ToolInput) > 0 {
			_, _ = fmt.Fprintf(builder, "Arguments: %s\n", truncateForSummary(string(entry.ToolInput)))
		}
		builder.WriteString("\n")

	case backends.EntryTypeToolResult:
		_, _ = fmt.Fprintf(builder, "## Tool Result: %s\n", entry.ToolName)
		builder.WriteString(truncateForSummary(entry.ToolOutput))
		if entry.IsError {
			builder.WriteString(" [ERROR]")
		}
		builder.WriteString("\n\n")

	case backends.EntryTypeSystem:
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
	client, err := c.clientFactory(c.config.Plugin, 0)
	if err != nil {
		return "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	// Build request with model specified in options
	// SkipSetup=true for minimal startup (no hooks/skills/context)
	req := &pb.RunRequest{
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
	exitCode, err := client.Run(ctx, req, &stdout, &stderr)
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
	summary = strings.TrimSpace(parsed.Summary)
	// Take only the first line in case the LLM emitted a multi-line value
	// despite the prompt.
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		summary = strings.TrimSpace(summary[:i])
	}
	if len(summary) > 80 {
		summary = textutil.TruncateBytes(summary, 80)
	}
	return summary, bodyText, true
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
const sessionDistillPrompt = `You are a session summarizer. Given a conversation log between a user and an AI assistant, extract the essential information for future reference.

## Output Format

Begin your output with a YAML frontmatter block in this exact form:

    ---
    summary: <one line, ≤80 characters, no quotes, no trailing period>
    ---

The summary line must capture the session's purpose in a single line —
what was being worked on and (if applicable) the key outcome. Style: like
a git commit subject. Examples:
  - Designed bundle review on startup; landed PR f1262a4
  - Hardened bundle tools — path traversal, distill state
  - Spike: ctxloom-tasks replacement design

After the closing ` + "`---`" + ` and a blank line, emit the full structured body:

### Open Items
- [pending item 1]
- [pending item 2]

### State
[current state of the work]

### Decisions
- [decision 1]
- [decision 2]

### Completed
- [what was done]

### Key Context
- [important context for next session]

## What to Extract

1. **Open Items** - What's still pending or needs follow-up (most important for resume)
2. **Current State** - Where things stand at the end of this session
3. **Decisions Made** - What was decided and why
4. **Work Completed** - What was actually accomplished (not just attempted)
5. **Key Context** - Important information for continuing this work

## Plan Blocks

The log contains markers like ` + "`[plan-block #N — Label, preserved below]`" + ` where plans, task lists, and roadmap-style documents have been excised. These blocks are preserved verbatim elsewhere in the output. **Do not paraphrase or summarize the missing content.** Reference them by number when relevant (e.g. "see plan-block #2 for the migration roadmap"), but write nothing about what they contain.

## Rules

- Be extremely concise - target 30-50% of original size
- Use bullet points and short sentences
- Preserve exact file paths, function names, and code references
- Keep error messages and their solutions
- Skip failed attempts unless the lesson learned is important
- Skip verbose tool outputs - just note what was done
- Skip small talk and confirmations
`
