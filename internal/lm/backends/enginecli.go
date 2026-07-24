package backends

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// This file is the name→EngineCLI seam: the single place a caller that holds
// only a backend NAME turns it into that backend's engine-CLI declarations
// (agent.EngineCLI), WITHOUT importing the concrete backend package.
//
// It exists for the standalone mock engine binary (cmd/mockengine): the mock
// must impersonate a real vendor CLI by reading the SAME declaration the real
// driver reads (which flags exist, where the prompt travels, which files the
// engine probes), so a fake carrying its own copy cannot drift out of step with
// the driver. Routing that through Get + a type assertion here keeps the mock
// depending on the backend registry, not on internal/claude directly — the
// mirror of BuildSurfaces (name→SurfaceSet) and GetSettingsWriter
// (name→settings writer).

// EngineCLIsFor returns the named backend's native CLI-surface declarations.
// The bool is false when the name is unregistered OR when the backend declares
// no CLI surfaces (it is not an agent.EngineCLIProvider) — an ACP-only backend,
// say. "Has no declaration" is reported rather than fabricated, exactly as the
// EngineCLIProvider doc intends, so a caller asking for a personality the mock
// cannot impersonate gets a loud miss instead of an empty run.
func EngineCLIsFor(name string) ([]agent.EngineCLI, bool) {
	b := Get(name)
	if b == nil {
		return nil, false
	}
	p, ok := b.(agent.EngineCLIProvider)
	if !ok {
		return nil, false
	}
	clis := p.EngineCLIs()
	if len(clis) == 0 {
		return nil, false
	}
	return clis, true
}
