package backends

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// geminiAppCommandsDir is the subdirectory for ctxloom-managed Gemini commands.
const geminiAppCommandsDir = "ctxloom"

// GeminiCommand represents a Gemini CLI slash command in TOML format.
type GeminiCommand struct {
	Description string `toml:"description,omitempty"`
	Prompt      string `toml:"prompt"`
}

// GeminiLifecycle implements LifecycleHandler for Gemini using hooks.
// Embeds BaseLifecycle for shared implementation.
type GeminiLifecycle struct {
	*BaseLifecycle
	backend *Gemini
}

// NewGeminiLifecycle creates a new Gemini lifecycle handler.
func NewGeminiLifecycle(backend *Gemini) *GeminiLifecycle {
	return &GeminiLifecycle{
		BaseLifecycle: NewBaseLifecycle("gemini"),
		backend:       backend,
	}
}

// GeminiMCPManager implements MCPManager for Gemini CLI.
// Embeds BaseMCPManager for shared implementation.
type GeminiMCPManager struct {
	*BaseMCPManager
	backend *Gemini
}

// NewGeminiMCPManager creates a new Gemini MCP manager.
func NewGeminiMCPManager(backend *Gemini) *GeminiMCPManager {
	return &GeminiMCPManager{
		BaseMCPManager: NewBaseMCPManager("gemini"),
		backend:        backend,
	}
}

// GeminiContext implements ContextProvider for Gemini using file + hook.
// Embeds BaseContextProvider for shared implementation.
type GeminiContext struct {
	*BaseContextProvider
	backend *Gemini
}

// NewGeminiContext creates a new Gemini context provider.
func NewGeminiContext(backend *Gemini) *GeminiContext {
	return &GeminiContext{
		BaseContextProvider: NewBaseContextProvider(),
		backend:             backend,
	}
}

// GeminiSkills implements SkillRegistry for Gemini CLI using slash commands.
type GeminiSkills struct {
	backend *Gemini
}

// Register adds a skill as a Gemini CLI slash command.
func (s *GeminiSkills) Register(workDir string, skill Skill) error {
	enabled := true
	content := &bundles.LoadedContent{
		Name:    skill.Name,
		Content: skill.Content,
	}
	content.Plugins.LM.Gemini.Enabled = &enabled
	content.Plugins.LM.Gemini.Description = skill.Description
	return WriteGeminiCommandFiles(workDir, []*bundles.LoadedContent{content})
}

// RegisterAll adds multiple skills as Gemini CLI slash commands.
func (s *GeminiSkills) RegisterAll(workDir string, skills []Skill) error {
	var contents []*bundles.LoadedContent
	enabled := true
	for _, skill := range skills {
		content := &bundles.LoadedContent{
			Name:    skill.Name,
			Content: skill.Content,
		}
		content.Plugins.LM.Gemini.Enabled = &enabled
		content.Plugins.LM.Gemini.Description = skill.Description
		contents = append(contents, content)
	}
	return WriteGeminiCommandFiles(workDir, contents)
}

// RegisterFromContent registers skills from LoadedContent objects.
func (s *GeminiSkills) RegisterFromContent(workDir string, contents []*bundles.LoadedContent) error {
	return WriteGeminiCommandFiles(workDir, contents)
}

// Clear removes all ctxloom-managed skills.
func (s *GeminiSkills) Clear(workDir string) error {
	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)
	return os.RemoveAll(appDir)
}

// List returns registered skill names.
func (s *GeminiSkills) List(workDir string) ([]string, error) {
	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)
	entries, err := os.ReadDir(appDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".toml" {
			name := entry.Name()[:len(entry.Name())-5] // Remove .toml
			names = append(names, name)
		}
	}
	return names, nil
}

// WriteGeminiCommandFiles generates Gemini CLI slash command files from prompts.
// It deletes the .gemini/commands/ctxloom/ directory and regenerates it fresh.
// Only prompts with Gemini.IsEnabled() == true are exported.
func WriteGeminiCommandFiles(workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	options := &commandFileOptions{fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(options)
	}
	fs := options.fs

	appDir := filepath.Join(workDir, ".gemini", "commands", geminiAppCommandsDir)

	// Clean slate - remove and recreate
	if err := fs.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove ctxloom commands dir: %w", err)
	}

	// Check if we have any prompts to export
	hasExportable := false
	for _, p := range prompts {
		if p.Plugins.LM.Gemini.IsEnabled() {
			hasExportable = true
			break
		}
	}

	// Only create directory if we have prompts to export
	if !hasExportable {
		return nil
	}

	if err := fs.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create ctxloom commands dir: %w", err)
	}

	for _, p := range prompts {
		if !p.Plugins.LM.Gemini.IsEnabled() {
			continue // Explicitly disabled
		}

		tomlData, err := TransformToGeminiCommand(p)
		if err != nil {
			return fmt.Errorf("transform command %s: %w", p.Name, err)
		}

		path := filepath.Join(appDir, p.Name+".toml")

		// Ensure parent directory exists for nested prompt names
		if dir := filepath.Dir(path); dir != appDir {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create command subdir %s: %w", dir, err)
			}
		}

		if err := afero.WriteFile(fs, path, tomlData, 0644); err != nil {
			return fmt.Errorf("write command %s: %w", p.Name, err)
		}
	}

	return nil
}

// TransformToGeminiCommand converts a ctxloom prompt to Gemini CLI command format.
// It generates a TOML file with prompt and description fields.
// Gemini uses {{args}} for argument injection natively.
func TransformToGeminiCommand(p *bundles.LoadedContent) ([]byte, error) {
	cmd := GeminiCommand{
		Description: p.Plugins.LM.Gemini.Description,
		Prompt:      p.Content,
	}

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(cmd); err != nil {
		return nil, fmt.Errorf("encode TOML: %w", err)
	}

	return buf.Bytes(), nil
}

// GeminiSessionHistory implements SessionHistory for Gemini CLI.
// Reads from ~/.gemini/tmp/<hash>/chats/*.json
type GeminiSessionHistory struct {
	backend  *Gemini
	registry *BaseSessionRegistry
	fs       afero.Fs
	homeDir  string // Override home directory for testing
}

// GeminiSessionHistoryOption configures GeminiSessionHistory.
type GeminiSessionHistoryOption func(*GeminiSessionHistory)

// WithGeminiSessionFS sets a custom filesystem for testing.
func WithGeminiSessionFS(fs afero.Fs) GeminiSessionHistoryOption {
	return func(h *GeminiSessionHistory) {
		h.fs = fs
	}
}

// WithGeminiSessionHomeDir sets a custom home directory for testing.
func WithGeminiSessionHomeDir(dir string) GeminiSessionHistoryOption {
	return func(h *GeminiSessionHistory) {
		h.homeDir = dir
	}
}

// NewGeminiSessionHistory creates a new Gemini session history handler.
func NewGeminiSessionHistory(backend *Gemini, opts ...GeminiSessionHistoryOption) *GeminiSessionHistory {
	h := &GeminiSessionHistory{
		backend:  backend,
		registry: NewBaseSessionRegistry("gemini-session-registry.json"),
		fs:       afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *GeminiSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	// Find most recent chat file
	chatsDir := filepath.Join(projectDir, "chats")
	sessions, err := h.listChatFiles(chatsDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found in %s", chatsDir)
	}

	// Return most recent
	return h.parseSessionFile(filepath.Join(chatsDir, sessions[0].ID+".json"))
}

// ListSessions returns available session metadata.
func (h *GeminiSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	chatsDir := filepath.Join(projectDir, "chats")
	return h.listChatFiles(chatsDir)
}

// GetSession returns a specific session by ID.
func (h *GeminiSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(projectDir, "chats", sessionID+".json")
	return h.parseSessionFile(sessionPath)
}

// GetSessionByPath returns a session by its transcript file path.
func (h *GeminiSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return h.parseSessionFile(path)
}

// findProjectDir finds the Gemini project directory for the given workDir.
func (h *GeminiSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir := h.homeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
	}

	// Gemini uses SHA256 hash of the absolute path
	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	hash := sha256.Sum256([]byte(absPath))
	projectHash := hex.EncodeToString(hash[:])

	projectDir := filepath.Join(homeDir, ".gemini", "tmp", projectHash)
	if _, err := h.fs.Stat(projectDir); err != nil {
		return "", fmt.Errorf("project directory not found: %s", projectDir)
	}

	return projectDir, nil
}

// listChatFiles returns session metadata for all chat files, sorted by time (most recent first).
func (h *GeminiSessionHistory) listChatFiles(chatsDir string) ([]SessionMeta, error) {
	entries, err := afero.ReadDir(h.fs, chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read chats directory: %w", err)
	}

	var sessions []SessionMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		sessions = append(sessions, SessionMeta{
			ID:        strings.TrimSuffix(entry.Name(), ".json"),
			StartTime: entry.ModTime(), // Approximate
		})
	}

	// Sort by time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// parseSessionFile reads and parses a Gemini session JSON file.
func (h *GeminiSessionHistory) parseSessionFile(path string) (*Session, error) {
	data, err := afero.ReadFile(h.fs, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	// Gemini stores sessions as a JSON object with messages array
	var rawSession geminiRawSession
	if err := json.Unmarshal(data, &rawSession); err != nil {
		return nil, fmt.Errorf("failed to parse session JSON: %w", err)
	}

	session := &Session{
		ID:      filepath.Base(path),
		Entries: []SessionEntry{},
	}

	for _, msg := range rawSession.Messages {
		entry := h.convertMessage(msg)
		if entry != nil {
			session.Entries = append(session.Entries, *entry)
		}
	}

	// Set start/end times from entries
	if len(session.Entries) > 0 {
		session.StartTime = session.Entries[0].Timestamp
		session.EndTime = session.Entries[len(session.Entries)-1].Timestamp
	}

	return session, nil
}

// geminiRawSession represents Gemini's session JSON structure.
type geminiRawSession struct {
	Messages []geminiMessage `json:"messages"`
}

// geminiMessage represents a message in Gemini's session.
type geminiMessage struct {
	Role      string `json:"role"` // "user", "model"
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// convertMessage converts a Gemini message to a normalized SessionEntry.
func (h *GeminiSessionHistory) convertMessage(msg geminiMessage) *SessionEntry {
	entry := &SessionEntry{}

	// Parse timestamp
	if msg.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}

	// Map Gemini roles to normalized types
	switch msg.Role {
	case "user":
		entry.Type = EntryTypeUser
		entry.Content = msg.Content

	case "model", "assistant":
		entry.Type = EntryTypeAssistant
		entry.Content = msg.Content

	default:
		return nil
	}

	return entry
}

// TranscriptPathFromHook returns the transcript path from hook input.
// For Gemini, the hook provides the path directly.
func (h *GeminiSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}

// RegisterSession records a session for the given ctxloom run (by PID).
// Delegates to BaseSessionRegistry for shared implementation with file locking.
func (h *GeminiSessionHistory) RegisterSession(workDir string, pid int, transcriptPath string) error {
	return h.registry.RegisterSession(workDir, pid, transcriptPath)
}

// GetPreviousSession returns the session before the current one for /clear recovery.
// Delegates to BaseSessionRegistry for shared implementation with file locking.
func (h *GeminiSessionHistory) GetPreviousSession(workDir string, pid int) (*Session, error) {
	return h.registry.GetPreviousSession(workDir, pid, h.parseSessionFile)
}
