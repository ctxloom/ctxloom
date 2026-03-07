package backends

import (
	"os"
	"path/filepath"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
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
		l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart, NewContextInjectionHook(contextHash))
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

// Clear removes all SCM-managed skills.
func (s *ClaudeSkills) Clear(workDir string) error {
	scmDir := filepath.Join(workDir, ".claude", "commands", SCMCommandsDir)
	return os.RemoveAll(scmDir)
}

// List returns registered skill names.
func (s *ClaudeSkills) List(workDir string) ([]string, error) {
	scmDir := filepath.Join(workDir, ".claude", "commands", SCMCommandsDir)
	entries, err := os.ReadDir(scmDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" {
			name := entry.Name()[:len(entry.Name())-3] // Remove .md
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
