package backends

import (
	"fmt"

	"github.com/spf13/afero"
)

// SettingsStatus reports which ctxloom-managed artifacts a backend has wired
// into its config files.
type SettingsStatus struct {
	SettingsExists bool // the backend's settings file is present
	HooksPresent   bool // at least one ctxloom-managed hook is configured
	StatusLine     bool // a ctxloom-managed statusline is configured
	MCPPresent     bool // at least one ctxloom-managed MCP server is configured
}

// Wired reports whether any ctxloom-managed artifact is present.
func (s SettingsStatus) Wired() bool {
	return s.HooksPresent || s.StatusLine || s.MCPPresent
}

// RemoveSettings strips ctxloom-managed artifacts from the named backend's
// config files. Unsupported backends are a no-op. Use WithSettingsFS for tests.
func RemoveSettings(backendName, projectDir string, opts ...SettingsOption) error {
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := GetSettingsWriter(backendName, options.fs)
	if writer == nil {
		return nil
	}
	return writer.RemoveSettings(projectDir)
}

// BackendStatus reports the named backend's ctxloom wiring. Unsupported
// backends report an empty (un-wired) status.
func BackendStatus(backendName, projectDir string, opts ...SettingsOption) (SettingsStatus, error) {
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := GetSettingsWriter(backendName, options.fs)
	if writer == nil {
		return SettingsStatus{}, nil
	}
	return writer.Status(projectDir)
}

// RemoveSettings implements SettingsWriter for Claude Code: it clears
// ctxloom-managed hooks and statusline from settings.json and ctxloom-marked
// servers from .mcp.json, touching neither file when it does not already exist.
func (w *ClaudeCodeHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()

	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); exists {
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return fmt.Errorf("failed to load existing settings: %w", err)
		}
		w.removeCtxloomHooks(settings)
		if isCtxloomManagedStatusLine(settings.StatusLine) {
			settings.StatusLine = nil
		}
		if err := w.saveSettings(settingsPath, settings); err != nil {
			return err
		}
	}

	mcpPath := w.MCPConfigPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for name, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				delete(mcpConfig.MCPServers, name)
			}
		}
		if err := w.saveMCPConfig(mcpPath, mcpConfig); err != nil {
			return err
		}
	}
	return nil
}

// Status implements SettingsWriter for Claude Code.
func (w *ClaudeCodeHookWriter) Status(projectDir string) (SettingsStatus, error) {
	fs := w.getFS()
	var status SettingsStatus

	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); exists {
		status.SettingsExists = true
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing settings: %w", err)
		}
		status.HooksPresent = claudeHasManagedHook(settings)
		status.StatusLine = isCtxloomManagedStatusLine(settings.StatusLine)
	}

	mcpPath := w.MCPConfigPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for _, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				status.MCPPresent = true
				break
			}
		}
	}
	return status, nil
}

// claudeHasManagedHook reports whether any configured hook is ctxloom-managed.
func claudeHasManagedHook(settings *claudeCodeSettings) bool {
	for _, matchers := range settings.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if hook.SCM != "" || isCtxloomManagedHook(hook.Command) {
					return true
				}
			}
		}
	}
	return false
}

// RemoveSettings implements SettingsWriter for Gemini CLI: it clears
// ctxloom-managed hooks and MCP servers from settings.json, leaving an absent
// file absent.
func (w *GeminiHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return nil
	}
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing settings: %w", err)
	}
	w.removeCtxloomHooks(settings)
	w.removeCtxloomMCPServers(settings)
	return w.saveSettings(settingsPath, settings)
}

// Status implements SettingsWriter for Gemini CLI.
func (w *GeminiHookWriter) Status(projectDir string) (SettingsStatus, error) {
	fs := w.getFS()
	var status SettingsStatus
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return status, nil
	}
	status.SettingsExists = true
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return status, fmt.Errorf("failed to load existing settings: %w", err)
	}
	status.HooksPresent = geminiHasManagedHook(settings)
	for _, server := range settings.MCPServers {
		if server.SCM != "" || server.Command != "" && isCtxloomManagedHook(server.Command) {
			status.MCPPresent = true
			break
		}
	}
	return status, nil
}

// geminiHasManagedHook reports whether any configured hook is ctxloom-managed,
// descending Gemini's event→group→hooks[] shape.
func geminiHasManagedHook(settings *geminiSettings) bool {
	for _, groups := range settings.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Name == geminiCtxloomHookName || isCtxloomManagedHook(hook.Command) {
					return true
				}
			}
		}
	}
	return false
}
