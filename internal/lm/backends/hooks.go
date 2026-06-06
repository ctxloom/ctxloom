package backends

import (
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/ctxloom/internal/agent/claude"
	"github.com/ctxloom/ctxloom/internal/agent/gemini"
	"github.com/ctxloom/shared/wire"
	"github.com/spf13/afero"
)

// SettingsWriter is the agent settings-writing contract. It lives in
// internal/agent (the engine-agnostic core) so a consumer can take the settings
// facet without the launch facet (Backend); aliased here for existing sites.
type SettingsWriter = agent.SettingsWriter

// HookWriter is kept for backwards compatibility.
type HookWriter = SettingsWriter

// Settings options + shared write helpers live in internal/agent (the
// engine-agnostic core) so the per-agent writers can use them without importing
// backends. settingsOptions is the unexported alias the local registry keeps
// using; SettingsOption and the With* funcs are re-exported for external callers
// (internal/operations).
type settingsOptions = agent.SettingsOptions

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
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}

	writer := newSettingsWriter(backendName, options)
	if writer == nil {
		return nil // Backend doesn't support settings
	}
	return writer.WriteSettings(hooks, mcp, bundleMCP, projectDir)
}

// settingsWriterRegistry maps backend names to their settings writer constructors.
var settingsWriterRegistry = map[string]func(*settingsOptions) SettingsWriter{
	"claude-code": func(o *settingsOptions) SettingsWriter {
		return claude.NewWriter(*o)
	},
	"gemini": func(o *settingsOptions) SettingsWriter { return gemini.NewWriter(*o) },
}

// newSettingsWriter constructs the named backend's writer from the resolved
// options, or nil if the backend doesn't support settings.
func newSettingsWriter(name string, o *settingsOptions) SettingsWriter {
	if constructor, ok := settingsWriterRegistry[name]; ok {
		return constructor(o)
	}
	return nil
}

// GetSettingsWriter returns a SettingsWriter for the named backend, or nil if not supported.
// If fs is provided, it will be used for filesystem operations; otherwise the OS filesystem is used.
func GetSettingsWriter(name string, fs afero.Fs) SettingsWriter {
	return newSettingsWriter(name, &settingsOptions{FS: fs})
}

// BackendsWithSettings returns the names of all backends that support settings.
func BackendsWithSettings() []string {
	names := make([]string, 0, len(settingsWriterRegistry))
	for name := range settingsWriterRegistry {
		names = append(names, name)
	}
	return names
}

// computeHookHash delegates to agent.ComputeHookHash (transitional wrapper —
// removed when the writers move to the per-agent packages).
func computeHookHash(h wire.Hook) string { return agent.ComputeHookHash(h) }

// =============================================================================
// Shared Helper Functions
// =============================================================================
// These helpers reduce code duplication between ClaudeCodeHookWriter and
// GeminiHookWriter implementations.

func getFS(fs afero.Fs) afero.Fs { return agent.GetFS(fs) }

func warn(format string, args ...any) { agent.Warn(format, args...) }

func atomicWriteFile(fs afero.Fs, path string, data []byte, desc string) error {
	return agent.AtomicWriteFile(fs, path, data, desc)
}

// Shared symbols used by symlink.go and the per-agent capabilities.
const ctxloomBinary = agent.CtxloomBinary

var ctxloomMCPArgs = agent.CtxloomMCPArgs

func isCtxloomManaged(command string) bool { return agent.IsManaged(command, "ctxloom") }
