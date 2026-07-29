package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// RemoveSettings strips ctxloom-managed artifacts from the named backend's
// config files. Unsupported backends are a no-op. Use WithSettingsFS for tests.
// U057-F16 gave this one a backend-named error wrap; BackendStatus's error
// return is deliberately left alone (its finding, U057-F27, is a narrower,
// separately-scoped fix — the two intentionally diverge here).
// reprise:accept-drift
func RemoveSettings(backendName, projectDir string, opts ...SettingsOption) error {
	options := &agent.SettingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := GetSettingsWriter(backendName, options.FS)
	if writer == nil {
		return nil
	}
	if err := writer.RemoveSettings(projectDir); err != nil {
		// U057-F16: the writer's own error names no backend, so a
		// multi-backend loop (operations.RemoveHooks) could not attribute a
		// failure to its source.
		return fmt.Errorf("backend %q: %w", backendName, err)
	}
	return nil
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
