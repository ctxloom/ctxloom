package backends

import (
	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
)

// MockHostings returns the mock engine and its three doubles. mock is the
// COMPLETE engine with no real model behind it — every capability provided,
// deliberately: while mock delivered only some surfaces, fixtures quietly
// came to depend on the gaps, and a gap depended upon is a gap that breaks
// something the day it closes. Each double is mock's descriptor plus ONE
// declared difference; they are separate doubles rather than flags on mock
// because mock proves the surface seam is complete, and a double that is
// sometimes complete cannot prove that.
func MockHostings() []hosting.Hosting {
	// The deliberately-LOSSY double: two unified hook kinds declared
	// unsupported, which is what gives UncarriedSurfaces (and so doctor's
	// capability-loss check and `manage check`'s loss reporting) a subject.
	// TWO kinds, not one: a double modelling a single missing event cannot
	// exercise a report that groups several. Each names its own reason so a
	// report cannot attribute one kind's absence to the other's cause.
	lossy := mockHosting(config.BackendMockLossy, NewMockLossy, func() agent.BackendConfig { return &MockLossyConfig{} })
	lossy.UnsupportedHookKinds = map[string]string{
		"session_start": config.BackendMockLossy + " has no native session_start event",
		"session_end":   config.BackendMockLossy + " has no native session_end event",
	}

	// The LAUNCH-DELIVERED double (see config.BackendMockLaunch). It keeps
	// CommandExports and SkillExports though it writes neither: those
	// populate SurfaceInputs, and LaunchOnlySurfaces reports a surface only
	// when the inputs actually CARRIED something — drop them and nothing is
	// reported missing because the question was never posed. It keeps a
	// settings writer because the writer is the LAUNCH-time mechanism; the
	// missing materialize surface is the point.
	launch := mockHosting(config.BackendMockLaunch, NewMockLaunch, func() agent.BackendConfig { return &MockLaunchConfig{} })
	launch.Surfaces = mockLaunchDeclaration(config.BackendMockLaunch)
	// The clause SAYS WHERE THEY COME FROM, not merely that they were not
	// written: "not carried" alone reads as this engine losing them.
	launch.LaunchOnlySettingsReason = config.BackendMockLaunch +
		" keeps settings, MCP servers, commands and skills in a per-session engine home: they are delivered per-session at launch, which a static materialize has no home to write into"

	// The NO-SKILLS double: THE ABSENCE IS THE ENTIRE POINT. Every other
	// registered backend exports skills, which left the missing-skills arm of
	// every caller with nothing to point at. Its engine Definition has no
	// Skills approach (engines/mock builds it Without skills), so its Exports
	// offer none.
	noSkills := mockHosting(config.BackendMockNoSkills, NewMockNoSkills, func() agent.BackendConfig { return &MockNoSkillsConfig{} })

	return []hosting.Hosting{
		mockHosting(config.BackendMock, NewMock, func() agent.BackendConfig { return &MockConfig{} }),
		lossy,
		launch,
		noSkills,
	}
}

// mockHosting is the shared shape of mock and its doubles.
func mockHosting(name string, ctor func() *Mock, newConfig func() agent.BackendConfig) hosting.Hosting {
	return hosting.Hosting{
		Engine:          engine.Name(name),
		NewBackend:      func(agent.Launcher) agent.Backend { return ctor() },
		NewConfig:       newConfig,
		Surfaces:        mockDeclaration(name),
		SettingsWriter:  engine.Provide(NewMockSettingsWriter),
		HookGlobalScope: engine.Absent[hosting.HookGlobalScope](name + "'s settings surface is a project-relative file with no user-global twin"),
		VersionCommand:  engine.Absent[engineversion.Command](name + " has no binary: there is no single version that would mean anything"),
	}
}
