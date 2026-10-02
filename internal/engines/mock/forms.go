package mock

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// This file is the mock's NAMED FORMS on the agent.Declaration seam: per
// surface kind, the session-rooted form (MockSessionFile, the default) and
// the project form (agent.ApproachUnsafeFile). What survives of the seam is
// its NAMES — a binding's `surfaces:` preference is validated against them
// (Mock.Declaration) — and the settings-writer equity suite; the forms'
// delivery bodies reuse the shared managed-tree writers so the mock proves
// the seam rather than a second implementation of it.

// mockSurfacePath resolves ONE surface's path against the advised roots,
// through the same rel table every approach's Present reads. Every mock path
// goes through here rather than through a per-kind copy: the bodies differed
// only by which SurfaceKind they looked up, and five copies of a resolve chain
// is five places for it to drift.
func mockSurfacePath(kind agent.SurfaceKind, start present.Start) string {
	return start.UnderProjectRoot(mockRel[kind]).Build().HostPath
}

// mockRel is where each mock surface lands, relative to the root it is
// delivered under — the one table Present and the dir-taking path helpers
// both read, so Route() and the delivery agree by construction. Every form
// composes UnderProjectRoot; the session form (sessionRooted) hands it a
// Start whose project root is the run's session home.
var mockRel = map[agent.SurfaceKind]string{
	agent.SurfaceContext:  ContextFileName,
	agent.SurfaceSkills:   skillsRel,
	agent.SurfaceMCP:      mcpRel,
	agent.SurfaceSettings: settingsRel,
	agent.SurfaceCommands: commandsRel,
}

// newMockContext is mock's context approach: the SHARED native-file
// implementation (agent.NativeContextFile) bound to MOCK_CONTEXT.md, differing
// from claude's CLAUDE.md only in the filename.
var newMockContext = agent.NativeContextFile("mock/context", ContextFileName)

// mockSkillsPath returns the mock skills directory's path under dir, via the
// declared skills presenter.
func mockSkillsPath(dir string) string {
	return mockSurfacePath(agent.SurfaceSkills, present.ProjectOnHost(dir))
}

// newMockSkillsSurface builds mock's skills surface: the SHARED
// agent.ManagedSkillPackagesDelivery bound to the SHARED
// agent.WriteManagedSkillPackages writer, exactly as claude's newSkillsSurface
// does. Everything that makes a skill
// package land correctly — the per-skill directory prefix, the DECLARED mode
// on each file, the manifest-scoped reversal — lives in that shared body, not
// here; this function contributes a directory and a manifest name.
func newMockSkillsSurface(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
	return agent.NewManagedSkillPackagesDelivery("mock/skills", skillsRel, in.Skills, func(dir string, skills []agent.SkillExport) error {
		return agent.WriteManagedSkillPackages(agent.GetFS(fs), mockSkillsPath(dir), skills, agent.WithWriteReporter(in.Reporter))
	})
}

func mockSettingsPath(dir string) string {
	return mockSurfacePath(agent.SurfaceSettings, present.ProjectOnHost(dir))
}

// mockMCPSurface is mock's MCP form's presentation: .mock/mcp.json. The
// write is mcpFile.DeliverMCP's (surfaces.go).
type mockMCPSurface struct{}

// Present declares .mock/mcp.json beneath the advised project root. No flag:
// mock has no out-of-cwd redirect.
func (s *mockMCPSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(mockRel[agent.SurfaceMCP]).Build()
}

// UnsafeInfo names mock's MCP surface for the DeliverShared fallback warning.
func (s *mockMCPSurface) UnsafeInfo() string { return "mock/mcp" }

// mockSettingsSurface is mock's settings form's presentation:
// .mock/settings.json. The write is settingsFile.DeliverSettings's
// (surfaces.go).
type mockSettingsSurface struct{}

// Present declares .mock/settings.json beneath the advised project root.
func (s *mockSettingsSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(mockRel[agent.SurfaceSettings]).Build()
}

// UnsafeInfo names mock's settings surface for the DeliverShared fallback.
func (s *mockSettingsSurface) UnsafeInfo() string { return "mock/settings" }

// newMockCommandsSurface builds mock's commands form: the SHARED
// agent.ManagedCommandsDelivery at the mock's commands directory.
func newMockCommandsSurface(agent.SurfaceInputs, afero.Fs) agent.Approach {
	return agent.NewManagedCommandsDelivery("mock/commands", commandsRel)
}

// MockSessionFile is the mock's session-rooted form of every surface: the
// same well-known file, beneath the run's session home instead of the
// project root. It is each surface's DEFAULT, so a binding that selects no root
// leaves the project tree alone (ruled 2026-09-21); the project form stays
// selectable by name as agent.ApproachUnsafeFile.
const MockSessionFile = "session-file"

// sessionRooted rebases a project-rooted form onto the run's session home:
// the inner approach presents and delivers exactly as it would beneath a
// project root, handed a Start whose project root IS this run's session home.
// A run advising no session home rebases onto "" and the form presents a
// bare relative path — which is precisely what keeps selection from choosing
// it (agent.rootedInThisRun reads that).
func sessionRooted(ctor agent.Construct) agent.Construct {
	return func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &sessionForm{inner: ctor(in, fs)}
	}
}

type sessionForm struct{ inner agent.Approach }

// rebase re-roots the composition at the session home, keeping BOTH of its
// sides: the roots are already relocated, so they are advised as they are.
func (s *sessionForm) rebase(start present.Start) present.Start {
	p := start.Paths()
	return present.New(present.Advised(present.Paths{
		ProjectRoot: p.SessionHome,
		SessionHome: p.SessionHome,
	}))
}

func (s *sessionForm) Present(start present.Start) present.Presentation {
	return s.inner.Present(s.rebase(start))
}

// Declaration is agent.Hosted's: the named forms per surface kind this
// double carries — the session-rooted form (MockSessionFile, the default)
// and the project form (agent.ApproachUnsafeFile) of one constructor. It is
// derived from the kind's own surfaces: a kind the double does not carry
// (the launch double keeps only its context surface; the rest arrive per
// session, inside an engine home) has no form to name, so a static
// materialize skips it. STATIC — no roots, no run state — so Names and Default stay pure
// for --help.
func (m Mock) Declaration() agent.Declaration {
	name := string(m.Name)
	both := func(kind agent.SurfaceKind, ctor agent.Construct) agent.Presentations {
		return agent.Presents(name, kind, MockSessionFile, sessionRooted(ctor)).Or(agent.ApproachUnsafeFile, ctor)
	}
	all := agent.Declaration{
		agent.SurfaceContext: both(agent.SurfaceContext, newMockContext),
		agent.SurfaceSkills:  both(agent.SurfaceSkills, newMockSkillsSurface),
		agent.SurfaceMCP: both(agent.SurfaceMCP, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockMCPSurface{}
		}),
		agent.SurfaceSettings: both(agent.SurfaceSettings, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockSettingsSurface{}
		}),
		agent.SurfaceCommands: both(agent.SurfaceCommands, newMockCommandsSurface),
	}
	d := agent.Declaration{}
	for kind, p := range all {
		if m.Carries(kind) {
			d[kind] = p
		}
	}
	return d
}

// Compile-time capability contracts.
var (
	_ agent.Approach = (*mockMCPSurface)(nil)
	_ agent.Approach = (*mockSettingsSurface)(nil)
)
