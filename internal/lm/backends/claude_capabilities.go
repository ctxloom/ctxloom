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
	"github.com/SophisticatedContextManager/scm/internal/config"
)

// ClaudeLifecycle implements LifecycleHandler for Claude Code using hooks.
type ClaudeLifecycle struct {
	backend *ClaudeCode
	hooks   *config.HooksConfig
	mcp     *config.MCPConfig
}

// OnSessionStart registers a handler for session start events.
func (l *ClaudeLifecycle) OnSessionStart(workDir string, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Type:    "command",
		Timeout: handler.Timeout,
	}
	l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart, hook)
	return nil
}

// OnSessionEnd registers a handler for session end events.
func (l *ClaudeLifecycle) OnSessionEnd(workDir string, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Type:    "command",
		Timeout: handler.Timeout,
	}
	l.hooks.Unified.SessionEnd = append(l.hooks.Unified.SessionEnd, hook)
	return nil
}

// OnToolUse registers a handler for tool use events.
func (l *ClaudeLifecycle) OnToolUse(workDir string, event ToolEvent, handler EventHandler) error {
	l.ensureHooks()
	hook := config.Hook{
		Command: handler.Command,
		Type:    "command",
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
func (l *ClaudeLifecycle) Clear(workDir string) error {
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
func (l *ClaudeLifecycle) Flush(workDir string) error {
	if l.hooks == nil && l.mcp == nil {
		return nil
	}
	return WriteSettings(l.backend.Name(), l.hooks, l.mcp, nil, workDir)
}

// MergeConfigHooks merges hooks and MCP config from the configuration into this lifecycle.
func (l *ClaudeLifecycle) MergeConfigHooks(cfg *config.Config, workDir string, contextHash string) {
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

func (l *ClaudeLifecycle) ensureHooks() {
	if l.hooks == nil {
		l.hooks = &config.HooksConfig{
			Plugins: make(map[string]config.BackendHooks),
		}
	}
}

func (l *ClaudeLifecycle) ensureMCP() {
	if l.mcp == nil {
		l.mcp = &config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
			Plugins: make(map[string]map[string]config.MCPServer),
		}
	}
}

// ClaudeMCPManager implements MCPManager for Claude Code.
type ClaudeMCPManager struct {
	backend *ClaudeCode
	servers map[string]MCPServer
}

// RegisterServer adds an MCP server to the backend configuration.
func (m *ClaudeMCPManager) RegisterServer(workDir string, server MCPServer) error {
	m.ensureServers()
	m.servers[server.Name] = server
	return nil
}

// UnregisterServer removes an MCP server from the backend configuration.
func (m *ClaudeMCPManager) UnregisterServer(workDir string, name string) error {
	m.ensureServers()
	delete(m.servers, name)
	return nil
}

// ListServers returns the names of registered MCP servers.
func (m *ClaudeMCPManager) ListServers(workDir string) ([]string, error) {
	m.ensureServers()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	return names, nil
}

// GetServer returns the configuration for a specific MCP server.
func (m *ClaudeMCPManager) GetServer(workDir string, name string) (*MCPServer, error) {
	m.ensureServers()
	if srv, ok := m.servers[name]; ok {
		return &srv, nil
	}
	return nil, nil
}

// Clear removes all SCM-managed MCP servers.
func (m *ClaudeMCPManager) Clear(workDir string) error {
	m.servers = make(map[string]MCPServer)
	return m.Flush(workDir)
}

// Flush writes all pending MCP configuration changes.
func (m *ClaudeMCPManager) Flush(workDir string) error {
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

func (m *ClaudeMCPManager) ensureServers() {
	if m.servers == nil {
		m.servers = make(map[string]MCPServer)
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
type ClaudeContext struct {
	backend     *ClaudeCode
	contextHash string
}

// Provide writes context to a file that the session start hook will read.
func (c *ClaudeContext) Provide(workDir string, fragments []*Fragment) error {
	hash, err := WriteContextFile(workDir, fragments)
	if err != nil {
		return err
	}
	c.contextHash = hash
	return nil
}

// Clear removes the context file.
func (c *ClaudeContext) Clear(workDir string) error {
	if c.contextHash != "" {
		contextPath := filepath.Join(workDir, SCMContextSubdir, c.contextHash+".md")
		_ = os.Remove(contextPath)
		c.contextHash = ""
	}
	return nil
}

// GetContextHash returns the hash of the current context file.
func (c *ClaudeContext) GetContextHash() string {
	return c.contextHash
}

// GetContextFilePath returns the path to the context file (for env var).
func (c *ClaudeContext) GetContextFilePath() string {
	if c.contextHash == "" {
		return ""
	}
	return filepath.Join(SCMContextSubdir, c.contextHash+".md")
}

// ClaudeSessionHistory implements SessionHistory for Claude Code.
// Reads from ~/.claude/projects/<hash>/session.jsonl
type ClaudeSessionHistory struct {
	backend *ClaudeCode
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
