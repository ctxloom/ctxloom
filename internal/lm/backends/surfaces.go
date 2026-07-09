package backends

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the name→SurfaceSet seam: the single place a caller that holds
// only a backend NAME (operations.MaterializeProfile) turns a run's shared inputs
// into that backend's per-provider SurfaceSet, without importing the concrete
// backend. It mirrors GetSettingsWriter (name→settings writer) over the descriptor
// table, so surface delivery is provider-correct by construction — routed through
// each backend's own NewSurfaces — and adding a backend means registering ONE
// descriptor, not touching a cross-backend switch here.

// BuildSurfaces builds the named backend's SurfaceSet from a run's shared inputs
// and a filesystem (nil = OS fs, for a test-injected afero.Fs), reading the
// descriptor's newSurfaces builder (registry.go). A backend that materializes no
// surfaces (acp) or an unregistered name returns an agent.EmptySurfaceSet, so a
// caller can iterate Deliveries() unconditionally.
func BuildSurfaces(name string, inputs agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
	if d, ok := descriptors[name]; ok && d.newSurfaces != nil {
		return d.newSurfaces(inputs, fs)
	}
	return agent.EmptySurfaceSet{}
}

// CommandExportsFor maps loaded bundle content to the named backend's command
// exports (its per-prompt enablement + metadata), for a caller assembling
// agent.SurfaceInputs.Skills. It is the exported seam over commandExportsFor
// (managed.go) — the same mapper the descriptor's skills surface uses — so the
// materialize skills surface can't diverge from a run's.
func CommandExportsFor(name string, prompts []*bundles.LoadedContent) []agent.CommandExport {
	return commandExportsFor(name, prompts)
}

// wellKnownPlacement is a placeholder agent.Placement for building a SurfaceSet
// that only drives the well-known Deliveries() path (materialize, which lands
// native files at the cell's dir). claude's NewSurfaces binds an out-of-cwd
// placement for its RACE-SAFE variants (--append-system-prompt-file /
// --mcp-config / --settings scratch); those are never exercised on the well-known
// path, so the placement is never dereferenced and an empty dir is harmless. The
// other backends ignore the placement entirely (no out-of-cwd flag).
type wellKnownPlacement struct{}

// Dir returns the empty string: the well-known Deliveries() path never reads it.
func (wellKnownPlacement) Dir() string { return "" }
