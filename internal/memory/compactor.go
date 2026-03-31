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
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

const (
	// DefaultChunkTokens is the target tokens per chunk for distillation.
	DefaultChunkTokens = 8000
	// ChunkOverlapTokens is the overlap between chunks for context continuity.
	ChunkOverlapTokens = 500
	// CharsPerToken is a rough estimate for token counting.
	CharsPerToken = 4
	// DistilledDir is the subdirectory for compacted summaries.
	DistilledDir = "distilled"
)

// CompactionConfig holds settings for session compaction.
type CompactionConfig struct {
	Plugin          string           // LLM plugin to use for distillation (default: claude-code)
	Model           string           // Model to use within the plugin (e.g., "haiku", "sonnet")
	Backend         string           // Backend name to read session from (e.g., "claude-code")
	ChunkSize       int              // Target tokens per chunk
	SessionID       string           // Session to compact (empty = most recent)
	WorkDir         string           // Working directory for the session
	OutputDir       string           // Directory to save distilled output (defaults to .ctxloom/memory)
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

	history := c.backend.History()
	if history == nil {
		return nil, fmt.Errorf("backend %q does not support session history", c.config.Backend)
	}

	// Get session to compact
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
	result.SessionID = session.ID

	if len(session.Entries) == 0 {
		return nil, fmt.Errorf("session %s has no entries", session.ID)
	}

	// Convert entries to text for chunking
	logText := c.sessionToText(session)
	result.TotalTokensIn = estimateTokens(logText)

	// Chunk the log
	chunks := c.chunkText(logText, c.config.ChunkSize)
	result.ChunksCreated = len(chunks)

	// Distill each chunk
	var distilledChunks []string
	for i, chunk := range chunks {
		fmt.Fprintf(os.Stderr, "ctxloom: compacting chunk %d/%d...\n", i+1, len(chunks))

		distilled, err := c.distillChunk(ctx, chunk, i+1, len(chunks))
		if err != nil {
			// Log error but continue with other chunks
			fmt.Fprintf(os.Stderr, "ctxloom: warning: chunk %d failed: %v\n", i+1, err)
			distilledChunks = append(distilledChunks, fmt.Sprintf("<!-- Chunk %d failed: %v -->", i+1, err))
			continue
		}
		distilledChunks = append(distilledChunks, distilled)
	}

	// Combine distilled chunks
	combined := strings.Join(distilledChunks, "\n\n---\n\n")
	result.TotalTokensOut = estimateTokens(combined)

	// If combined is still large, do a final compression pass
	if result.TotalTokensOut > c.config.ChunkSize && len(chunks) > 1 {
		fmt.Fprintf(os.Stderr, "ctxloom: final compression pass...\n")
		final, err := c.distillChunk(ctx, combined, 0, 0) // 0,0 = final pass
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: final pass failed, using combined: %v\n", err)
		} else {
			combined = final
			result.TotalTokensOut = estimateTokens(combined)
		}
	}

	// Save distilled output
	distilledPath, err := c.saveDistilled(session.ID, combined)
	if err != nil {
		return nil, fmt.Errorf("save distilled: %w", err)
	}
	result.DistilledPath = distilledPath

	result.Duration = time.Since(start)
	return result, nil
}

// sessionToText converts a session to readable text for distillation.
func (c *Compactor) sessionToText(session *backends.Session) string {
	var builder strings.Builder

	for _, entry := range session.Entries {
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
			_, _ = fmt.Fprintf(&builder, "## Tool Call: %s\n", entry.ToolName)
			if len(entry.ToolInput) > 0 {
				// Truncate large arguments
				args := string(entry.ToolInput)
				if len(args) > 500 {
					args = args[:500] + "..."
				}
				_, _ = fmt.Fprintf(&builder, "Arguments: %s\n", args)
			}
			builder.WriteString("\n")

		case backends.EntryTypeToolResult:
			_, _ = fmt.Fprintf(&builder, "## Tool Result: %s\n", entry.ToolName)
			// Truncate large output
			output := entry.ToolOutput
			if len(output) > 500 {
				output = output[:500] + "..."
			}
			builder.WriteString(output)
			if entry.IsError {
				builder.WriteString(" [ERROR]")
			}
			builder.WriteString("\n\n")

		case backends.EntryTypeSystem:
			_, _ = fmt.Fprintf(&builder, "## System: %s\n\n", entry.Content)
		}
	}

	return builder.String()
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

// saveDistilled saves the distilled content to a file.
func (c *Compactor) saveDistilled(sessionID, content string) (string, error) {
	outputDir := c.config.OutputDir
	if outputDir == "" {
		outputDir = ".ctxloom/memory"
	}
	distilledDir := filepath.Join(outputDir, DistilledDir)
	if err := os.MkdirAll(distilledDir, 0755); err != nil {
		return "", err
	}

	distilled := DistilledSession{
		SessionID:   sessionID,
		CreatedAt:   time.Now().UTC(),
		Content:     content,
		TokenCount:  estimateTokens(content),
	}

	data, err := yaml.Marshal(distilled)
	if err != nil {
		return "", err
	}

	path := filepath.Join(distilledDir, fmt.Sprintf("session-%s.yaml", sessionID))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}

	return path, nil
}

// DistilledSession holds a compacted session summary.
type DistilledSession struct {
	SessionID  string    `yaml:"session_id"`
	CreatedAt  time.Time `yaml:"created_at"`
	Content    string    `yaml:"content"`
	TokenCount int       `yaml:"token_count"`
}

// LoadDistilledSession loads a distilled session summary.
func LoadDistilledSession(memoryDir, sessionID string) (*DistilledSession, error) {
	path := filepath.Join(memoryDir, DistilledDir, fmt.Sprintf("session-%s.yaml", sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var distilled DistilledSession
	if err := yaml.Unmarshal(data, &distilled); err != nil {
		return nil, err
	}

	return &distilled, nil
}

// ListDistilledSessions returns all distilled session IDs.
func ListDistilledSessions(memoryDir string) ([]string, error) {
	distilledDir := filepath.Join(memoryDir, DistilledDir)
	entries, err := os.ReadDir(distilledDir)
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
		// Match session-*.yaml
		if len(name) > 13 && name[:8] == "session-" && name[len(name)-5:] == ".yaml" {
			sessionID := name[8 : len(name)-5]
			sessions = append(sessions, sessionID)
		}
	}

	return sessions, nil
}

// estimateTokens provides a rough token count estimate.
func estimateTokens(text string) int {
	return len(text) / CharsPerToken
}

// sessionDistillPrompt is the system prompt for session distillation.
const sessionDistillPrompt = `You are a session summarizer. Given a conversation log between a user and an AI assistant, extract the essential information for future reference.

## What to Extract

1. **Decisions Made** - What was decided and why
2. **Work Completed** - What was actually accomplished (not just attempted)
3. **Current State** - Where things stand at the end of this session
4. **Open Items** - What's still pending or needs follow-up
5. **Key Context** - Important information for continuing this work

## Rules

- Be extremely concise - target 30-50% of original size
- Use bullet points and short sentences
- Preserve exact file paths, function names, and code references
- Keep error messages and their solutions
- Skip failed attempts unless the lesson learned is important
- Skip verbose tool outputs - just note what was done
- Skip small talk and confirmations

## Output Format

Use this structure:

### Summary
[1-2 sentence overview of what happened]

### Decisions
- [decision 1]
- [decision 2]

### Completed
- [what was done]

### State
[current state of the work]

### Open Items
- [pending item 1]
- [pending item 2]

### Key Context
- [important context for next session]
`

// EssencesDir is the subdirectory for session essences.
const EssencesDir = "essences"

// SessionEssence holds a brief summary of a session (few sentences).
type SessionEssence struct {
	SessionID   string    `yaml:"session_id"`
	CreatedAt   time.Time `yaml:"created_at"`
	Essence     string    `yaml:"essence"`
	GeneratedAt time.Time `yaml:"generated_at"`
}

// LoadSessionEssence loads a cached session essence.
func LoadSessionEssence(memoryDir, sessionID string) (*SessionEssence, error) {
	path := filepath.Join(memoryDir, EssencesDir, fmt.Sprintf("session-%s.yaml", sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var essence SessionEssence
	if err := yaml.Unmarshal(data, &essence); err != nil {
		return nil, err
	}

	return &essence, nil
}

// SaveSessionEssence saves a session essence to cache.
func SaveSessionEssence(memoryDir string, essence *SessionEssence) error {
	essencesDir := filepath.Join(memoryDir, EssencesDir)
	if err := os.MkdirAll(essencesDir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(essence)
	if err != nil {
		return err
	}

	path := filepath.Join(essencesDir, fmt.Sprintf("session-%s.yaml", essence.SessionID))
	return os.WriteFile(path, data, 0644)
}

// EssenceConfig holds settings for essence generation.
type EssenceConfig struct {
	Plugin        string           // LLM plugin to use (e.g., "claude-code")
	Model         string           // Model to use (e.g., "haiku")
	MemoryDir     string           // Directory for essence cache
	ClientFactory pb.ClientFactory // Factory for creating LLM clients (default: pb.DefaultClientFactory())
}

// GenerateSessionEssence creates a brief essence of a session using an LLM.
// It checks the cache first and only generates if not already cached.
func GenerateSessionEssence(ctx context.Context, session *backends.Session, config EssenceConfig) (*SessionEssence, error) {
	// Check cache first
	if cached, err := LoadSessionEssence(config.MemoryDir, session.ID); err == nil {
		return cached, nil
	}

	// Convert session to text (first ~4000 chars for essence)
	var textBuilder strings.Builder
	for _, entry := range session.Entries {
		if entry.Type == backends.EntryTypeUser || entry.Type == backends.EntryTypeAssistant {
			_, _ = fmt.Fprintf(&textBuilder, "[%s]: %s\n\n", entry.Type, entry.Content)
		}
		if textBuilder.Len() > 4000 {
			break
		}
	}
	sessionText := textBuilder.String()

	if len(sessionText) == 0 {
		return &SessionEssence{
			SessionID:   session.ID,
			CreatedAt:   session.StartTime,
			Essence:     "(empty session)",
			GeneratedAt: time.Now(),
		}, nil
	}

	// Generate essence using LLM
	clientFactory := config.ClientFactory
	if clientFactory == nil {
		clientFactory = pb.DefaultClientFactory()
	}
	client, err := clientFactory(config.Plugin, 0)
	if err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	req := &pb.RunRequest{
		Prompt: &pb.Fragment{
			Content: fmt.Sprintf("<session_excerpt>\n%s\n</session_excerpt>", sessionText),
		},
		Fragments: []*pb.Fragment{
			{Content: essencePrompt},
		},
		Options: &pb.RunOptions{
			AutoApprove: true,
			Mode:        pb.ExecutionMode_ONESHOT,
			Model:       config.Model,
			SkipSetup:   true,
		},
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, req, &stdout, &stderr)
	if err != nil {
		return nil, err
	}

	if exitCode != 0 {
		return nil, fmt.Errorf("LLM exited with code %d: %s", exitCode, stderr.String())
	}

	essence := &SessionEssence{
		SessionID:   session.ID,
		CreatedAt:   session.StartTime,
		Essence:     strings.TrimSpace(stdout.String()),
		GeneratedAt: time.Now(),
	}

	// Cache the essence
	if err := SaveSessionEssence(config.MemoryDir, essence); err != nil {
		// Log but don't fail - essence was generated successfully
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to cache essence: %v\n", err)
	}

	return essence, nil
}

// essencePrompt is the system prompt for generating session essences.
const essencePrompt = `Write a single paragraph (2-3 sentences) capturing the essence of this coding session. What was worked on and what was the outcome? Be direct and specific. No bullet points.

Example: "Implemented user authentication with JWT tokens. Added login/logout endpoints and middleware. Tests passing but password reset still needed."`
