package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
)

// The project-default posture (config.yaml's top-level `permissions:`) is a rung
// of EVERY chain that resolves a launch posture from config, not just the
// interactive run's. Two live here:
//
//   - RunOneshot's, which today reads req.Permissions → the engine label's; a
//     project that declared a default must be consulted after the label and
//     before the member-posture gate refuses an undeclared one.
//   - ResolveAgent's EffectivePermissions, the posture `agent show` prints as
//     "what an unflagged run will actually do". If that display skipped the
//     project default it would print a posture the run does not use, which is
//     worse than printing nothing.
//
// In both, the project default is the LAST declaration consulted: anything
// narrower that a binding or a label actually declared still wins.

// projectPermConfig builds a config whose only permission declaration is the
// PROJECT default, plus the labels/agents a case needs.
func projectPermConfig(root, projectPerm string, subs map[string]agents.Agent, labels map[string]config.LLMConfig) *config.Config {
	if labels == nil {
		labels = map[string]config.LLMConfig{}
	}
	labels["fast"] = config.LLMConfig{Type: "mock", Body: map[string]any{"model": "m-fast"}}
	if _, ok := labels["primary"]; !ok {
		labels["primary"] = config.LLMConfig{Type: "claude-code"}
	}
	return config.NewFixture(config.Fixture{
		AppPaths:    []string{filepath.Join(root, ".ctxloom")},
		LM:          config.LMConfig{Configs: labels, Defaults: config.RoleDefaults{Primary: "primary"}},
		Agents:      subs,
		Permissions: projectPerm,
	})
}

// TestResolveAgent_EffectivePermissions_ProjectDefault pins the project default
// into the posture `agent show` reports — and pins that a narrower declaration
// still beats it.
func TestResolveAgent_EffectivePermissions_ProjectDefault(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)

	t.Run("an undeclared agent on a non-claude engine takes the project default", func(t *testing.T) {
		cfg := projectPermConfig(root, "plan", map[string]agents.Agent{
			"blank": {LLM: "fast", Profiles: []string{"p1"}},
		}, nil)

		res, err := ResolveAgent(context.Background(), cfg, "blank", "")
		require.NoError(t, err)
		assert.Equal(t, "plan", res.EffectivePermissions,
			"an agent declaring no posture, on an engine whose label declares none either, must take the project default")
	})

	t.Run("the agent's own declaration beats the project default", func(t *testing.T) {
		cfg := projectPermConfig(root, "bypass", map[string]agents.Agent{
			"careful": {LLM: "fast", Profiles: []string{"p1"}, Permissions: "plan"},
		}, nil)

		res, err := ResolveAgent(context.Background(), cfg, "careful", "")
		require.NoError(t, err)
		assert.Equal(t, "plan", res.EffectivePermissions,
			"a binding that declared plan must not be widened to the project's bypass")
	})

	t.Run("the engine label beats the project default", func(t *testing.T) {
		cfg := projectPermConfig(root, "bypass", map[string]agents.Agent{
			"blank": {LLM: "careful", Profiles: []string{"p1"}},
		}, map[string]config.LLMConfig{
			"careful": {Type: "mock", Permissions: "plan"},
		})

		res, err := ResolveAgent(context.Background(), cfg, "blank", "")
		require.NoError(t, err)
		assert.Equal(t, "plan", res.EffectivePermissions,
			"the engine label's declared plan is nearer than the project default")
	})

	t.Run("a declared project default beats the claude-code host stopgap", func(t *testing.T) {
		cfg := projectPermConfig(root, "plan", map[string]agents.Agent{
			"blank": {LLM: "primary", Profiles: []string{"p1"}},
		}, nil)

		res, err := ResolveAgent(context.Background(), cfg, "blank", "")
		require.NoError(t, err)
		assert.Equal(t, "plan", res.EffectivePermissions,
			"the host stopgap stands in for a posture NOBODY stated; a project that stated one has answered it")
	})

	t.Run("an undeclared project default leaves the stopgap standing", func(t *testing.T) {
		cfg := projectPermConfig(root, "", map[string]agents.Agent{
			"blank": {LLM: "primary", Profiles: []string{"p1"}},
		}, nil)

		res, err := ResolveAgent(context.Background(), cfg, "blank", "")
		require.NoError(t, err)
		assert.Equal(t, "bypass", res.EffectivePermissions,
			"a project that declared nothing must behave exactly as it did before this key existed")
	})
}

// TestRunOneshotPermissions_ProjectDefault pins the bare-profile oneshot's own
// chain. resolveOneshotPermissions is the extracted rung order the caller uses;
// asserting it directly keeps the case from needing a live engine while still
// covering the ordering that matters.
func TestRunOneshotPermissions_ProjectDefault(t *testing.T) {
	cases := []struct {
		name      string
		req       string
		labelPerm string
		project   string
		want      string
	}{
		{"project default fills an undeclared oneshot", "", "", "bypass", "bypass"},
		{"the label beats the project default", "", "plan", "bypass", "plan"},
		{"an explicit request beats both", "plan", "bypass", "bypass", "plan"},
		{"nothing declared anywhere stays empty", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveOneshotPermissions(tc.req, tc.labelPerm, tc.project))
		})
	}
}
