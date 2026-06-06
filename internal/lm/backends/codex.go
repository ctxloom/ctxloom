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

	"github.com/ctxloom/shared/agent"
	"github.com/spf13/afero"
)

// CodexConfig is codex's typed LLM config. The backend owns this struct; the
// config package only carries the raw body that decodes into it.
type CodexConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (CodexConfig) BackendType() string { return "codex" }

// Codex implements the Backend interface for OpenAI Codex CLI.
type Codex struct {
	agent.BaseBackend
	context *CLIContextProvider
	history *CodexSessionHistory
}

// Configure applies a decoded codex config to this backend.
func (b *Codex) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*CodexConfig)
	if !ok {
		return
	}
	if c.BinaryPath != "" {
		b.BinaryPath = c.BinaryPath
	}
	if len(c.Args) > 0 {
		b.Args = c.Args
	}
	for k, v := range c.Env {
		b.Env[k] = v
	}
}

// NewCodex creates a new Codex backend with default settings.
func NewCodex() *Codex {
	b := &Codex{
		BaseBackend: agent.NewBaseBackend("codex", "1.0.0"),
		context:     &CLIContextProvider{},
	}
	b.BinaryPath = "codex"
	b.history = NewCodexSessionHistory(b)
	return b
}

// Lifecycle returns nil - Codex doesn't support lifecycle hooks.
func (b *Codex) Lifecycle() agent.LifecycleHandler { return nil }

// Skills returns nil - Codex doesn't support skills.
func (b *Codex) Skills() agent.SkillRegistry { return nil }

// Context returns the context provider (CLI arg injection).
func (b *Codex) Context() agent.ContextProvider { return b.context }

// MCP returns nil - Codex doesn't support MCP servers.
func (b *Codex) MCP() agent.MCPManager { return nil }

// History returns the session history accessor.
func (b *Codex) History() agent.SessionHistory { return b.history }

// Setup prepares the backend for execution.
func (b *Codex) Setup(ctx context.Context, req *agent.SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if _, err := agent.WriteContextFile(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to write context file: %w", err)
	}
	return b.context.Provide(b.WorkDir(), req.Fragments)
}

// Execute runs the backend with the given request.
func (b *Codex) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = "o3-mini"
	}
	modelInfo := &agent.ModelInfo{ModelName: modelName, Provider: "openai"}

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	quiet := req.Mode == agent.ModeOneshot
	args := b.buildArgs(req, quiet)
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	var exitCode int32
	var err error
	if req.Mode == agent.ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, req.Env, req.Stdin, stdout, stderr, req.Resize)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, req.Env, stdout, stderr)
	}

	return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// Cleanup releases resources after execution.
func (b *Codex) Cleanup(ctx context.Context) error { return nil }

func (b *Codex) buildArgs(req *agent.ExecuteRequest, quiet bool) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--full-auto")
	}
	if quiet {
		args = append(args, "--quiet")
	}

	context := b.context.GetAssembled()
	prompt := agent.GetPromptContent(req.Prompt)
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
// Reads from ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. The fs
// and homeDir fields are afero injection points used by tests; in
// production they default to OS filesystem + os.UserHomeDir.
type CodexSessionHistory struct {
	backend *Codex
	fs      afero.Fs
	homeDir string // empty => fall back to os.UserHomeDir + $CODEX_HOME
}

// CodexSessionHistoryOption configures CodexSessionHistory.
type CodexSessionHistoryOption func(*CodexSessionHistory)

// WithCodexSessionFS sets a custom filesystem for testing.
func WithCodexSessionFS(fs afero.Fs) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) {
		h.fs = fs
	}
}

// WithCodexSessionHomeDir sets a custom home directory for testing.
// Overrides both os.UserHomeDir and the CODEX_HOME env var.
func WithCodexSessionHomeDir(dir string) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) {
		h.homeDir = dir
	}
}

// NewCodexSessionHistory creates a new Codex session history handler.
// Mirrors NewGeminiSessionHistory's shape so tests can swap in an
// afero.MemMapFs and a synthetic home dir for hermetic runs.
func NewCodexSessionHistory(backend *Codex, opts ...CodexSessionHistoryOption) *CodexSessionHistory {
	h := &CodexSessionHistory{
		backend: backend,
		fs:      afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *CodexSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
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
func (h *CodexSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	var sessions []agent.SessionMeta

	// Walk through YYYY/MM/DD structure
	err = afero.Walk(h.fs, sessionsDir, func(path string, info os.FileInfo, err error) error {
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
		sessions = append(sessions, agent.SessionMeta{
			ID:        relPath,
			StartTime: info.ModTime(),
			Path:      path,
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
func (h *CodexSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(sessionsDir, sessionID)
	return h.parseSessionFile(sessionPath)
}

// getSessionsDir returns the Codex sessions directory.
//
// Resolution order: explicit homeDir override (test-only) → $CODEX_HOME →
// os.UserHomeDir() + "/.codex". The chosen dir is suffixed with /sessions
// and stat-checked through the injected fs so tests with an empty
// MemMapFs see the "not found" error without touching the real OS.
func (h *CodexSessionHistory) getSessionsDir() (string, error) {
	var codexHome string
	switch {
	case h.homeDir != "":
		codexHome = filepath.Join(h.homeDir, ".codex")
	case os.Getenv("CODEX_HOME") != "":
		codexHome = os.Getenv("CODEX_HOME")
	default:
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		codexHome = filepath.Join(homeDir, ".codex")
	}

	sessionsDir := filepath.Join(codexHome, "sessions")
	if _, err := h.fs.Stat(sessionsDir); err != nil {
		return "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
	}

	return sessionsDir, nil
}

// parseSessionFile reads and parses a Codex session JSONL file.
func (h *CodexSessionHistory) parseSessionFile(path string) (*agent.Session, error) {
	file, err := h.fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = file.Close() }()

	session := &agent.Session{
		ID:      filepath.Base(path),
		Entries: []agent.SessionEntry{},
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
func (h *CodexSessionHistory) parseEntry(line []byte) (*agent.SessionEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	entry := &agent.SessionEntry{}

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
			entry.Type = agent.EntryTypeUser
			entry.Content = raw.Content
		case "assistant":
			entry.Type = agent.EntryTypeAssistant
			entry.Content = raw.Content
		default:
			return nil, nil
		}

	case "tool_use", "codex.tool_decision":
		entry.Type = agent.EntryTypeToolUse
		entry.ToolName = raw.ToolName
		entry.ToolInput = raw.ToolInput

	case "tool_result", "codex.tool_result":
		entry.Type = agent.EntryTypeToolResult
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
func (h *CodexSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return h.parseSessionFile(path)
}

// TranscriptPathFromHook returns empty string - Codex doesn't support session registration yet.
func (h *CodexSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}
