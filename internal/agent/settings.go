package agent

import "github.com/ctxloom/shared/wire"

// SettingsWriter writes hooks and MCP servers to an agent's settings files. It
// is the SETTINGS facet of an agent, deliberately separate from the launch
// facet (Backend) so a consumer — ltk, which only writes a hook — can depend on
// it without dragging in any launch/run machinery.
//
// NOTE (extraction): the config.* parameter types are ctxloom's normalized hook
// shape. When this package becomes github.com/ctxloom/agent those normalized
// types move here too (the UnifiedHooks contract), so the core owns the
// normalized form and each agent owns only the wire-format conversion.
type SettingsWriter interface {
	// WriteSettings writes hooks and MCP servers to the agent's config file,
	// preserving user-defined settings and adding/updating managed ones.
	// bundleMCP carries MCP servers resolved from profile bundles.
	WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error

	// RemoveSettings strips every managed hook, statusline, and MCP server from
	// the agent's config files, preserving user-defined entries. Absent files
	// are left absent (uninstall never creates files).
	RemoveSettings(projectDir string) error

	// Status reports which managed artifacts are currently wired in.
	Status(projectDir string) (SettingsStatus, error)

	// WriteHooks writes hooks only (backwards compatible).
	WriteHooks(cfg *wire.HooksConfig, projectDir string) error

	// SettingsPath returns the path to the settings file.
	SettingsPath(projectDir string) string

	// HooksPath is an alias for SettingsPath.
	HooksPath(projectDir string) string
}

// SettingsStatus reports which managed artifacts an agent has wired into its
// settings files.
type SettingsStatus struct {
	SettingsExists bool // the agent's settings file is present
	HooksPresent   bool // at least one managed hook is configured
	StatusLine     bool // a managed statusline is configured
	MCPPresent     bool // at least one managed MCP server is configured
}

// Wired reports whether any managed artifact is present.
func (s SettingsStatus) Wired() bool {
	return s.HooksPresent || s.StatusLine || s.MCPPresent
}
