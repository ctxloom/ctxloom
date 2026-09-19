package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Every way a launch is asked for — an agent binding, a profile set, a label
// override, init's discovery probe, an internal one-shot — is a launch.Source
// through ONE resolver. Tonight that resolver delegates to the phases `run`
// already runs, so the table below asserts PARITY: for each source, the
// engine, label, permission and axes resolveViaPhases yields EQUAL what
// today's run state yields for the same inputs. "Today" is computed by
// driving the run state through its own flag globals, never by hand-writing
// the expected values.

// runFlags is one invocation of `ctxloom run` as its flag globals see it.
type runFlags struct {
	agent, llm, profile, workspace, permissions string
	oneShot                                     bool
}

// withRunFlags installs f into the run command's flag globals for the test and
// restores the previous values on cleanup, so the table's cases cannot leak
// into each other or into other tests of this package.
func withRunFlags(t *testing.T, f runFlags) {
	t.Helper()
	prev := runFlags{agent: runAgent, llm: runLLM, profile: runProfile, workspace: runWorkspace, permissions: runPermissions, oneShot: runOneShot}
	prevFragments, prevTags := runFragments, runTags
	runAgent, runLLM, runProfile, runWorkspace, runPermissions, runOneShot = f.agent, f.llm, f.profile, f.workspace, f.permissions, f.oneShot
	runFragments, runTags = nil, nil
	t.Cleanup(func() {
		runAgent, runLLM, runProfile, runWorkspace, runPermissions, runOneShot = prev.agent, prev.llm, prev.profile, prev.workspace, prev.permissions, prev.oneShot
		runFragments, runTags = prevFragments, prevTags
	})
}

// todayResolution is the existing resolution, called: the launch-source arms,
// then the workspace, mode and posture steps, in the order runRun runs them.
func todayResolution(t *testing.T, cfg *config.Config, f runFlags) resolvedLaunch {
	t.Helper()
	withRunFlags(t, f)
	st := &runState{ctx: context.Background(), cfg: cfg}
	require.NoError(t, st.resolveLaunchSource())
	st.resolveSessionWorkspace(runWorkspace)
	st.resolveMode(runOneShot)
	require.NoError(t, st.resolvePostureAndAxes(runPermissions))
	return resolvedLaunch{
		Engine:     st.backendName,
		Label:      st.label,
		Model:      st.labelModel,
		Permission: st.permMode,
		Axes:       st.runAxes,
	}
}

// fiveSourceProject is a project with the bindings test C's table names: a
// `dev` agent on the primary label declaring plan, a `setup` agent for the
// discovery probe, a `distiller` agent on the fast label for the internal
// one-shot, and a `base` profile for the bare profile set.
func fiveSourceProject(t *testing.T) *config.Config {
	t.Helper()
	testsupport.Isolate(t)
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(app, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(app, "profiles", "base.yaml"), []byte("description: the base profile\n"), 0o644))
	return config.NewFixture(config.Fixture{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"primary": {Type: config.BackendMock},
				"fast":    {Type: config.BackendMock, Body: map[string]any{"model": "m-fast"}},
			},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Agents: map[string]agents.Agent{
			"dev":       {LLM: "primary", Profiles: []string{"base"}, Permissions: "plan", Runtime: string(launch.RuntimeHost)},
			"setup":     {Profiles: []string{"base"}},
			"distiller": {LLM: "fast", Profiles: []string{"base"}},
		},
	})
}

func TestResolveViaPhases_FiveSources_ParityWithToday(t *testing.T) {
	cfg := fiveSourceProject(t)
	id := sessions.Identity{Harp: "quiet-amber-falcon"}

	cases := []struct {
		name  string
		src   launch.Source
		today runFlags
	}{
		{"agent binding",
			launch.Source{Identity: id, Agent: "dev", Mode: engine.Interactive, Prompt: "x"},
			runFlags{agent: "dev"}},
		{"profile set",
			launch.Source{Identity: id, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x"},
			runFlags{profile: "base"}},
		{"label override",
			launch.Source{Identity: id, Agent: "dev", Label: "fast", Mode: engine.Interactive, Prompt: "x"},
			runFlags{agent: "dev", llm: "fast"}},
		{"init probe",
			launch.Source{Identity: id, Agent: "setup", Mode: engine.Structured, Prompt: "ping"},
			runFlags{agent: "setup", oneShot: true}},
		{"internal one-shot",
			launch.Source{Identity: id, Agent: "distiller", Mode: engine.Structured, Prompt: "payload"},
			runFlags{agent: "distiller", oneShot: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := todayResolution(t, cfg, tc.today)
			got, err := resolveViaPhases(context.Background(), cfg, tc.src)
			require.NoError(t, err)
			assert.Equal(t, want.Engine, got.Engine, "engine")
			assert.Equal(t, want.Label, got.Label, "label")
			assert.Equal(t, want.Model, got.Model, "model")
			assert.Equal(t, want.Permission, got.Permission, "permission")
			assert.Equal(t, want.Axes, got.Axes, "axes")
			t.Logf("%s: engine=%s label=%s permission=%s axes=%+v", tc.name, got.Engine, got.Label, got.Permission, got.Axes)
		})
	}
}
