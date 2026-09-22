package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// BackendStatus reports the named backend's ctxloom wiring. A registered
// backend with no settings writer (mock — deliberately no native config
// format) reports an empty (un-wired) status with a nil error: a legitimate
// "nothing to report". An UNREGISTERED name errors instead: before
// this, both cases returned the identical zero status + nil error, so a
// typo'd backend name was indistinguishable from a real, wired-nothing read —
// a caller could not tell "you asked about something that doesn't exist" from
// "this backend genuinely has nothing installed".
func BackendStatus(backendName, projectDir string, opts ...SettingsOption) (agent.SettingsStatus, error) {
	if _, ok := engines.Hosted(backendName); !ok {
		return agent.SettingsStatus{}, fmt.Errorf("unknown backend %q", backendName)
	}
	options := &agent.SettingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	writer := settingsWriter(backendName, *options)
	if writer == nil {
		return agent.SettingsStatus{}, nil
	}
	return writer.Status(projectDir)
}
