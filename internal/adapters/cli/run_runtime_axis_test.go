package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestResolveLaunchSource_RefusesATypodProjectRuntime pins the run's runtime
// entry point. The project `runtime:` default is read here, before any launch
// arm is chosen, and it is the ONLY reading for a launch that never binds a
// named agent. Asserted past the parser it would read as not-a-container —
// the bare host — so a project asking for a container boundary would have run
// outside one with nothing said.
//
// The parse happens before dispatch, so it governs every launch arm rather
// than only the ones that bind a named agent — which is what the controls
// pin: a declared axis is accepted here and reaches the run's state, and
// silence is accepted unchanged.
func TestResolveLaunchSource_RefusesATypodProjectRuntime(t *testing.T) {
	runtimeState := func(t *testing.T, runtime string) *runState {
		t.Helper()
		resetStrictness(t)
		return newPermissionRunState(t, config.NewFixture(config.Fixture{
			AppPaths: []string{t.TempDir()},
			Runtime:  runtime,
		}), "mock", "mock")
	}

	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		t.Run("control: "+string(axis)+" is accepted and assigned before dispatch", func(t *testing.T) {
			st := runtimeState(t, string(axis))

			require.NoError(t, st.resolveLaunchSource(),
				"a declared container axis is accepted, and dispatch proceeds past it")
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
		assert.Equal(t, launch.RuntimeAxis(""), st.agentRuntime, "THE POINT: no axis was resolved")
		assert.Nil(t, st.req, "and no run request was ever built")
	})

	t.Run("UNSET still resolves to the existing host default", func(t *testing.T) {
		st := runtimeState(t, "")

		require.NoError(t, st.resolveLaunchSource(),
			"a project that declares no runtime must behave exactly as it did before this key existed — silence is not a typo")
		assert.Equal(t, launch.RuntimeAxis(""), st.agentRuntime, "and unset stays unset")
	})
}

// TestBuildRunRequest_CarriesTheResolvedRuntimeAxis pins what the request
// builder does with the axis: it carries the value the launch source already
// resolved. st.agentRuntime is TYPED, so there is no string here to
// re-interpret and no second door onto a security boundary that has exactly
// one — the parse lives at the entry (TestResolveLaunchSource_... above, and
// resolveAgentBinding for a named agent).
func TestBuildRunRequest_CarriesTheResolvedRuntimeAxis(t *testing.T) {
	build := func(t *testing.T, axis launch.RuntimeAxis) *runState {
		t.Helper()
		resetStrictness(t)
		withRunPermissionsFlag(t, "")
		st := newPermissionRunState(t, config.NewFixture(config.Fixture{
			AppPaths: []string{t.TempDir()},
		}), "mock", "mock")
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
		assert.Equal(t, launch.RuntimeAxis(""), st.runAxes.Runtime,
			"unset passes through as unset; it is not rewritten as a literal host")
		assert.False(t, st.runAxes.WantsContainer())
	})
}

// TestResolveLaunchSource_MockAgentBoundToContainerReachesTheRunAxes is the
// CLI half of the claim "nothing short of a runtime probe refuses mock in a
// container". The --agent arm resolves the binding, the request builder
// carries the axis, and the next thing to consult it is isolation.Prepare —
// whose chain probes the host for a daemon. Everything before that point is
// exercised here for BOTH ownership modes; the probe itself is not a unit
// concern.
//
// The fixture is the containerized-run journey's (a mock label, an agent
// bound to it with a container runtime), so a refusal that only fires on the
// real shape could not hide behind a synthetic one.
func TestResolveLaunchSource_MockAgentBoundToContainerReachesTheRunAxes(t *testing.T) {
	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		t.Run(string(axis), func(t *testing.T) {
			resetStrictness(t)
			withRunPermissionsFlag(t, "")
			withRunAgentFlag(t, "mock-container")
			cfg := config.NewFixture(config.Fixture{
				AppPaths: []string{t.TempDir()},
				LM: config.LMConfig{
					Configs:  map[string]config.LLMConfig{"fast": {Type: "mock"}},
					Defaults: config.RoleDefaults{Primary: "fast"},
				},
				Agents: map[string]agents.Agent{
					"mock-container": {LLM: "fast", Runtime: string(axis)},
				},
			})
			st := newPermissionRunState(t, cfg, "", "")
			st.ctx = context.Background()

			require.NoError(t, st.resolveLaunchSource(), "the --agent arm accepts a mock binding with a container runtime")
			assert.Equal(t, "mock", st.backendName, "the binding's label resolved to the double")
			assert.Equal(t, axis, st.agentRuntime)

			require.NoError(t, st.buildRunRequest(), "the request builder accepts the resolved pair")
			assert.Equal(t, axis, st.runAxes.Runtime)
			assert.True(t, st.runAxes.WantsContainer(),
				"the container demand reaches the axes isolation.Prepare will read — the first thing past here is the runtime probe")
			assert.Empty(t, strictness.All(), "no finding of any class was raised on the way")
		})
	}
}

// withRunAgentFlag sets the package-level --agent flag var for one test and
// restores it after; resolveLaunchSource dispatches on it directly.
func withRunAgentFlag(t *testing.T, v string) {
	t.Helper()
	orig := runAgent
	runAgent = v
	t.Cleanup(func() { runAgent = orig })
}
