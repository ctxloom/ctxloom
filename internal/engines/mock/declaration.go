package mock

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// MockSessionFile is the mock's session-rooted approach name for every
// surface: the same well-known file, beneath the run's session home instead
// of the project root. It is each surface's DEFAULT, so a binding that
// selects no root leaves the project tree alone (ruled 2026-09-21); the
// project form stays selectable by name as agent.ApproachFile.
const MockSessionFile = "session-file"

// Declaration is agent.Hosted's: the approach names a binding may select per
// surface kind this double carries — the session form (MockSessionFile, the
// default) and the project form (agent.ApproachFile). A kind the double
// does not carry (the launch double keeps only its context surface; the rest
// arrive per session, inside an engine home) has no name to select. STATIC —
// no roots, no run state — so Names and Default stay pure for --help.
func (m Mock) Declaration() agent.Declaration {
	d := agent.Declaration{}
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSkills, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands} {
		if m.Carries(kind) {
			d[kind] = agent.Presents(MockSessionFile, agent.ApproachFile)
		}
	}
	return d
}
