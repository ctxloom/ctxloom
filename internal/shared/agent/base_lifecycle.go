package agent

import (
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// BaseLifecycle provides shared lifecycle handler logic for backends: it folds
// the host-assembled ManagedConfig (hooks + bundle MCP servers) into its merged
// state (MergeManaged), which the surfaces × cells Setup then reads via GetHooks /
// GetBundleMCP to write each settings/config surface. The statusline policy travels on
// ManagedConfig itself (read directly by Setup), not through the lifecycle.
type BaseLifecycle struct {
	backendName string
	hooks       *wire.HooksConfig
	bundleMCP   map[string]wire.MCPServer
}

// NewBaseLifecycle creates a new lifecycle handler for the given backend.
func NewBaseLifecycle(backendName string) *BaseLifecycle {
	return &BaseLifecycle{
		backendName: backendName,
	}
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

	if m.Hooks != nil {
		MergeHooksConfig(l.hooks, m.Hooks)
	}
	if contextHash != "" {
		l.hooks.Unified.SessionStart = append(l.hooks.Unified.SessionStart,
			NewContextInjectionHooks(contextHash, workDir)...)
	}

	// Bundle MCP servers are the WHOLE managed set — every MCP server, ctxloom's
	// own included, reaches a session through some bundle — and they carry their
	// own bundle-source _ctxloom marker. Mirrors operations.ApplyHooks, which
	// hands the same ResolveBundleMCPServers() set as its bundleMCP argument.
	if m.BundleMCP != nil {
		l.bundleMCP = m.BundleMCP
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

// ChatMCPServers composes the managed MCP set this lifecycle holds into
// chat-injectable server entries (see ComposeChatMCPServers). nil until
// MergeManaged has folded a managed payload in — a skip-setup run merges nothing,
// so it injects nothing.
func (l *BaseLifecycle) ChatMCPServers(override string) []ChatMCPServer {
	return ComposeChatMCPServers(override, l.bundleMCP, nil)
}

// GetHooks returns the current hooks configuration.
func (l *BaseLifecycle) GetHooks() *wire.HooksConfig {
	return l.hooks
}

// GetBundleMCP returns the managed MCP server set this lifecycle holds.
func (l *BaseLifecycle) GetBundleMCP() map[string]wire.MCPServer {
	return l.bundleMCP
}
