package backends

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// SettingsOption and WithSettingsFS are re-exported for the callers that
// reach the settings-writer dispatch (BackendStatus).
type SettingsOption = agent.SettingsOption

var WithSettingsFS = agent.WithSettingsFS

// settingsWriter constructs the named engine's settings writer, nil for an
// unregistered name.
func settingsWriter(name string, o agent.SettingsOptions) agent.SettingsWriter {
	h, ok := engines.Hosted(name)
	if !ok {
		return nil
	}
	return h.SettingsWriter(o)
}

// BackendsWithSettings returns the names of the engines that carry a
// settings writer, sorted: every Hosted engine.
func BackendsWithSettings() []string {
	var names []string
	for _, n := range engines.Registry().Names(nil) {
		if _, ok := engines.Hosted(string(n)); ok {
			names = append(names, string(n))
		}
	}
	return names
}
