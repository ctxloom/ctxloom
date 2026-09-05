package operations

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// writeFile is a small helper for the agent fixtures.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0644))
}

// writeAgentProfileFixture lays down two local bundles (each one fragment) and
// three local profiles composing them, so agent resolution can be exercised
// end to end without any remote/network. Real tempdirs (GetBundleDirs/
// GetProfileDirs stat the real fs).
//
//	p1: bundle kit1 (FRAG-ONE), llm: fast
//	p2: bundle kit2 (FRAG-TWO), llm: slow
//	p3: bundle kit1 (FRAG-ONE), no llm
func writeAgentProfileFixture(t *testing.T, root string) {
	t.Helper()
	app := filepath.Join(root, ".ctxloom")
	writeFile(t, filepath.Join(app, "content", "bundles", "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeFile(t, filepath.Join(app, "content", "bundles", "kit2.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f2:\n    content: \"FRAG-TWO\"\n")
	writeFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"llm: fast\nbundles:\n  - ctxloom:local@bundles/kit1\n")
	writeFile(t, filepath.Join(app, "profiles", "p2.yaml"),
		"llm: slow\nbundles:\n  - ctxloom:local@bundles/kit2\n")
	writeFile(t, filepath.Join(app, "profiles", "p3.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
}

// agentTestConfig builds a Config over root with a small mock LLM registry and
// the given config-key agents.
func agentTestConfig(root string, subs map[string]agents.Agent) *config.Config {
	return agentTestConfigWithDefault(root, subs, "")
}

// agentTestConfigWithDefault is agentTestConfig plus an explicit default_agent
// (set via the Fixture directly, since Config's fields are unexported outside
// internal/config and cannot be assigned after construction).
func agentTestConfigWithDefault(root string, subs map[string]agents.Agent, defaultAgent string) *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths: []string{filepath.Join(root, ".ctxloom")},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast":    {Type: "mock", Body: map[string]any{"model": "m-fast"}},
				"slow":    {Type: "mock", Body: map[string]any{"model": "m-slow"}},
				"primary": {Type: "claude-code"},
			},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Agents:       subs,
		DefaultAgent: defaultAgent,
	})
}

// TestResolveAgent_ComposesAndOverridesEngine is the core of the entity:
// multiple profiles compose into ONE context (union of their fragments), and the
// agent's engine OVERRIDES the constituent profiles' llm.
func TestResolveAgent_ComposesAndOverridesEngine(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"dev": {LLM: "slow", Profiles: []string{"p1", "p2"}},
	})

	res, err := ResolveAgent(context.Background(), cfg, "dev", "")
	require.NoError(t, err)

	// Compose: both profiles' fragments reach the one assembled context.
	assert.Contains(t, res.Context, "FRAG-ONE")
	assert.Contains(t, res.Context, "FRAG-TWO")
	assert.Equal(t, []string{"p1", "p2"}, res.Profiles)

	// Engine override: the agent's engine ("slow") beats p1's declared llm
	// ("fast", the first non-empty composed profile llm).
	assert.Equal(t, "slow", res.Label, "engine overrides the composed profiles' llm")
	assert.Equal(t, "mock", res.Backend)
	assert.Equal(t, "m-slow", res.Model)
}

// TestResolveAgent_BareLaunchBindsDefaultAgent is the resolution-layer contract
// behind a bare `ctxloom run` (internal/cli/run.go): the bare-launch else branch
// resolves cfg.defaultAgent through ResolveAgent, inheriting the agent's composed
// profiles + engine + runtime + permissions exactly like --agent. profiles.defaults
// was retired — DefaultAgentProfiles is the same set.
func TestResolveAgent_BareLaunchBindsDefaultAgent(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfigWithDefault(root, map[string]agents.Agent{
		"default": {LLM: "slow", Profiles: []string{"p1", "p2"}, Runtime: "container-rootless", Permissions: "plan"},
	}, "default")

	// The "default profile set" every non-run consumer reads matches the agent.
	assert.Equal(t, []string{"p1", "p2"}, cfg.DefaultAgentProfiles())

	// A bare run resolves cfg.GetDefaultAgent() — profiles compose, engine/runtime/
	// permissions ride along.
	res, err := ResolveAgent(context.Background(), cfg, cfg.GetDefaultAgent(), "")
	require.NoError(t, err)
	assert.Contains(t, res.Context, "FRAG-ONE")
	assert.Contains(t, res.Context, "FRAG-TWO")
	assert.Equal(t, "slow", res.Label)
	assert.Equal(t, agent.RuntimeContainerRootless, res.Runtime, "the default agent's runtime rides the bare launch")
	assert.Equal(t, "plan", res.Permissions, "the default agent's permissions ride the bare launch")
}

// TestResolveAgent_MissingDefaultAgentDegrades pins the fault-tolerant half: a
// bare run resolves an empty or unknown cfg.defaultAgent, and ResolveAgent must
// return an error the run path degrades on (warn + empty context, never a hard
// stop — CLAUDE.md). Unlike --agent, this is not a fatal condition.
func TestResolveAgent_MissingDefaultAgentDegrades(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)

	t.Run("empty default_agent", func(t *testing.T) {
		cfg := agentTestConfig(root, nil) // no DefaultAgent set
		assert.Nil(t, cfg.DefaultAgentProfiles())
		_, err := ResolveAgent(context.Background(), cfg, cfg.GetDefaultAgent(), "")
		require.Error(t, err, "an empty default_agent is the run path's degrade signal")
	})

	t.Run("default_agent names an undefined agent", func(t *testing.T) {
		cfg := agentTestConfigWithDefault(root, nil, "ghost")
		assert.Nil(t, cfg.DefaultAgentProfiles())
		_, err := ResolveAgent(context.Background(), cfg, cfg.GetDefaultAgent(), "")
		require.Error(t, err, "an unresolvable default_agent is the run path's degrade signal")
	})
}

// TestResolveAgent_EffectivePermissions pins the resolved posture surfaced by
// `agent show`: a declared value wins; a blank claude-code agent resolves to the
// host-bypass stopgap (not ""); a blank non-claude agent resolves to default.
func TestResolveAgent_EffectivePermissions(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"claude-blank": {LLM: "primary", Profiles: []string{"p1"}},
		"mock-plan":    {LLM: "fast", Profiles: []string{"p1"}, Permissions: "plan"},
		"mock-blank":   {LLM: "fast", Profiles: []string{"p1"}},
	})
	cases := map[string]string{
		"claude-blank": "bypass",  // claude-code host stopgap made visible
		"mock-plan":    "plan",    // declared value surfaces
		"mock-blank":   "default", // non-claude blank → prompt
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := ResolveAgent(context.Background(), cfg, name, "")
			require.NoError(t, err)
			assert.Equal(t, want, res.EffectivePermissions)
		})
	}
}

// TestResolveAgent_ConfigHome proves the resolve-time treatment of
// ResolvedAgent.ConfigHome: undeclared and unresolvable both warn-and-default
// to agents.ConfigHomeHost (never fatal — a hand-edited config.yaml must not
// block a launch over this), a declared "project" or "host" round-trips
// unchanged, and the field is NEVER empty once an agent resolved at all —
// that emptiness is reserved for "no agent binding was resolved", a state
// this function (which always resolves SOME binding) can never produce.
func TestResolveAgent_ConfigHome(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"undeclared": {LLM: "fast", Profiles: []string{"p1"}},
		"project":    {LLM: "fast", Profiles: []string{"p1"}, ConfigHome: "project"},
		"host":       {LLM: "fast", Profiles: []string{"p1"}, ConfigHome: "host"},
		"typo":       {LLM: "fast", Profiles: []string{"p1"}, ConfigHome: "projectt"},
	})
	cases := map[string]string{
		"undeclared": agents.ConfigHomeHost, // MUTATION TARGET m1's unit-layer twin
		"project":    agents.ConfigHomeProject,
		"host":       agents.ConfigHomeHost,
		"typo":       agents.ConfigHomeHost, // warn+default, never fatal
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := ResolveAgent(context.Background(), cfg, name, "")
			require.NoError(t, err, "an unresolvable config_home must warn, not fail the resolve")
			assert.Equal(t, want, res.ConfigHome)
		})
	}
}

// TestResolveAgent_ExplicitEngineOverrideWins proves a caller-supplied
// engine override (ACP --llm) beats the agent's own declared engine — the
// same precedence a delegated child's engine override uses.
func TestResolveAgent_ExplicitEngineOverrideWins(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"dev": {LLM: "slow", Profiles: []string{"p1"}},
	})

	res, err := ResolveAgent(context.Background(), cfg, "dev", "fast")
	require.NoError(t, err)
	assert.Equal(t, "fast", res.Label, "explicit override beats the declared engine")
	assert.Equal(t, "m-fast", res.Model)
	assert.Equal(t, "slow", res.LLM, "the DECLARED engine is still reported as written")
}

// TestResolveAgent_EngineUnsetFallsBackToProfileLLM proves an empty engine
// falls back to the composed profiles' llm (first non-empty).
func TestResolveAgent_EngineUnsetFallsBackToProfileLLM(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"dev": {Profiles: []string{"p1", "p2"}}, // no engine
	})

	res, err := ResolveAgent(context.Background(), cfg, "dev", "")
	require.NoError(t, err)
	assert.Equal(t, "fast", res.Label, "no engine → the composed profiles' llm (p1's 'fast')")
}

// TestResolveAgent_EngineUnsetNoProfileLLMUsesProjectDefault proves the
// terminal fallback: no engine and no profile llm → the project's primary label
// ("default = the project backend").
func TestResolveAgent_EngineUnsetNoProfileLLMUsesProjectDefault(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"plain": {Profiles: []string{"p3"}}, // p3 declares no llm
	})

	res, err := ResolveAgent(context.Background(), cfg, "plain", "")
	require.NoError(t, err)
	assert.Contains(t, res.Context, "FRAG-ONE")
	assert.Equal(t, "primary", res.Label, "no engine, no profile llm → project primary")
	assert.Equal(t, "claude-code", res.Backend)
}

// TestListAgents_MultipleNamed proves several named agents list from the one
// source, sorted, and that each one still resolves.
func TestListAgents_MultipleNamed(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)
	cfg := agentTestConfig(root, map[string]agents.Agent{
		"dev":    {LLM: "primary", Profiles: []string{"p1"}},
		"finder": {LLM: "fast", Profiles: []string{"p2"}},
	})

	list := ListAgents(cfg)
	require.Len(t, list, 2)
	assert.Equal(t, "dev", list[0].Name)
	assert.Equal(t, "primary", list[0].LLM)
	assert.Equal(t, "finder", list[1].Name)
	assert.Equal(t, "fast", list[1].LLM)

	// Both resolve.
	for _, name := range []string{"dev", "finder"} {
		_, err := ResolveAgent(context.Background(), cfg, name, "")
		require.NoErrorf(t, err, "agent %q must resolve", name)
	}
}

// TestResolveAgent_BundleProfileMember proves an agent may reference a
// PHASE-A bundle profile ("<bundle>#profiles/<name>") as a member, resolving it
// through the same shared profile loader.
func TestResolveAgent_BundleProfileMember(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root) // ships bundle profile kitProfileKey (llm: fast)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{filepath.Join(root, ".ctxloom")},
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"fast": {Type: "mock"}},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: map[string]agents.Agent{
			"reviewer": {Profiles: []string{kitProfileKey}},
		},
	})

	res, err := ResolveAgent(context.Background(), cfg, "reviewer", "")
	require.NoError(t, err)
	assert.Contains(t, res.Context, "FRAG-ONE", "bundle profile's composed fragment reaches context")
	assert.Equal(t, "fast", res.Label, "bundle profile's llm flows through when engine is unset")
}

// TestResolveAgent_Driving proves the driving axis carries through
// resolveAgentBinding onto ResolvedAgent.Driving, and that an unknown value
// FAILS LOUD at resolve — SetAgent already refuses it at the write edge, so
// resolve is where a HAND-EDITED config.yaml typo must still be caught.
func TestResolveAgent_Driving(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)

	t.Run("absent driving resolves to the empty (conversational) zero value", func(t *testing.T) {
		cfg := agentTestConfig(root, map[string]agents.Agent{
			"dev": {LLM: "slow", Profiles: []string{"p1"}},
		})
		res, err := ResolveAgent(context.Background(), cfg, "dev", "")
		require.NoError(t, err)
		assert.Equal(t, agents.DrivingMode(""), res.Driving)
	})

	t.Run("declared oneshot carries through to ResolvedAgent.Driving", func(t *testing.T) {
		cfg := agentTestConfig(root, map[string]agents.Agent{
			"dev": {LLM: "slow", Profiles: []string{"p1"}, Driving: agents.DrivingOneshot},
		})
		res, err := ResolveAgent(context.Background(), cfg, "dev", "")
		require.NoError(t, err)
		assert.Equal(t, agents.DrivingOneshot, res.Driving)
	})

	t.Run("unknown driving value fails loud at resolve, not just at the write edge", func(t *testing.T) {
		cfg := agentTestConfig(root, map[string]agents.Agent{
			"dev": {LLM: "slow", Profiles: []string{"p1"}, Driving: agents.DrivingMode("bogus")},
		})
		_, err := ResolveAgent(context.Background(), cfg, "dev", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bogus")
	})
}

// TestResolveAgent_NotFound is the unknown-name error path.
func TestResolveAgent_NotFound(t *testing.T) {
	root := t.TempDir()
	cfg := agentTestConfig(root, nil)
	_, err := ResolveAgent(context.Background(), cfg, "nope", "")
	assert.Error(t, err)
}

// --- LOCAL-ONLY invariant -------------------------------------------------

// TestAgent_LocalOnly_NeverFromBundle is the structural + behavioral proof
// that agents are LOCAL ONLY: bundles carry no Agents field, and a bundle
// that (illegitimately) declares a `agents:` key contributes nothing.
func TestAgent_LocalOnly_NeverFromBundle(t *testing.T) {
	// Structural: there is no Bundle.Agents — agents can never be a bundle
	// item kind. (If someone adds the field, this fails and forces a redesign.)
	_, hasField := reflect.TypeOf(bundles.Bundle{}).FieldByName("Agents")
	assert.False(t, hasField, "bundles.Bundle must have no Agents field")

	// Behavioral: a bundle YAML carrying a `agents:` key surfaces NO agent —
	// the agent loader reads only the config key, never a bundle.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".ctxloom", "content", "bundles", "evil.yaml"),
		"version: \"1.0.0\"\nagents:\n  smuggled:\n    llm: attacker\n    profiles: [x]\n")
	cfg := agentTestConfig(root, nil) // no config-key agents at all

	assert.Empty(t, ListAgents(cfg), "a bundle cannot define an agent")
	_, ok := cfg.Agent("smuggled")
	assert.False(t, ok, "a bundle-declared agent is never loadable")
}

// --- UNGATED invariant ----------------------------------------------------

// TestAgent_Ungated proves the agent DEFINITION is ungated orchestration/
// config: it resolves with no trust state whatsoever (a fresh, empty store) —
// only its constituent bundle items go through the review model. (The
// enumeration half of the old assertion rode on the retired TR6 baseline; the
// structural "agents are never a bundle item kind" guard lives in
// TestAgent_LocalOnly_NeverFromBundle above.)
func TestAgent_Ungated(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root) // kit: 1 fragment + 1 mcp + 1 hook + 1 profile
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{filepath.Join(root, ".ctxloom")},
		Agents:   map[string]agents.Agent{"reviewer": {Profiles: []string{kitProfileKey}}},
	})

	// Ungated: resolves with no trust setup whatsoever.
	_, err := ResolveAgent(context.Background(), cfg, "reviewer", "")
	require.NoError(t, err)
}
