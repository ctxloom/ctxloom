package backends

import (
	"github.com/ctxloom/ctxloom/internal/config"
)

// BaseLifecycle provides shared lifecycle handler logic for backends.
// It manages hooks and MCP configuration that are written to backend settings files.
type BaseLifecycle struct {
	backendName string
	hooks       *config.HooksConfig
	mcp         *config.MCPConfig
}

// NewBaseLifecycle creates a new lifecycle handler for the given backend.
func NewBaseLifecycle(backendName string) *BaseLifecycle {
	return &BaseLifecycle{
		backendName: backendName,
	}
}

// OnSessionStart registers a handler for session start events.
func (l *BaseLifecycle) OnSessionStart(workDir string, handler EventHandler) error {
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
func (l *BaseLifecycle) OnSessionEnd(workDir string, handler EventHandler) error {
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
func (l *BaseLifecycle) OnToolUse(workDir string, event ToolEvent, handler EventHandler) error {
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

// Clear removes all ctxloom-managed lifecycle handlers.
func (l *BaseLifecycle) Clear(workDir string) error {
	l.hooks = &config.HooksConfig{
		Plugins: make(map[string]config.BackendHooks),
	}
	l.mcp = &config.MCPConfig{
		Servers: make(map[string]config.MCPServer),
		Plugins: make(map[string]map[string]config.MCPServer),
	}
	return WriteSettings(l.backendName, l.hooks, l.mcp, nil, workDir)
}

// Flush writes accumulated hooks and MCP config to the settings file.
func (l *BaseLifecycle) Flush(workDir string) error {
	if l.hooks == nil && l.mcp == nil {
		return nil
	}
	return WriteSettings(l.backendName, l.hooks, l.mcp, nil, workDir)
}

// MergeConfigHooks merges hooks and MCP config from the configuration into this lifecycle.
func (l *BaseLifecycle) MergeConfigHooks(cfg *config.Config, workDir string, contextHash string) {
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

// ensureHooks initializes hooks config if nil.
func (l *BaseLifecycle) ensureHooks() {
	if l.hooks == nil {
		l.hooks = &config.HooksConfig{
			Plugins: make(map[string]config.BackendHooks),
		}
	}
}

// ensureMCP initializes MCP config if nil.
func (l *BaseLifecycle) ensureMCP() {
	if l.mcp == nil {
		l.mcp = &config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
			Plugins: make(map[string]map[string]config.MCPServer),
		}
	}
}

// GetHooks returns the current hooks configuration.
func (l *BaseLifecycle) GetHooks() *config.HooksConfig {
	return l.hooks
}

// GetMCP returns the current MCP configuration.
func (l *BaseLifecycle) GetMCP() *config.MCPConfig {
	return l.mcp
}
