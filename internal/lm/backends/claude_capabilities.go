package backends

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
)

// ClaudeLifecycle implements LifecycleHandler for Claude Code using hooks.
// Embeds BaseLifecycle for shared implementation.
type ClaudeLifecycle struct {
	*BaseLifecycle
	backend *ClaudeCode
}

// NewClaudeLifecycle creates a new Claude lifecycle handler.
func NewClaudeLifecycle(backend *ClaudeCode) *ClaudeLifecycle {
	return &ClaudeLifecycle{
		BaseLifecycle: NewBaseLifecycle("claude-code"),
		backend:       backend,
	}
}

// ClaudeMCPManager implements MCPManager for Claude Code.
// Embeds BaseMCPManager for shared implementation.
type ClaudeMCPManager struct {
	*BaseMCPManager
	backend *ClaudeCode
}

// NewClaudeMCPManager creates a new Claude MCP manager.
func NewClaudeMCPManager(backend *ClaudeCode) *ClaudeMCPManager {
	return &ClaudeMCPManager{
		BaseMCPManager: NewBaseMCPManager("claude-code"),
		backend:        backend,
	}
}

// ClaudeSkills implements SkillRegistry for Claude Code using slash commands.
type ClaudeSkills struct {
	backend *ClaudeCode
}

// Register adds a skill as a Claude Code slash command.
func (s *ClaudeSkills) Register(workDir string, skill Skill) error {
	enabled := true
	content := &bundles.LoadedContent{
		Name:    skill.Name,
		Content: skill.Content,
	}
	content.Plugins.LM.ClaudeCode.Enabled = &enabled
	content.Plugins.LM.ClaudeCode.Description = skill.Description
	return WriteCommandFiles(workDir, []*bundles.LoadedContent{content})
}

// RegisterAll adds multiple skills as Claude Code slash commands.
func (s *ClaudeSkills) RegisterAll(workDir string, skills []Skill) error {
	var contents []*bundles.LoadedContent
	enabled := true
	for _, skill := range skills {
		content := &bundles.LoadedContent{
			Name:    skill.Name,
			Content: skill.Content,
		}
		content.Plugins.LM.ClaudeCode.Enabled = &enabled
		content.Plugins.LM.ClaudeCode.Description = skill.Description
		contents = append(contents, content)
	}
	return WriteCommandFiles(workDir, contents)
}

// RegisterFromContent registers skills from LoadedContent objects.
func (s *ClaudeSkills) RegisterFromContent(workDir string, contents []*bundles.LoadedContent) error {
	return WriteCommandFiles(workDir, contents)
}

// Clear removes all SCM-managed skills using the manifest.
func (s *ClaudeSkills) Clear(workDir string) error {
	commandsDir := filepath.Join(workDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".scm-manifest")

	// Clean up old subdirectory style (migration)
	_ = os.RemoveAll(filepath.Join(commandsDir, "scm"))

	// Read manifest and remove tracked files
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			os.Remove(filepath.Join(commandsDir, name))
		}
	}

	return os.Remove(manifestPath)
}

// List returns registered skill names from the manifest.
func (s *ClaudeSkills) List(workDir string) ([]string, error) {
	manifestPath := filepath.Join(workDir, ".claude", "commands", ".scm-manifest")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			// Remove .md extension
			if strings.HasSuffix(name, ".md") {
				name = name[:len(name)-3]
			}
			names = append(names, name)
		}
	}
	return names, nil
}

// ClaudeContext implements ContextProvider for Claude Code using file + hook.
// Embeds BaseContextProvider for shared implementation.
type ClaudeContext struct {
	*BaseContextProvider
	backend *ClaudeCode
}

// NewClaudeContext creates a new Claude context provider.
func NewClaudeContext(backend *ClaudeCode) *ClaudeContext {
	return &ClaudeContext{
		BaseContextProvider: NewBaseContextProvider(),
		backend:             backend,
	}
}

// ClaudeSessionHistory implements SessionHistory for Claude Code.
// Reads from ~/.claude/projects/<hash>/session.jsonl
type ClaudeSessionHistory struct {
	backend  *ClaudeCode
	registry *BaseSessionRegistry
}

// NewClaudeSessionHistory creates a new Claude session history handler.
func NewClaudeSessionHistory(backend *ClaudeCode) *ClaudeSessionHistory {
	return &ClaudeSessionHistory{
		backend:  backend,
		registry: NewBaseSessionRegistry("claude-session-registry.json"),
	}
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *ClaudeSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	sessions, err := h.ListSessions(workDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}

	// Return most recent (ListSessions is sorted by time descending)
	return h.GetSession(workDir, sessions[0].ID)
}

// ListSessions returns available session metadata.
func (h *ClaudeSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	// Look for session files in the project directory
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read project directory: %w", err)
	}

	var sessions []SessionMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		sessions = append(sessions, SessionMeta{
			ID:        strings.TrimSuffix(entry.Name(), ".jsonl"),
			StartTime: info.ModTime(), // Approximate - would need to read file for exact
		})
	}

	// Sort by time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *ClaudeSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	return h.parseSessionFile(sessionPath)
}

// GetSessionByPath returns a session by its transcript file path.
func (h *ClaudeSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return h.parseSessionFile(path)
}

// findProjectDir finds the Claude project directory for the given workDir.
// Claude Code converts paths by replacing / with - and prefixing with -.
// Example: /home/user/project -> -home-user-project
func (h *ClaudeSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Claude Code converts paths: /home/user/project -> -home-user-project
	projectName := strings.ReplaceAll(absPath, string(filepath.Separator), "-")

	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)
	if _, err := os.Stat(projectDir); err != nil {
		return "", fmt.Errorf("project directory not found: %s", projectDir)
	}

	return projectDir, nil
}

// findSessionFile finds the main session file for the workDir.
func (h *ClaudeSessionHistory) findSessionFile(workDir string) (string, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return "", err
	}

	// Claude Code uses session.jsonl as the main session file
	sessionPath := filepath.Join(projectDir, "session.jsonl")
	if _, err := os.Stat(sessionPath); err != nil {
		return "", fmt.Errorf("session file not found: %s", sessionPath)
	}

	return sessionPath, nil
}

// parseSessionFile reads and parses a Claude session JSONL file.
func (h *ClaudeSessionHistory) parseSessionFile(path string) (*Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer file.Close()

	session := &Session{
		ID:      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Entries: []SessionEntry{},
	}

	scanner := bufio.NewScanner(file)
	// Increase buffer size for potentially large tool outputs
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := h.parseEntry(line)
		if err != nil {
			// Skip malformed entries
			continue
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

// claudeEntry represents a raw entry from Claude's session.jsonl.
type claudeEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
	// Tool-related fields
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Output    string          `json:"output"`
	IsError   bool            `json:"is_error"`
}

// parseEntry converts a Claude JSONL entry to a normalized SessionEntry.
func (h *ClaudeSessionHistory) parseEntry(line []byte) (*SessionEntry, error) {
	var raw claudeEntry
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

	// Map Claude entry types to normalized types
	switch raw.Type {
	case "user", "human":
		entry.Type = EntryTypeUser
		// Extract content from message if present
		if len(raw.Message) > 0 {
			var msg struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(raw.Message, &msg) == nil {
				entry.Content = msg.Content
			} else {
				// Try as plain string
				var content string
				if json.Unmarshal(raw.Message, &content) == nil {
					entry.Content = content
				}
			}
		}

	case "assistant":
		entry.Type = EntryTypeAssistant
		if len(raw.Message) > 0 {
			var msg struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(raw.Message, &msg) == nil {
				entry.Content = msg.Content
			}
		}

	case "tool_use":
		entry.Type = EntryTypeToolUse
		entry.ToolName = raw.Name
		entry.ToolInput = raw.Input

	case "tool_result":
		entry.Type = EntryTypeToolResult
		entry.ToolName = raw.Name
		entry.ToolOutput = raw.Output
		entry.IsError = raw.IsError

	default:
		// Unknown type - skip
		return nil, nil
	}

	return entry, nil
}

// TranscriptPathFromHook computes the transcript path from hook input.
// For Claude, we compute the path from sessionID + workDir.
func (h *ClaudeSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	if sessionID == "" {
		return ""
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	projectName := strings.ReplaceAll(absPath, string(filepath.Separator), "-")
	return filepath.Join(homeDir, ".claude", "projects", projectName, sessionID+".jsonl")
}

// RegisterSession records a session transcript path for the given SCM run (by PID).
// Delegates to BaseSessionRegistry for shared implementation with file locking.
func (h *ClaudeSessionHistory) RegisterSession(workDir string, pid int, transcriptPath string) error {
	return h.registry.RegisterSession(workDir, pid, transcriptPath)
}

// GetPreviousSession returns the session before the current one for /clear recovery.
// Delegates to BaseSessionRegistry for shared implementation with file locking.
func (h *ClaudeSessionHistory) GetPreviousSession(workDir string, pid int) (*Session, error) {
	return h.registry.GetPreviousSession(workDir, pid, h.parseSessionFile)
}
