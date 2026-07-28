package backends

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// RemoveSettings strips ctxloom-managed artifacts from the named backend's
// config files. Unsupported backends are a no-op. Use WithSettingsFS for tests.
func RemoveSettings(backendName, projectDir string, opts ...SettingsOption) error {
	options := &agent.SettingsOptions{}
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
func BackendStatus(backendName, projectDir string, opts ...SettingsOption) (agent.SettingsStatus, error) {
	options := &agent.SettingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := GetSettingsWriter(backendName, options.FS)
	if writer == nil {
		return agent.SettingsStatus{}, nil
	}
	return writer.Status(projectDir)
}
