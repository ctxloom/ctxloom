package backends

import (
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
	"github.com/spf13/afero"
)

// Settings options + shared write helpers live in shared/agent (the
// engine-agnostic core) so the per-agent writers can use them without importing
// backends. SettingsOption and the With* funcs are re-exported for external
// callers (internal/operations).
type SettingsOption = agent.SettingsOption

var (
	WithSettingsFS         = agent.WithSettingsFS
	WithStatusLineDisabled = agent.WithStatusLineDisabled
)

// WriteSettings writes hooks and MCP servers for the specified backend.
// If the backend doesn't support settings, this is a no-op.
// bundleMCP contains MCP servers resolved from profile bundles.
// Use WithSettingsFS to provide a custom filesystem for testing.
func WriteSettings(backendName string, hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string, opts ...SettingsOption) error {
	options := &agent.SettingsOptions{}
	for _, opt := range opts {
		opt(options)
	}

	writer := newSettingsWriter(backendName, options)
	if writer == nil {
		return nil // Backend doesn't support settings
	}
	return writer.WriteSettings(hooks, mcp, bundleMCP, projectDir)
}

// newSettingsWriter constructs the named backend's writer from the resolved
// options, or nil if the backend doesn't support settings. The per-backend
// writer constructors live in the descriptor table (registry.go).
func newSettingsWriter(name string, o *agent.SettingsOptions) agent.SettingsWriter {
	if d, ok := descriptors[name]; ok && d.newWriter != nil {
		return d.newWriter(*o)
	}
	return nil
}

// GetSettingsWriter returns a settings writer for the named backend, or nil if not supported.
// If fs is provided, it will be used for filesystem operations; otherwise the OS filesystem is used.
func GetSettingsWriter(name string, fs afero.Fs) agent.SettingsWriter {
	return newSettingsWriter(name, &agent.SettingsOptions{FS: fs})
}

// BackendsWithSettings returns the names of all backends that support settings.
func BackendsWithSettings() []string {
	names := make([]string, 0, len(descriptors))
	for name, d := range descriptors {
		if d.newWriter != nil {
			names = append(names, name)
		}
	}
	return names
}

// The per-agent settings-writer helpers (hook-hash, managed-command detection,
// fs/atomic-write, ctxloom binary/args) now live in shared/agent and are used
// directly by the claude/antigravity/codex writer modules — the transitional wrappers
// that used to bridge them here are gone.
