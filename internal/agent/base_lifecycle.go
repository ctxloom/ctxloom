package agent

import (
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

// MergeManaged folds the host-assembled ManagedConfig into this lifecycle and
// appends the agent's own context-injection hook. It is the wire-only successor
// to MergeConfigHooks: the host now resolves config/profile/bundle hooks and MCP
// servers (backends.AssembleManagedConfig) and ships them over the wire, so the
// agent never touches ctxloom config.
//
// m.Hooks is the config+default-profile+bundle set WITHOUT context-injection,
// kept identical to the operations.ApplyHooks write (which also assembles via
// backends.AssembleManagedHooks) so WriteSettings' remove-all-then-re-add
// reconcile can't drop a hook one writer assembled but the other didn't — the
// failure class that once broke forward-bind. The context-injection hook is
// appended here from the plugin-side contextHash, the one piece only the agent
// knows.
func (l *BaseLifecycle) MergeManaged(m *ManagedConfig, workDir string, contextHash string) {
	if m == nil {
		return
	}
	l.ensureHooks()
	l.ensureMCP()
	l.statusLineDisabled = !m.ManageStatusline

	if m.Hooks != nil {
		MergeHooksConfig(l.hooks, m.Hooks)
	}
	if contextHash != "" {
		l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart,
			NewContextInjectionHooks(contextHash, workDir)...)
	}

	if m.MCP != nil {
		wire.MergeMCPConfig(l.mcp, m.MCP)
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
