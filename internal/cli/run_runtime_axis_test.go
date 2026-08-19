package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestResolveLaunchSource_RefusesATypodProjectRuntime pins the run's runtime
// entry point. The project `runtime:` default is read here, before any launch
// arm is chosen, and it is the ONLY reading for a launch that never binds a
// named agent. Asserted past the parser it would read as not-a-container —
// the bare host — so a project asking for a container boundary would have run
// outside one with nothing said.
//
// The refusal has to happen before dispatch, which is what the control below
// pins: the axis is assigned whatever the chosen arm then does.
func TestResolveLaunchSource_RefusesATypodProjectRuntime(t *testing.T) {
	runtimeState := func(t *testing.T, runtime string) *runState {
		t.Helper()
		resetStrictness(t)
		return newPermissionRunState(t, config.NewFixture(config.Fixture{
			AppPaths: []string{t.TempDir()},
			Runtime:  runtime,
		}), "codex", "codex")
	}

	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		t.Run("control: "+string(axis)+" is accepted and assigned before dispatch", func(t *testing.T) {
			st := runtimeState(t, string(axis))
			// The launch arm this then dispatches into needs a whole project
			// on disk and is not what is under test; the axis is assigned
			// first, and that assignment is the claim.
			_ = st.resolveLaunchSource()
			assert.Equal(t, axis, st.agentRuntime,
				"a declared container axis really does reach the run's state — without this the refusal below could pass on a dead path")
		})
	}

	t.Run("a typo'd project runtime is refused and nothing is resolved", func(t *testing.T) {
		st := runtimeState(t, "contianer-rootless")

		err := st.resolveLaunchSource()

		require.Error(t, err, "a typo must not ride into st.agentRuntime as an unread string")
		assert.Contains(t, err.Error(), "contianer-rootless")
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful",
			"the refusal names the legal values, not just the bad one")
		assert.Equal(t, agent.RuntimeAxis(""), st.agentRuntime, "THE POINT: no axis was resolved")
		assert.Nil(t, st.req, "and no run request was ever built")
	})

	t.Run("UNSET still resolves to the existing host default", func(t *testing.T) {
		st := runtimeState(t, "")
		_ = st.resolveLaunchSource()
		assert.Equal(t, agent.RuntimeAxis(""), st.agentRuntime,
			"a project that declares no runtime must behave exactly as it did before this key existed")
	})
}

// TestBuildRunRequest_CarriesTheResolvedRuntimeAxis pins what the request
// builder does with the axis: it carries the value the launch source already
// resolved. st.agentRuntime is TYPED, so there is no string here to
// re-interpret and no second door onto a security boundary that has exactly
// one — the parse lives at the entry (TestResolveLaunchSource_... above, and
// resolveAgentBinding for a named agent).
func TestBuildRunRequest_CarriesTheResolvedRuntimeAxis(t *testing.T) {
	build := func(t *testing.T, axis agent.RuntimeAxis) *runState {
		t.Helper()
		resetStrictness(t)
		withRunPermissionsFlag(t, "")
		st := newPermissionRunState(t, config.NewFixture(config.Fixture{
			AppPaths: []string{t.TempDir()},
		}), "codex", "codex")
		st.agentRuntime = axis
		require.NoError(t, st.buildRunRequest())
		return st
	}

	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		t.Run("control: "+string(axis)+" reaches the session axes", func(t *testing.T) {
			st := build(t, axis)
			assert.Equal(t, axis, st.runAxes.Runtime)
			assert.True(t, st.runAxes.WantsContainer(),
				"both ownership modes are containers — a gate that only saw one would answer host for the other")
		})
	}

	t.Run("UNSET reaches the axes as unset and still means the host", func(t *testing.T) {
		st := build(t, "")
		assert.Equal(t, agent.RuntimeAxis(""), st.runAxes.Runtime,
			"unset passes through as unset; it is not rewritten as a literal host")
		assert.False(t, st.runAxes.WantsContainer())
	})
}
