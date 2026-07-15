package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CodexCommands registers custom prompts for Codex CLI.
type CodexCommands struct{}

// RegisterFromContent writes custom prompts from host-resolved command exports.
// The host maps bundle content (with codex enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *CodexCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// CodexSessionHistory implements SessionHistory for Codex CLI. Reads from
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl (default ~/.codex). The
// embedded agent.SessionStore carries the afero fs + homeDir injection points
// used by tests (HomeDir empty => fall back to os.UserHomeDir + $CODEX_HOME)
// and the shared transcript parse loop.
type CodexSessionHistory struct {
	backend *Codex
	agent.SessionStore
}

// CodexSessionHistoryOption configures CodexSessionHistory.
type CodexSessionHistoryOption func(*CodexSessionHistory)

// WithCodexSessionFS sets a custom filesystem for testing.
func WithCodexSessionFS(fs afero.Fs) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) { h.FS = fs }
}

// WithCodexSessionHomeDir sets a custom home directory for testing. Overrides
// both os.UserHomeDir and the CODEX_HOME env var.
func WithCodexSessionHomeDir(dir string) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) { h.HomeDir = dir }
}

// NewCodexSessionHistory creates a new Codex session history handler.
func NewCodexSessionHistory(backend *Codex, opts ...CodexSessionHistoryOption) *CodexSessionHistory {
	h := &CodexSessionHistory{
		backend:      backend,
		SessionStore: agent.NewSessionStore(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *CodexSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return agent.GetCurrentSessionViaGetSession(workDir, h.ListSessions, h.GetSession)
}

// ListSessions returns available session metadata.
func (h *CodexSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	var sessions []agent.SessionMeta
	err = afero.Walk(agent.GetFS(h.FS), sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(info.Name(), "rollout-") || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
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

	agent.SortSessionsMostRecentFirst(sessions)
	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *CodexSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}
	return h.parseSessionFile(filepath.Join(sessionsDir, sessionID))
}

// GetSessionByPath returns a session by its full file path.
func (h *CodexSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return h.parseSessionFile(path)
}

// getSessionsDir returns the Codex sessions directory. Resolution order:
// explicit homeDir override (test-only) -> $CODEX_HOME -> os.UserHomeDir()/.codex.
// The chosen dir is suffixed with /sessions and stat-checked through the injected
// fs so tests with an empty MemMapFs see "not found" without touching the OS.
func (h *CodexSessionHistory) getSessionsDir() (string, error) {
	var home string
	if h.HomeDir != "" {
		home = filepath.Join(h.HomeDir, ".codex")
	} else {
		var err error
		if home, err = codexHome(); err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
	}

	sessionsDir := filepath.Join(home, "sessions")
	if _, err := agent.GetFS(h.FS).Stat(sessionsDir); err != nil {
		return "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
	}
	return sessionsDir, nil
}

// parseSessionFile reads and parses a Codex session JSONL file via the shared
// SessionStore loop; malformed lines are skipped.
func (h *CodexSessionHistory) parseSessionFile(path string) (*agent.Session, error) {
	return h.ParseSessionFile(path, filepath.Base(path), func(line []byte) []agent.SessionEntry {
		entry, err := h.parseEntry(line)
		if err != nil || entry == nil {
			return nil // Skip malformed/unknown entries
		}
		return []agent.SessionEntry{*entry}
	})
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
	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}

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
		return nil, nil // Skip unknown types
	}
	return entry, nil
}

// TranscriptPathFromHook returns the transcript path codex provides directly on
// every hook's stdin (the shared `transcript_path` field), enabling /clear
// recovery and session-bind. Empty when codex reports none (transcript_path may
// be null); the path points at the session's rollout-*.jsonl, which
// GetSessionByPath parses.
func (h *CodexSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}
