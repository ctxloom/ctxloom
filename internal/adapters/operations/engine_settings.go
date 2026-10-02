package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// engineSettingsStatus reports the named backend's ctxloom wiring, read
// through its settings reader against the project writer's claims. An
// UNREGISTERED name errors rather than reporting an empty status: a typo'd
// backend must not read as one that genuinely has nothing installed.
func engineSettingsStatus(reg engine.Registry, backendName, projectDir string, opts ...agent.SettingsOption) (agent.SettingsStatus, error) {
	h, ok := agent.HostedIn(reg, backendName)
	if !ok {
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
		options.ProjectClaims = delivery.ProjectClaims(fs, records)
	}
	return h.SettingsReader(*options).Status(projectDir)
}
