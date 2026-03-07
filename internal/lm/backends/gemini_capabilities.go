package backends

import (
	"os"

	"github.com/benjaminabbitt/scm/internal/config"
)

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
