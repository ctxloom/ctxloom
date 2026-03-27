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

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
)

// geminiSCMCommandsDir is the subdirectory for SCM-managed Gemini commands.
const geminiSCMCommandsDir = "scm"

// GeminiCommand represents a Gemini CLI slash command in TOML format.
type GeminiCommand struct {
	Description string `toml:"description,omitempty"`
	Prompt      string `toml:"prompt"`
}

// GeminiLifecycle implements LifecycleHandler for Gemini using hooks.
type GeminiLifecycle struct {
	backend *Gemini
	hooks   *config.HooksConfig
	mcp     *config.MCPConfig
}

// OnSessionStart registers a handler for session start events.
func (l *GeminiLifecycle) OnSessionStart(workDir string, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Timeout: handler.Timeout,
	}
	l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart, hook)
	return nil
}

// OnSessionEnd registers a handler for session end events.
func (l *GeminiLifecycle) OnSessionEnd(workDir string, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Timeout: handler.Timeout,
	}
	l.hooks.Unified.SessionEnd = append(l.hooks.Unified.SessionEnd, hook)
	return nil
}

// OnToolUse registers a handler for tool use events.
func (l *GeminiLifecycle) OnToolUse(workDir string, event ToolEvent, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Timeout: handler.Timeout,
	}
	switch event {
	case BeforeToolUse:
		l.hooks.Unified.PreTool = append(l.hooks.Unified.PreTool, hook)
	case AfterToolUse:
		l.hooks.Unified.PostTool = append(l.hooks.Unified.PostTool, hook)
	}
	return nil
}

// Clear removes all SCM-managed lifecycle handlers.
func (l *GeminiLifecycle) Clear(workDir string) error {
	l.hooks = &config.HooksConfig{
		Plugins: make(map[string]config.BackendHooks),
	}
	l.mcp = &config.MCPConfig{
		Servers: make(map[string]config.MCPServer),
		Plugins: make(map[string]map[string]config.MCPServer),
	}
	return WriteSettings(l.backend.Name(), l.hooks, l.mcp, nil, workDir)
}

// Flush writes accumulated hooks and MCP config to the settings file.
func (l *GeminiLifecycle) Flush(workDir string) error {
	if l.hooks == nil && l.mcp == nil {
		return nil
	}
	return WriteSettings(l.backend.Name(), l.hooks, l.mcp, nil, workDir)
}

// MergeConfigHooks merges hooks and MCP config from the configuration into this lifecycle.
func (l *GeminiLifecycle) MergeConfigHooks(cfg *config.Config, workDir string, contextHash string) {
	l.ensureHooks()
	l.ensureMCP()

	// Auto-register context injection hook with the context hash
	if contextHash != "" {
		l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart, NewContextInjectionHook(contextHash, workDir))
	}

	// Merge top-level hooks and MCP
	mergeHooksConfig(l.hooks, &cfg.Hooks)
	MergeMCPConfig(l.mcp, &cfg.MCP)

	// Merge from default profiles
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles, profileName)
		if err != nil {
			continue
		}
		mergeHooksConfig(l.hooks, &resolved.Hooks)
		MergeMCPConfig(l.mcp, &resolved.MCP)
	}
}

func (l *GeminiLifecycle) ensureHooks() {
	if l.hooks == nil {
		l.hooks = &config.HooksConfig{
			Plugins: make(map[string]config.BackendHooks),
		}
	}
}

func (l *GeminiLifecycle) ensureMCP() {
	if l.mcp == nil {
		l.mcp = &config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
			Plugins: make(map[string]map[string]config.MCPServer),
		}
	}
}

// GeminiMCPManager implements MCPManager for Gemini CLI.
type GeminiMCPManager struct {
	backend *Gemini
	servers map[string]MCPServer
}

// RegisterServer adds an MCP server to the backend configuration.
func (m *GeminiMCPManager) RegisterServer(workDir string, server MCPServer) error {
	m.ensureServers()
	m.servers[server.Name] = server
	return nil
}

// UnregisterServer removes an MCP server from the backend configuration.
func (m *GeminiMCPManager) UnregisterServer(workDir string, name string) error {
	m.ensureServers()
	delete(m.servers, name)
	return nil
}

// ListServers returns the names of registered MCP servers.
func (m *GeminiMCPManager) ListServers(workDir string) ([]string, error) {
	m.ensureServers()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	return names, nil
}

// GetServer returns the configuration for a specific MCP server.
func (m *GeminiMCPManager) GetServer(workDir string, name string) (*MCPServer, error) {
	m.ensureServers()
	if srv, ok := m.servers[name]; ok {
		return &srv, nil
	}
	return nil, nil
}

// Clear removes all SCM-managed MCP servers.
func (m *GeminiMCPManager) Clear(workDir string) error {
	m.servers = make(map[string]MCPServer)
	return m.Flush(workDir)
}

// Flush writes all pending MCP configuration changes.
func (m *GeminiMCPManager) Flush(workDir string) error {
	m.ensureServers()

	// Convert to config.MCPConfig
	mcpCfg := &config.MCPConfig{
		Servers: make(map[string]config.MCPServer),
	}
	for name, srv := range m.servers {
		mcpCfg.Servers[name] = config.MCPServer{
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
		}
	}

	// Write settings (hooks are nil, just MCP)
	return WriteSettings(m.backend.Name(), nil, mcpCfg, nil, workDir)
}

func (m *GeminiMCPManager) ensureServers() {
	if m.servers == nil {
		m.servers = make(map[string]MCPServer)
	}
}

// GeminiContext implements ContextProvider for Gemini using file + hook.
type GeminiContext struct {
	backend     *Gemini
	contextHash string
}

// Provide writes context to a file that the session start hook will read.
func (c *GeminiContext) Provide(workDir string, fragments []*Fragment) error {
	hash, err := WriteContextFile(workDir, fragments)
	if err != nil {
		return err
	}
	c.contextHash = hash
	return nil
}

// Clear removes the context file.
func (c *GeminiContext) Clear(workDir string) error {
	if c.contextHash != "" {
		contextPath := SCMContextSubdir + "/" + c.contextHash + ".md"
		_ = os.Remove(contextPath)
		c.contextHash = ""
	}
	return nil
}

// GetContextHash returns the hash of the current context file.
func (c *GeminiContext) GetContextHash() string {
	return c.contextHash
}

// GetContextFilePath returns the path to the context file (for env var).
func (c *GeminiContext) GetContextFilePath() string {
	if c.contextHash == "" {
		return ""
	}
	return SCMContextSubdir + "/" + c.contextHash + ".md"
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

// Clear removes all SCM-managed skills.
func (s *GeminiSkills) Clear(workDir string) error {
	scmDir := filepath.Join(workDir, ".gemini", "commands", geminiSCMCommandsDir)
	return os.RemoveAll(scmDir)
}

// List returns registered skill names.
func (s *GeminiSkills) List(workDir string) ([]string, error) {
	scmDir := filepath.Join(workDir, ".gemini", "commands", geminiSCMCommandsDir)
	entries, err := os.ReadDir(scmDir)
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
// It deletes the .gemini/commands/scm/ directory and regenerates it fresh.
// Only prompts with Gemini.IsEnabled() == true are exported.
func WriteGeminiCommandFiles(workDir string, prompts []*bundles.LoadedContent) error {
	scmDir := filepath.Join(workDir, ".gemini", "commands", geminiSCMCommandsDir)

	// Clean slate - remove and recreate
	if err := os.RemoveAll(scmDir); err != nil {
		return fmt.Errorf("remove scm commands dir: %w", err)
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

	if err := os.MkdirAll(scmDir, 0755); err != nil {
		return fmt.Errorf("create scm commands dir: %w", err)
	}

	for _, p := range prompts {
		if !p.Plugins.LM.Gemini.IsEnabled() {
			continue // Explicitly disabled
		}

		tomlData, err := TransformToGeminiCommand(p)
		if err != nil {
			return fmt.Errorf("transform command %s: %w", p.Name, err)
		}

		path := filepath.Join(scmDir, p.Name+".toml")

		// Ensure parent directory exists for nested prompt names
		if dir := filepath.Dir(path); dir != scmDir {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create command subdir %s: %w", dir, err)
			}
		}

		if err := os.WriteFile(path, tomlData, 0644); err != nil {
			return fmt.Errorf("write command %s: %w", p.Name, err)
		}
	}

	return nil
}

// TransformToGeminiCommand converts an SCM prompt to Gemini CLI command format.
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
	backend *Gemini
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

// findProjectDir finds the Gemini project directory for the given workDir.
func (h *GeminiSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	// Gemini uses SHA256 hash of the absolute path
	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	hash := sha256.Sum256([]byte(absPath))
	projectHash := hex.EncodeToString(hash[:])

	projectDir := filepath.Join(homeDir, ".gemini", "tmp", projectHash)
	if _, err := os.Stat(projectDir); err != nil {
		return "", fmt.Errorf("project directory not found: %s", projectDir)
	}

	return projectDir, nil
}

// listChatFiles returns session metadata for all chat files, sorted by time (most recent first).
func (h *GeminiSessionHistory) listChatFiles(chatsDir string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(chatsDir)
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

		info, err := entry.Info()
		if err != nil {
			continue
		}

		sessions = append(sessions, SessionMeta{
			ID:        strings.TrimSuffix(entry.Name(), ".json"),
			StartTime: info.ModTime(), // Approximate
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
	data, err := os.ReadFile(path)
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
	Role      string          `json:"role"` // "user", "model"
	Content   string          `json:"content"`
	Timestamp string          `json:"timestamp"`
	ToolCalls []geminiToolUse `json:"tool_calls,omitempty"`
}

// geminiToolUse represents a tool call in Gemini's session.
type geminiToolUse struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  bool            `json:"error"`
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
