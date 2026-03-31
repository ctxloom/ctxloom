package backends

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Codex implements the Backend interface for OpenAI Codex CLI.
type Codex struct {
	BaseBackend
	context *CLIContextProvider
	history *CodexSessionHistory
}

// NewCodex creates a new Codex backend with default settings.
func NewCodex() *Codex {
	b := &Codex{
		BaseBackend: NewBaseBackend("codex", "1.0.0"),
		context:     &CLIContextProvider{},
	}
	b.BinaryPath = "codex"
	b.history = &CodexSessionHistory{backend: b}
	return b
}

// Lifecycle returns nil - Codex doesn't support lifecycle hooks.
func (b *Codex) Lifecycle() LifecycleHandler { return nil }

// Skills returns nil - Codex doesn't support skills.
func (b *Codex) Skills() SkillRegistry { return nil }

// Context returns the context provider (CLI arg injection).
func (b *Codex) Context() ContextProvider { return b.context }

// MCP returns nil - Codex doesn't support MCP servers.
func (b *Codex) MCP() MCPManager { return nil }

// History returns the session history accessor.
func (b *Codex) History() SessionHistory { return b.history }

// Setup prepares the backend for execution.
func (b *Codex) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if _, err := WriteContextFile(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to write context file: %w", err)
	}
	return b.context.Provide(b.WorkDir(), req.Fragments)
}

// Execute runs the backend with the given request.
func (b *Codex) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = "o3-mini"
	}
	modelInfo := &ModelInfo{ModelName: modelName, Provider: "openai"}

	if req.DryRun {
		return &ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	quiet := req.Mode == ModeOneshot
	args := b.buildArgs(req, quiet)
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	var exitCode int32
	var err error
	if req.Mode == ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, req.Env, stdout, stderr)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, req.Env, stdout, stderr)
	}

	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// Cleanup releases resources after execution.
func (b *Codex) Cleanup(ctx context.Context) error { return nil }

func (b *Codex) buildArgs(req *ExecuteRequest, quiet bool) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--full-auto")
	}
	if quiet {
		args = append(args, "--quiet")
	}

	context := b.context.GetAssembled()
	prompt := GetPromptContent(req.Prompt)
	if prompt != "" {
		var message string
		if context != "" {
			message = fmt.Sprintf("Context:\n%s\n\n---\n\nTask: %s", context, prompt)
		} else {
			message = prompt
		}
		args = append(args, message)
	}

	return args
}

// CodexSessionHistory implements SessionHistory for Codex CLI.
// Reads from ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
type CodexSessionHistory struct {
	backend *Codex
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *CodexSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	sessions, err := h.ListSessions(workDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}

	// Return most recent
	return h.GetSession(workDir, sessions[0].ID)
}

// ListSessions returns available session metadata.
func (h *CodexSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	var sessions []SessionMeta

	// Walk through YYYY/MM/DD structure
	err = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(info.Name(), "rollout-") || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}

		// Use relative path from sessions dir as ID
		relPath, _ := filepath.Rel(sessionsDir, path)
		sessions = append(sessions, SessionMeta{
			ID:        relPath,
			StartTime: info.ModTime(),
		})
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	// Sort by time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *CodexSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(sessionsDir, sessionID)
	return h.parseSessionFile(sessionPath)
}

// getSessionsDir returns the Codex sessions directory.
func (h *CodexSessionHistory) getSessionsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	// Check CODEX_HOME env var first
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}

	sessionsDir := filepath.Join(codexHome, "sessions")
	if _, err := os.Stat(sessionsDir); err != nil {
		return "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
	}

	return sessionsDir, nil
}

// parseSessionFile reads and parses a Codex session JSONL file.
func (h *CodexSessionHistory) parseSessionFile(path string) (*Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = file.Close() }()

	session := &Session{
		ID:      filepath.Base(path),
		Entries: []SessionEntry{},
	}

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := h.parseEntry(line)
		if err != nil {
			continue // Skip malformed entries
		}
		if entry != nil {
			session.Entries = append(session.Entries, *entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan session file: %w", err)
	}

	// Set start/end times from entries
	if len(session.Entries) > 0 {
		session.StartTime = session.Entries[0].Timestamp
		session.EndTime = session.Entries[len(session.Entries)-1].Timestamp
	}

	return session, nil
}

// codexEntry represents a raw entry from Codex's rollout JSONL.
type codexEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Output    string          `json:"output"`
	IsError   bool            `json:"is_error"`
}

// parseEntry converts a Codex JSONL entry to a normalized SessionEntry.
func (h *CodexSessionHistory) parseEntry(line []byte) (*SessionEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	entry := &SessionEntry{}

	// Parse timestamp
	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}

	// Map Codex entry types to normalized types
	switch raw.Type {
	case "message":
		switch raw.Role {
		case "user":
			entry.Type = EntryTypeUser
			entry.Content = raw.Content
		case "assistant":
			entry.Type = EntryTypeAssistant
			entry.Content = raw.Content
		default:
			return nil, nil
		}

	case "tool_use", "codex.tool_decision":
		entry.Type = EntryTypeToolUse
		entry.ToolName = raw.ToolName
		entry.ToolInput = raw.ToolInput

	case "tool_result", "codex.tool_result":
		entry.Type = EntryTypeToolResult
		entry.ToolName = raw.ToolName
		entry.ToolOutput = raw.Output
		entry.IsError = raw.IsError

	default:
		// Skip unknown types
		return nil, nil
	}

	return entry, nil
}

// GetSessionByPath returns a session by its full file path.
func (h *CodexSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return h.parseSessionFile(path)
}

// RegisterSession is a no-op for Codex (no registry support yet).
// TranscriptPathFromHook returns empty string - Codex doesn't support session registration yet.
func (h *CodexSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

func (h *CodexSessionHistory) RegisterSession(workDir string, pid int, transcriptPath string) error {
	return nil // Silent no-op - Codex doesn't have registry support
}

// GetPreviousSession returns nil for Codex (no registry support yet).
func (h *CodexSessionHistory) GetPreviousSession(workDir string, pid int) (*Session, error) {
	return nil, nil // No previous session tracking for Codex
}
