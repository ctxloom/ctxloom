package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// RemoveSettings strips ctxloom-managed artifacts from the named backend's
// config files. Unsupported backends are a no-op. Use WithSettingsFS for tests.
// This one gives errors a backend-named wrap; BackendStatus's error return is
// deliberately left alone (a narrower, separately-scoped fix — the two
// intentionally diverge here).
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
		// The writer's own error names no backend, so a
		// multi-backend loop (operations.RemoveHooks) could not attribute a
		// failure to its source.
		return fmt.Errorf("backend %q: %w", backendName, err)
	}
	return nil
}

// BackendStatus reports the named backend's ctxloom wiring. A registered
// backend with no settings writer (acp, mock — deliberately no native config
// format) reports an empty (un-wired) status with a nil error: a legitimate
// "nothing to report". An UNREGISTERED name errors instead: before
// this, both cases returned the identical zero status + nil error, so a
// typo'd backend name was indistinguishable from a real, wired-nothing read —
// a caller could not tell "you asked about something that doesn't exist" from
// "this backend genuinely has nothing installed".
func BackendStatus(backendName, projectDir string, opts ...SettingsOption) (agent.SettingsStatus, error) {
	if !Exists(backendName) {
		return agent.SettingsStatus{}, fmt.Errorf("unknown backend %q", backendName)
	}
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
