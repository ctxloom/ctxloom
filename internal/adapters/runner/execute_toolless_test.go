package runner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// unmarked is the mock kind with engine.ToolLess hidden: only the port.
type unmarked struct{ engine.Engine }

// markedSpy is a kind implementing engine.ToolLess that records the
// sessions it bound through the marker.
type markedSpy struct {
	engine.Engine
	bound *[]engine.Session
}

func (m markedSpy) ToolLess(s engine.Session) (engine.Instance, error) {
	*m.bound = append(*m.bound, s)
	return m.Instance(s)
}

// A tool-less launch (marauding-magnolia) is bound through the engine's own
// tool-less launch, and a kind that does not implement engine.ToolLess
// refuses it before anything is delivered: the runner is the second place
// the marker is required, so a launch resolved elsewhere cannot slip a
// tool-less run onto an engine that would run it with tools.
func TestExecute_BindsAToolLessLaunchThroughTheMarker(t *testing.T) {
	env := newDeliveryEnv(t)
	resolve := func(runID string) launch.Launch {
		l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
			Identity: env.mint(t, 1, runID),
			Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
		})
		require.NoError(t, err)
		l.ToolLess = true
		return l
	}

	refused := resolve("run-tl-1")
	drive := &recordingDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  unmarked{mock.New()}, Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, refused)
	require.ErrorIs(t, err, launch.ErrToolLessUnsupported)
	require.Empty(t, drive.turns)
	require.Empty(t, cellTree(t, refused), "nothing was delivered")

	bound := []engine.Session{}
	l := resolve("run-tl-2")
	out, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  markedSpy{Engine: mock.New(), bound: &bound}, Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.NoError(t, err)
	if out.Close != nil {
		t.Cleanup(out.Close)
	}
	require.Len(t, bound, 1, "the session was bound through the marker")
	require.Equal(t, l.Identity.Harp, bound[0].Identity.Harp)
	require.Len(t, drive.turns, 1)
}
