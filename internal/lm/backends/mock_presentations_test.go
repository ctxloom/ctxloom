package backends

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
)

// This file pins the NEW protocol mock_surfaces.go migrated onto: presenters
// composed against an advised present.Start, declared via agent.Presents,
// rather than a raw filepath.Join keyed off an ApproachTable literal. The
// pre-migration test suite (mock_surfaces_test.go) never named mockApproaches,
// ApproachTable or TableDispatch directly — it exercised MockSurfaces only
// through the SurfaceSet interface, so it needed no rewrite and stays the
// external-behaviour regression net. These tests cover what that suite
// structurally could not: the presenter's own root choice, and its behaviour
// under an advised (containerized) Start, which nothing before this file could
// even construct.

// twoDistinguishableRoots builds an advised Start whose ProjectRoot and
// EngineHome are DIFFERENT, non-empty values. Two roots close together is
// exactly what makes handing the presenter the wrong one possible and silent
// (both are plausible absolute paths) — so the two must differ for a test to
// tell them apart, the same discipline presentations_test.go's fakeStart uses.
func twoDistinguishableRoots(projectRoot, engineHome string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: projectRoot},
		EngineHome:  present.Root{Host: engineHome},
	}))
}

// TestMockContextPresenter_RootsUnderProjectRoot_NotEngineHome pins that
// mockContextPresenter composes UnderProjectRoot. A presenter that read
// EngineHome instead (or read nothing and produced a bare relative name) would
// still "work" against hostStart, whose EngineHome is always the zero Root —
// so this test supplies a NON-zero, DIFFERENT EngineHome specifically to catch
// that mutation, which a same-value or zero-value EngineHome could not.
func TestMockContextPresenter_RootsUnderProjectRoot_NotEngineHome(t *testing.T) {
	start := twoDistinguishableRoots("/proj", "/elsewhere/home")

	got := mockContextPresenter(start)

	want := filepath.Join("/proj", mockContextFilename)
	assert.Equal(t, want, got.HostPath)
	assert.NotContains(t, got.HostPath, "/elsewhere/home",
		"the context presenter must not root on EngineHome")
}

// TestMockSkillsPresenter_RootsUnderProjectRoot_NotEngineHome is the skills
// half of the same pin.
func TestMockSkillsPresenter_RootsUnderProjectRoot_NotEngineHome(t *testing.T) {
	start := twoDistinguishableRoots("/proj", "/elsewhere/home")

	got := mockSkillsPresenter(start)

	want := filepath.Join("/proj", filepath.FromSlash(mockSkillsDirName))
	assert.Equal(t, want, got.HostPath)
	assert.NotContains(t, got.HostPath, "/elsewhere/home",
		"the skills presenter must not root on EngineHome")
}

// TestMockContextPath_JoinsDirWithTheLiteralFilename pins mockContextPath's
// contract against a LITERAL expectation built independently of the present
// chain, not against a second call to mockContextPath itself — deriving the
// expectation from the same function under test would agree with any wrong
// root the presenter chose, which is exactly the vacuous shape a self-referential
// assertion produces.
func TestMockContextPath_JoinsDirWithTheLiteralFilename(t *testing.T) {
	got := mockContextPath("/target")
	assert.Equal(t, filepath.Join("/target", "MOCK_CONTEXT.md"), got)
}

// TestMockSkillsPath_JoinsDirWithTheLiteralSkillsDir is the skills half of the
// same literal pin.
func TestMockSkillsPath_JoinsDirWithTheLiteralSkillsDir(t *testing.T) {
	got := mockSkillsPath("/target")
	assert.Equal(t, filepath.Join("/target", ".mock", "skills"), got)
}

// TestMockContextPresenter_ContainerizedRun_EnginePathDivergesFromHostPath is
// the actual point of the migration: composed against a Start that WAS
// advised by Containerize, EnginePath must diverge from HostPath and land at
// the container-visible root, and the run must record a mount making that
// true. Nothing before this migration could even express this question — a
// raw filepath.Join has no Host/Engine distinction to diverge.
func TestMockContextPresenter_ContainerizedRun_EnginePathDivergesFromHostPath(t *testing.T) {
	mapped := present.Containerize{ProjectRoot: "/mnt/proj"}.Apply(present.Paths{
		ProjectRoot: present.Root{Host: "/home/user/project"},
	})
	start := present.New(mapped)

	got := mockContextPresenter(start)

	assert.Equal(t, filepath.Join("/home/user/project", mockContextFilename), got.HostPath)
	assert.Equal(t, "/mnt/proj/"+mockContextFilename, got.EnginePath)
	assert.NotEqual(t, got.HostPath, got.EnginePath,
		"a containerized run must present a different engine path than host path")

	require.Len(t, mapped.Mounts(), 1)
	assert.Equal(t, present.Mount{HostDir: "/home/user/project", TargetDir: "/mnt/proj"}, mapped.Mounts()[0])
}

// TestMockPresentations_SupportedAndDefault_AgreeWithTheDeclaration proves
// SupportedApproaches/DefaultApproach are DERIVED from mockPresentations
// rather than a second, independently maintained fact — the exact disagreement
// ApproachTable's doc says a capability-list-plus-construction-map shape used
// to allow. A hand-edited SupportedApproaches that fell out of step with
// mockPresentations would pass a test asserting only "non-empty"; this
// compares against the declaration's own Names()/Default().
func TestMockPresentations_SupportedAndDefault_AgreeWithTheDeclaration(t *testing.T) {
	set := NewMockSurfaces(agent.SurfaceInputs{}, nil)

	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSkills} {
		decl := mockPresentations[kind]

		gotSupported := set.SupportedApproaches(kind)
		require.Len(t, gotSupported, len(decl.Names()))
		for _, name := range decl.Names() {
			wantApproach, err := agent.ParseApproach(name)
			require.NoError(t, err)
			assert.Contains(t, gotSupported, wantApproach)
		}

		gotDefault, ok := set.DefaultApproach(kind)
		require.True(t, ok)
		wantDefault, err := agent.ParseApproach(decl.Default())
		require.NoError(t, err)
		assert.Equal(t, wantDefault, gotDefault)
	}
}

// TestMockSurfaces_SurfaceFor_UnsupportedApproach_Errors pins the branch
// SurfaceFor takes when the KIND is declared but the requested APPROACH is
// not one of its names — mock declares only unsafe-file, so asking for
// ApproachHook on the context surface must be refused, not silently resolved
// to something else.
func TestMockSurfaces_SurfaceFor_UnsupportedApproach_Errors(t *testing.T) {
	set := NewMockSurfaces(agent.SurfaceInputs{Context: "X"}, nil)

	_, err := set.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	require.Error(t, err)
	// Exact text, not merely non-nil: a kind-absent refusal ("no context
	// surface") would also satisfy an error-is-not-nil check, so the message
	// must distinguish "the kind is unsupported" from "the approach is".
	assert.Equal(t, "mock: no context surface via hook", err.Error())
}

// TestMockSurfaces_SurfaceFor_UnsupportedKind_Errors pins the branch
// SurfaceFor takes when the KIND itself is absent from mockPresentations
// (MCP, settings, commands) — mock must refuse rather than fabricate a
// surface nothing built.
func TestMockSurfaces_SurfaceFor_UnsupportedKind_Errors(t *testing.T) {
	set := NewMockSurfaces(agent.SurfaceInputs{}, nil)

	_, err := set.SurfaceFor(agent.SurfaceMCP, agent.ApproachUnsafeFile)
	require.Error(t, err)
	// Exact text: "no mcp surface" (kind absent) must not read as "no mcp
	// surface via unsafe-file" (kind present, approach unsupported) — a
	// SurfaceFor that fell through the kind-absent check into the approach
	// loop would still refuse, but for the wrong stated reason.
	assert.Equal(t, "mock: no mcp surface", err.Error())
}
