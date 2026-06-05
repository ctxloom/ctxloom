package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/spf13/afero"
)

// SettingsStatus reports which ctxloom-managed artifacts a backend has wired
// into its config files. Defined in internal/agent; aliased here for existing
// call sites (and its Wired method).
type SettingsStatus = agent.SettingsStatus

// RemoveSettings strips ctxloom-managed artifacts from the named backend's
// config files. Unsupported backends are a no-op. Use WithSettingsFS for tests.
func RemoveSettings(backendName, projectDir string, opts ...SettingsOption) error {
	options := &settingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := GetSettingsWriter(backendName, options.FS)
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
	writer := GetSettingsWriter(backendName, options.FS)
	if writer == nil {
		return SettingsStatus{}, nil
	}
	return writer.Status(projectDir)
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
		if server.SCM != "" || server.Command != "" && isCtxloomManaged(server.Command) {
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
				if hook.Name == geminiCtxloomHookName || isCtxloomManaged(hook.Command) {
					return true
				}
			}
		}
	}
	return false
}
