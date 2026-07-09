package agent

import "github.com/ctxloom/ctxloom/internal/shared/wire"

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
}

// ContextWriter is the CONTEXT facet of an agent: it writes the assembled
// context string to the backend's native on-disk context surface (CLAUDE.md,
// .agents/AGENTS.md, .kiro/steering/…). It is a SIBLING of SettingsWriter, not
// an extension — a backend with no native context surface (codex/acp/mock)
// simply does not implement it, so the dispatch opts it out. Kept separate so a
// consumer that only writes context does not drag in the hooks/MCP surface, and
// a settings-only backend does not grow an unused WriteContext method.
type ContextWriter interface {
	// WriteContext writes the assembled context to the backend's native
	// surface under req.ProjectDir, replacing any prior ctxloom-managed
	// content, and reports the relative paths it wrote (or removed).
	WriteContext(req ContextWriteRequest) (ContextReport, error)
}

// ContextWriteRequest carries the assembled context payload for a WriteContext
// call. Context is the fully assembled string; each backend chooses its own
// framing/markers. It is a struct (not a bare string) so the request can grow
// without a signature churn — MCP/skills/hooks stay on their own delivery paths.
type ContextWriteRequest struct {
	ProjectDir string
	Context    string
}

// ContextReport lists the backend-relative paths a WriteContext call wrote or
// removed, for the caller's write report and later clean removal.
type ContextReport struct {
	Wrote   []string
	Removed []string
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
