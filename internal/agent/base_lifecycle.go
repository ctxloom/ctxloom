package agent

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/shared/wire"
)

// BaseLifecycle provides shared lifecycle handler logic for backends.
// It manages hooks and MCP configuration that are written to backend settings files.
type BaseLifecycle struct {
	backendName        string
	hooks              *wire.HooksConfig
	mcp                *wire.MCPConfig
	statusLineDisabled bool
	writeSettings      WriteSettingsFunc
}

// settingsOpts returns the write options reflecting accumulated lifecycle state
// (currently the statusline opt-out).
func (l *BaseLifecycle) settingsOpts() []SettingsOption {
	return []SettingsOption{WithStatusLineDisabled(l.statusLineDisabled)}
}

// NewBaseLifecycle creates a new lifecycle handler for the given backend. The
// writeSettings dispatch is injected so the base does not import the registry.
func NewBaseLifecycle(backendName string, writeSettings WriteSettingsFunc) *BaseLifecycle {
	return &BaseLifecycle{
		backendName:   backendName,
		writeSettings: writeSettings,
	}
}

// OnSessionStart registers a handler for session start events.
func (l *BaseLifecycle) OnSessionStart(workDir string, handler EventHandler) error {
	l.ensureHooks()
	hook := wire.Hook{
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
	hook := wire.Hook{
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
	hook := wire.Hook{
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
	l.hooks = &wire.HooksConfig{
		Plugins: make(map[string]wire.BackendHooks),
	}
	l.mcp = &wire.MCPConfig{
		Servers: make(map[string]wire.MCPServer),
		Plugins: make(map[string]map[string]wire.MCPServer),
	}
	return l.writeSettings(l.backendName, l.hooks, l.mcp, nil, workDir, l.settingsOpts()...)
}

// Flush writes accumulated hooks and MCP config to the settings file.
func (l *BaseLifecycle) Flush(workDir string) error {
	if l.hooks == nil && l.mcp == nil {
		return nil
	}
	return l.writeSettings(l.backendName, l.hooks, l.mcp, nil, workDir, l.settingsOpts()...)
}

// MergeConfigHooks merges hooks and MCP config from the configuration into this lifecycle.
func (l *BaseLifecycle) MergeConfigHooks(cfg *config.Config, workDir string, contextHash string) {
	l.ensureHooks()
	l.ensureMCP()
	l.statusLineDisabled = !cfg.Settings.ShouldManageStatusline()

	// Hooks: the complete managed set (config-level + default-profile +
	// bundle-shipped + context-injection) is assembled by the shared
	// AssembleManagedHooks so this Setup-time write and the later
	// operations.ApplyHooks write produce an identical set. Assembling a
	// partial set here is what left every `ctxloom run` session without
	// forward-bind; diverging from apply-hooks would resurface the same
	// drop-on-clobber class for any profile-shipped hook.
	mergeHooksConfig(l.hooks, AssembleManagedHooks(cfg, workDir, contextHash))

	// MCP: config-level + default-profile servers.
	wire.MergeMCPConfig(l.mcp, &cfg.MCP)
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName)
		if err != nil {
			continue
		}
		wire.MergeMCPConfig(l.mcp, &resolved.MCP)
	}
}

// ensureHooks initializes hooks config if nil.
func (l *BaseLifecycle) ensureHooks() {
	if l.hooks == nil {
		l.hooks = &wire.HooksConfig{
			Plugins: make(map[string]wire.BackendHooks),
		}
	}
}

// ensureMCP initializes MCP config if nil.
func (l *BaseLifecycle) ensureMCP() {
	if l.mcp == nil {
		l.mcp = &wire.MCPConfig{
			Servers: make(map[string]wire.MCPServer),
			Plugins: make(map[string]map[string]wire.MCPServer),
		}
	}
}

// GetHooks returns the current hooks configuration.
func (l *BaseLifecycle) GetHooks() *wire.HooksConfig {
	return l.hooks
}

// GetMCP returns the current MCP configuration.
func (l *BaseLifecycle) GetMCP() *wire.MCPConfig {
	return l.mcp
}
