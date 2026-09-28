package mock

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// This file pins the protocol mock_surfaces.go sits on: approaches
// constructed from the mock's Declaration whose Present composes against an
// advised present.Start, rather than a raw filepath.Join. These tests cover
// what the external-behaviour suite (mock_surfaces_test.go) structurally
// cannot: each approach's own root choice, and its behaviour under an advised
// (containerized) Start.

// twoDistinguishableRoots builds an advised Start whose ProjectRoot and
// EngineHome are DIFFERENT, non-empty values. Two roots close together is
// exactly what makes handing the presenter the wrong one possible and silent
// (both are plausible absolute paths) — so the two must differ for a test to
// tell them apart, the same discipline presentations_test.go's fakeStart uses.
func twoDistinguishableRoots(projectRoot, engineHome string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: projectRoot},
		SessionHome: present.Root{Host: engineHome},
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

	got := mockPresent(t, agent.SurfaceContext, start)

	want := filepath.Join("/proj", mockContextFilename)
	assert.Equal(t, want, got.HostPath)
	assert.NotContains(t, got.HostPath, "/elsewhere/home",
		"the context presenter must not root on EngineHome")
}

// TestMockSkillsPresenter_RootsUnderProjectRoot_NotEngineHome is the skills
// half of the same pin.
func TestMockSkillsPresenter_RootsUnderProjectRoot_NotEngineHome(t *testing.T) {
	start := twoDistinguishableRoots("/proj", "/elsewhere/home")

	got := mockPresent(t, agent.SurfaceSkills, start)

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
// the actual point of the migration: composed against a Start whose project
// root an environment relocated, EnginePath must diverge from HostPath and
// land at the container-visible root (the mount that makes it true is the
// environment's). Nothing before this migration could even express this
// question — a raw filepath.Join has no Host/Engine distinction to diverge.
func TestMockContextPresenter_ContainerizedRun_EnginePathDivergesFromHostPath(t *testing.T) {
	start := present.New(present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: "/home/user/project", Engine: "/mnt/proj"},
	}))

	got := mockPresent(t, agent.SurfaceContext, start)

	assert.Equal(t, filepath.Join("/home/user/project", mockContextFilename), got.HostPath)
	assert.Equal(t, "/mnt/proj/"+mockContextFilename, got.EnginePath)
	assert.NotEqual(t, got.HostPath, got.EnginePath,
		"a containerized run must present a different engine path than host path")
}

// mockPresent constructs the mock's PROJECT form (agent.ApproachUnsafeFile)
// for kind and presents it against start — what a launch selecting that
// form would do, minus the write.
func mockPresent(t *testing.T, kind agent.SurfaceKind, start present.Start) present.Presentation {
	t.Helper()
	return mockPresentNamed(t, kind, agent.ApproachUnsafeFile, start)
}

func mockPresentNamed(t *testing.T, kind agent.SurfaceKind, name string, start present.Start) present.Presentation {
	t.Helper()
	a, ok := New().(Mock).Declaration().Construct(kind, name, agent.SurfaceInputs{}, nil)
	require.True(t, ok)
	return a.Present(start)
}

// TestMockDefaultForm_RootsUnderSessionHome_NotProjectRoot pins the ruling
// on the launch arm: every surface's DEFAULT form is MockSessionFile, which
// presents beneath the run's session home — not the project root — and
// presents nothing rootable when no session home was advised.
func TestMockDefaultForm_RootsUnderSessionHome_NotProjectRoot(t *testing.T) {
	decl := New().(Mock).Declaration()
	start := twoDistinguishableRoots("/proj", "/sessions/harp/home/mock")
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		def, ok := decl.Default(kind)
		require.True(t, ok)
		assert.Equal(t, MockSessionFile, def, "kind %v", kind)
		got := mockPresentNamed(t, kind, def, start)
		assert.Equal(t, filepath.Join("/sessions/harp/home/mock", filepath.FromSlash(mockRel[kind])), got.HostPath, "kind %v", kind)
	}
	unrooted := mockPresentNamed(t, agent.SurfaceContext, MockSessionFile, present.ProjectOnHost("/proj"))
	assert.False(t, filepath.IsAbs(unrooted.HostPath), "with no session home advised the session form is not rootable: %q", unrooted.HostPath)
}

// TestMockDeclaration_UnsupportedApproach_IsRefused pins the branch Build
// takes when the KIND is declared but the requested APPROACH is not one of
// its names — mock declares the session and project forms only, so asking
// for ApproachHook on the context surface must be refused, not silently
// resolved to something else. The message must distinguish "the approach is unsupported" from "the
// kind is absent": it names the surface, the name and what IS declared.
func TestMockDeclaration_UnsupportedApproach_IsRefused(t *testing.T) {
	decl := New().(Mock).Declaration()

	_, ok := decl.Construct(agent.SurfaceContext, agent.ApproachHook, agent.SurfaceInputs{Context: "X"}, nil)
	assert.False(t, ok, "an undeclared approach is refused")
	assert.NotContains(t, decl.Names(agent.SurfaceContext), agent.ApproachHook)
}

// TestMockDeclaration_UnsupportedKind_IsAbsent pins that a KIND absent from
// the mock's declaration reads as absent — Construct false, Names nil,
// Default absent — rather than fabricating a surface nothing built.
//
// It uses an OUT-OF-RANGE kind because mock declares every real one. That is
// not a contrivance to keep a test alive: the branch is genuinely still
// reachable (a future SurfaceKind added to the seam reaches it until mock
// takes a position), and it is the branch that must report absence rather
// than return a nil Approach a caller would then use.
func TestMockDeclaration_UnsupportedKind_IsAbsent(t *testing.T) {
	decl := New().(Mock).Declaration()

	const notASurface = agent.SurfaceKind(9999)
	a, ok := decl.Construct(notASurface, agent.ApproachUnsafeFile, agent.SurfaceInputs{}, nil)
	assert.False(t, ok)
	assert.Nil(t, a)
	assert.Nil(t, decl.Names(notASurface))
	_, ok = decl.Default(notASurface)
	assert.False(t, ok)
}
