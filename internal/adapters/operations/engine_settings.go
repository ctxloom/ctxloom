package operations

import (
	"fmt"
	"slices"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// engineSettingsWriter constructs the named engine's settings writer
// (agent.Hosted), nil for an unregistered name.
func engineSettingsWriter(reg engine.Registry, name string, o agent.SettingsOptions) agent.SettingsWriter {
	h, ok := agent.HostedIn(reg, name)
	if !ok {
		return nil
	}
	return h.SettingsWriter(o)
}

// engineSettingsStatus reports the named backend's ctxloom wiring. A registered
// backend with no settings writer (mock — deliberately no native config
// format) reports an empty (un-wired) status with a nil error: a legitimate
// "nothing to report". An UNREGISTERED name errors instead: before
// this, both cases returned the identical zero status + nil error, so a
// typo'd backend name was indistinguishable from a real, wired-nothing read —
// a caller could not tell "you asked about something that doesn't exist" from
// "this backend genuinely has nothing installed".
func engineSettingsStatus(reg engine.Registry, backendName, projectDir string, opts ...agent.SettingsOption) (agent.SettingsStatus, error) {
	if _, ok := agent.HostedIn(reg, backendName); !ok {
		return agent.SettingsStatus{}, fmt.Errorf("unknown backend %q", backendName)
	}
	options := &agent.SettingsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	if options.ProjectClaims == nil {
		fs := agent.GetFS(options.FS)
		records, err := OwnershipRecordsOn(fs)
		if err != nil {
			return agent.SettingsStatus{}, err
		}
		options.ProjectClaims = projectClaims(fs, records)
	}
	writer := engineSettingsWriter(reg, backendName, *options)
	if writer == nil {
		return agent.SettingsStatus{}, nil
	}
	return writer.Status(projectDir)
}

// projectClaims is the record's account of what the project writer has
// installed in a file: the places it claims that the file holds now.
func projectClaims(fs afero.Fs, records delivery.Ownership) func(string) ([]string, error) {
	return func(target string) ([]string, error) {
		places, err := records.Paths(fs, target)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range places {
			if p.Live && slices.Contains(p.Writers, delivery.ProjectWriter) {
				out = append(out, p.Pointer)
			}
		}
		return out, nil
	}
}
