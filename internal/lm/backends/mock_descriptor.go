package backends

import (
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	mockreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/mock"
)

// MockDescriptors returns the mock engine and its three doubles. mock is the
// COMPLETE engine with no real model behind it — every capability provided,
// deliberately: while mock delivered only some surfaces, fixtures quietly
// came to depend on the gaps, and a gap depended upon is a gap that breaks
// something the day it closes. Each double is mock's descriptor plus ONE
// declared difference; they are separate doubles rather than flags on mock
// because mock proves the surface seam is complete, and a double that is
// sometimes complete cannot prove that.
func MockDescriptors() []engine.Descriptor {
	// The deliberately-LOSSY double: two unified hook kinds declared
	// unsupported, which is what gives UncarriedSurfaces (and so doctor's
	// capability-loss check and `manage check`'s loss reporting) a subject.
	// TWO kinds, not one: a double modelling a single missing event cannot
	// exercise a report that groups several. Each names its own reason so a
	// report cannot attribute one kind's absence to the other's cause.
	lossy := mockDescriptor(config.BackendMockLossy, NewMockLossy, func() agent.BackendConfig { return &MockLossyConfig{} })
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
	launch := mockDescriptor(config.BackendMockLaunch, NewMockLaunch, func() agent.BackendConfig { return &MockLaunchConfig{} })
	launch.Surfaces = mockLaunchDeclaration(config.BackendMockLaunch)
	// The clause SAYS WHERE THEY COME FROM, not merely that they were not
	// written: "not carried" alone reads as this engine losing them.
	launch.LaunchOnlySettingsReason = config.BackendMockLaunch +
		" keeps settings, MCP servers, commands and skills in a per-session engine home: they are delivered per-session at launch, which a static materialize has no home to write into"

	// The NO-SKILLS double: THE ABSENCE IS THE ENTIRE POINT. Every other
	// registered backend exports skills, which left the missing-skills arm of
	// every caller with nothing to point at. It is a declared absence, so it
	// cannot be "completed" by accident without rewriting this line.
	noSkills := mockDescriptor(config.BackendMockNoSkills, NewMockNoSkills, func() agent.BackendConfig { return &MockNoSkillsConfig{} })
	noSkills.SkillExports = agent.Absent[func([]*bundles.LoadedSkill) []agent.SkillExport](
		config.BackendMockNoSkills + " declares no skill export: it is the subject of every missing-skills-surface arm")

	return []engine.Descriptor{
		mockDescriptor(config.BackendMock, NewMock, func() agent.BackendConfig { return &MockConfig{} }),
		lossy,
		launch,
		noSkills,
	}
}

// mockDescriptor is the shared shape of mock and its doubles.
func mockDescriptor(name string, ctor func() *Mock, newConfig func() agent.BackendConfig) engine.Descriptor {
	return engine.Descriptor{
		Name:           name,
		Distribution:   agent.DistributionTestOnly,
		NewBackend:     func(agent.Launcher) agent.Backend { return ctor() },
		NewConfig:      newConfig,
		Surfaces:       mockDeclaration(name),
		SettingsWriter: agent.Provide(NewMockSettingsWriter),
		InstanceConfig: agent.Absent[func(agent.SettingsOptions) agent.InstanceConfigWriter](
			name + " generates no instance config: it has no config file of its own"),
		CredentialProjector: agent.Absent[func() agent.CredentialProjector](
			name + " has no credentials to project: it authenticates against nothing"),
		// Every prompt and skill is ENABLED: mock has no per-engine export
		// block in a bundle's LLM section, and a mock that silently exported
		// nothing would be a surface that reports success and writes zero
		// bytes — precisely the silent no-op the mock engine exists to catch.
		CommandExports:  agent.Provide(mockExports),
		SkillExports:    agent.Provide(mockSkillExports),
		HookGlobalScope: agent.Absent[engine.HookGlobalScope](name + "'s settings surface is a project-relative file with no user-global twin"),
		VersionCommand:  agent.Absent[engineversion.Command](name + " has no binary: there is no single version that would mean anything"),
		// mock keeps NO engine-global config or credential state: a bare echo
		// compiled into ctxloom that never spawns a grandchild and never
		// touches disk. This is a NAMED, verified exemption from home
		// isolation — a real engine that keeps state declares a Home.
		Home: agent.Absent[agent.EngineHome](name + " keeps no engine-global config or credential state: a bare echo that never touches disk"),
		// Nothing to provision, for the same reason mock declares no Home and
		// no credential projector: it authenticates against nothing, so there
		// is no material whose delivery mechanism could matter. Declared
		// ABSENT rather than left blank so a test engine cannot be the
		// undeclared hole in the middle of the gate that closes them.
		Provisioning: agent.Absent[agent.ProvisioningPolicy](
			name + " has no credential material to provision: it authenticates against nothing and keeps no engine-global state"),
		Container: agent.Provide(agent.EngineContainer{
			// mock installs NO vendor CLI: its engine is the ctxloom binary
			// itself, which composeAgentContainerfile copies in after every
			// engine fragment regardless. The fragment's only job is to be
			// non-nil (so the spec is composable) and to assert the one
			// mock-specific need — `cat`, for the shared-filesystem probe —
			// as a build-time gate rather than an assumption. NOT a template
			// for a real engine, whose fragment must install and validate a
			// real client.
			Install:         mockInstallFragment,
			ValidateCommand: "cat --version",
			// mock authenticates against no vendor: there is no API key,
			// token or credential file it could need, so resolution always
			// succeeds with nothing. A POSITIVE fact about this one engine,
			// verified by reading its implementation — not a template.
			Auth:        agent.Provide(agent.ContainerAuth{Vendorless: name + " authenticates against no vendor"}),
			OverlayDirs: []string{MockConfigDirName},
			// mock keeps no transcripts (NewMock wires NilSessionHistory), so
			// there is no native store root to bind-mount.
			TranscriptStoreRel: "",
		}),
		// mock has no vendor-native transcript store; it carries a DEGENERATE
		// reader anyway because a single-entry reader registry cannot fail —
		// version dispatch and lookup have no branch to take wrongly with one
		// engine. See internal/transcript/vendorreader/mock.
		TranscriptReaders: agent.Provide(mockreader.VersionedAdapters),
	}
}

// mockInstallFragment asserts `cat` (sharedfs.go's probeOneRoot runs `cat
// /probe/marker` in the image). See mockDescriptor's Container doc.
var mockInstallFragment = []byte(`RUN command -v cat >/dev/null 2>&1 \
    || { echo "ctxloom: this base has no cat (needed by the shared-fs probe, sharedfs.go's probeOneRoot)" >&2; exit 1; }
`)
