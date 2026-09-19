package agent

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// This file is the ACCEPTANCE proof for the open set: an engine whose
// surfaces look nothing like claude's declares itself entirely in its own
// scope, with a name no shared file has ever heard of, and the shared seam
// selects, builds, refuses and delivers it — with no enum, ordering list or
// parser to edit, because none exists. That the fake engine below compiles
// and runs against the shared package UNMODIFIED is the whole point.

// steeringEngine is a throwaway engine: its context is a "steering" file
// under a dot-directory, it has NO MCP surface at all (it rides settings),
// and it names its approach after its own mechanism, not after any other
// engine's. It lives only in this test.
const (
	steeringEngine   = "steeringengine"
	steeringApproach = "steering"
	steeringRel      = ".steer/RULES.md"
)

type steeringFile struct {
	content string
	fs      afero.Fs
}

func (s *steeringFile) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(steeringRel).Build()
}

func (s *steeringFile) Deliver(start present.Start) (Delivered, error) {
	p := s.Present(start).HostPath
	if err := s.fs.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := iox.WriteFileAtomicFs(s.fs, p, []byte(s.content), 0o644); err != nil {
		return nil, err
	}
	return DeliveredFunc(func() error { return s.fs.Remove(p) }), nil
}

func (s *steeringFile) UnsafeInfo() string { return steeringEngine + "/context" }

func steeringDeclaration() Declaration {
	return Declaration{
		SurfaceContext: Presents(steeringEngine, SurfaceContext, steeringApproach, func(in SurfaceInputs, fs afero.Fs) Approach {
			return &steeringFile{content: in.Context, fs: GetFS(fs)}
		}),
		SurfaceSettings: Presents(steeringEngine, SurfaceSettings, ApproachUnsafeFile, func(in SurfaceInputs, fs afero.Fs) Approach {
			return recordingDelivery{handle: stubHandle{}, info: steeringEngine + "/settings"}
		}),
	}
}

// An engine with a novel approach name and a surface set unlike claude's is
// enumerable, selectable, buildable and deliverable through the shared seam
// as declared — and nothing in the shared package was edited to admit it.
func TestDeclaration_ANovelEngineNeedsNoSharedEdit(t *testing.T) {
	decl := steeringDeclaration()

	// Enumeration reads the declaration only.
	assert.Equal(t, []string{steeringApproach}, decl.Names(SurfaceContext))
	def, ok := decl.Default(SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, steeringApproach, def)
	assert.Nil(t, decl.Names(SurfaceMCP), "a kind the engine does not declare is absent, not an error")
	assert.ElementsMatch(t, []string{steeringApproach, ApproachUnsafeFile}, decl.AllNames())

	// WithEverything selects every DECLARED kind at its default; MCP is skipped.
	fs := afero.NewMemMapFs()
	r, err := Select(decl).WithEverything().Build(SurfaceInputs{Context: "steer left"}, fs)
	require.NoError(t, err)
	require.Len(t, r.Approaches(), 2)

	// At rest it delivers where it presents.
	delivered, kinds, errs := r.DeliverUnder(present.ProjectOnHost("/proj"))
	require.Empty(t, errs)
	assert.ElementsMatch(t, []SurfaceKind{SurfaceContext, SurfaceSettings}, kinds)
	require.Len(t, delivered, 2)
	got, err := afero.ReadFile(fs, filepath.Join("/proj", steeringRel))
	require.NoError(t, err)
	assert.Equal(t, "steer left", string(got))

	// Selecting the undeclared MCP kind is a permitted no-op; naming an
	// approach the engine does not declare is refused loudly, naming the engine.
	_, err = Select(decl).With(SurfaceMCP, ApproachUnsafeFile).Build(SurfaceInputs{}, fs)
	require.NoError(t, err)
	_, err = Select(decl).With(SurfaceContext, ApproachUnsafeFile).Build(SurfaceInputs{}, fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), steeringEngine)
	assert.Contains(t, err.Error(), steeringApproach)
}

// The SAME registered approach, constructed once, lands in DIFFERENT places
// for a host run and a worktree run: roots bind at Present/Deliver, never at
// construction — the property that keeps a worktree-isolated agent out of the
// coordinator's checkout. Enumerating requires neither construction nor a root.
func TestDeclaration_RootsBindPerLaunchNotAtConstruction(t *testing.T) {
	decl := steeringDeclaration()
	a, ok := decl.Construct(SurfaceContext, steeringApproach, SurfaceInputs{Context: "x"}, afero.NewMemMapFs())
	require.True(t, ok)

	host := a.Present(present.ProjectOnHost("/home/dev/project")).HostPath
	worktree := a.Present(present.ProjectOnHost("/home/dev/worktrees/project--feat")).HostPath
	assert.NotEqual(t, host, worktree)
	assert.Equal(t, filepath.Join("/home/dev/project", steeringRel), host)
	assert.Equal(t, filepath.Join("/home/dev/worktrees/project--feat", steeringRel), worktree)
}

// A shared-cwd delivery of an approach with no out-of-cwd form warns and
// proceeds, naming the surface through the engine's own UnsafeInfo — the
// generic fallback needs nothing engine-specific from shared code.
func TestDeclaration_NovelEngineSharedCwdWarnsThroughItsOwnLabel(t *testing.T) {
	resetStrictness(t)
	fs := afero.NewMemMapFs()
	r, err := Select(steeringDeclaration()).With(SurfaceContext, steeringApproach).Build(SurfaceInputs{Context: "x"}, fs)
	require.NoError(t, err)

	stderr := captureStderr(t, func() {
		_, _, errs := r.DeliverShared(present.ProjectOnHost("/live"))
		require.Empty(t, errs)
	})
	assert.Contains(t, stderr, "warning:")
	assert.Contains(t, stderr, steeringEngine+"/context")
}
