package operations

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/subagents"
)

// writeFile is a small helper for the subagent fixtures.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0644))
}

// writeSubagentProfileFixture lays down two local bundles (each one fragment) and
// three local profiles composing them, so subagent resolution can be exercised
// end to end without any remote/network. Real tempdirs (GetBundleDirs/
// GetProfileDirs stat the real fs).
//
//	p1: bundle kit1 (FRAG-ONE), llm: fast
//	p2: bundle kit2 (FRAG-TWO), llm: slow
//	p3: bundle kit1 (FRAG-ONE), no llm
func writeSubagentProfileFixture(t *testing.T, root string) {
	t.Helper()
	app := filepath.Join(root, ".ctxloom")
	writeFile(t, filepath.Join(app, "cache", "bundles", "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeFile(t, filepath.Join(app, "cache", "bundles", "kit2.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f2:\n    content: \"FRAG-TWO\"\n")
	writeFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"llm: fast\nbundles:\n  - ctxloom:local@bundles/kit1\n")
	writeFile(t, filepath.Join(app, "profiles", "p2.yaml"),
		"llm: slow\nbundles:\n  - ctxloom:local@bundles/kit2\n")
	writeFile(t, filepath.Join(app, "profiles", "p3.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
}

// subagentTestConfig builds a Config over root with a small mock LLM registry and
// the given config-key subagents.
func subagentTestConfig(root string, subs map[string]subagents.Subagent) *config.Config {
	return &config.Config{
		AppPaths: []string{filepath.Join(root, ".ctxloom")},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast":    {Type: "mock", Body: map[string]any{"model": "m-fast"}},
				"slow":    {Type: "mock", Body: map[string]any{"model": "m-slow"}},
				"primary": {Type: "claude-code"},
			},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Subagents: subs,
	}
}

// TestResolveSubagent_ComposesAndOverridesEngine is the core of the entity:
// multiple profiles compose into ONE context (union of their fragments), and the
// subagent's engine OVERRIDES the constituent profiles' llm.
func TestResolveSubagent_ComposesAndOverridesEngine(t *testing.T) {
	root := t.TempDir()
	writeSubagentProfileFixture(t, root)
	cfg := subagentTestConfig(root, map[string]subagents.Subagent{
		"dev": {Engine: "slow", Profiles: []string{"p1", "p2"}},
	})

	res, err := ResolveSubagent(context.Background(), cfg, "dev")
	require.NoError(t, err)

	// Compose: both profiles' fragments reach the one assembled context.
	assert.Contains(t, res.Context, "FRAG-ONE")
	assert.Contains(t, res.Context, "FRAG-TWO")
	assert.Equal(t, []string{"p1", "p2"}, res.Profiles)

	// Engine override: the subagent's engine ("slow") beats p1's declared llm
	// ("fast", the first non-empty composed profile llm).
	assert.Equal(t, "slow", res.Label, "engine overrides the composed profiles' llm")
	assert.Equal(t, "mock", res.Backend)
	assert.Equal(t, "m-slow", res.Model)
}

// TestResolveSubagent_EngineUnsetFallsBackToProfileLLM proves an empty engine
// falls back to the composed profiles' llm (first non-empty).
func TestResolveSubagent_EngineUnsetFallsBackToProfileLLM(t *testing.T) {
	root := t.TempDir()
	writeSubagentProfileFixture(t, root)
	cfg := subagentTestConfig(root, map[string]subagents.Subagent{
		"dev": {Profiles: []string{"p1", "p2"}}, // no engine
	})

	res, err := ResolveSubagent(context.Background(), cfg, "dev")
	require.NoError(t, err)
	assert.Equal(t, "fast", res.Label, "no engine → the composed profiles' llm (p1's 'fast')")
}

// TestResolveSubagent_EngineUnsetNoProfileLLMUsesProjectDefault proves the
// terminal fallback: no engine and no profile llm → the project's primary label
// ("default = the project backend").
func TestResolveSubagent_EngineUnsetNoProfileLLMUsesProjectDefault(t *testing.T) {
	root := t.TempDir()
	writeSubagentProfileFixture(t, root)
	cfg := subagentTestConfig(root, map[string]subagents.Subagent{
		"plain": {Profiles: []string{"p3"}}, // p3 declares no llm
	})

	res, err := ResolveSubagent(context.Background(), cfg, "plain")
	require.NoError(t, err)
	assert.Contains(t, res.Context, "FRAG-ONE")
	assert.Equal(t, "primary", res.Label, "no engine, no profile llm → project primary")
	assert.Equal(t, "claude-code", res.Backend)
}

// TestListSubagents_MultipleNamedBothSources proves several named subagents list
// from BOTH local sources (config key + .ctxloom/subagents/*.yaml).
func TestListSubagents_MultipleNamedBothSources(t *testing.T) {
	root := t.TempDir()
	writeSubagentProfileFixture(t, root)
	cfg := subagentTestConfig(root, map[string]subagents.Subagent{
		"dev": {Engine: "primary", Profiles: []string{"p1"}},
	})
	// A second subagent from the directory source.
	writeFile(t, filepath.Join(root, ".ctxloom", "subagents", "finder.yaml"),
		"engine: fast\nprofiles: [p2]\n")

	list := ListSubagents(cfg)
	require.Len(t, list, 2)
	assert.Equal(t, "dev", list[0].Name)
	assert.Equal(t, subagents.SourceConfig, list[0].Source)
	assert.Equal(t, "finder", list[1].Name)
	assert.Equal(t, filepath.Join(root, ".ctxloom", "subagents", "finder.yaml"), list[1].Source)

	// Both resolve.
	for _, name := range []string{"dev", "finder"} {
		_, err := ResolveSubagent(context.Background(), cfg, name)
		require.NoErrorf(t, err, "subagent %q must resolve", name)
	}
}

// TestResolveSubagent_BundleProfileMember proves a subagent may reference a
// PHASE-A bundle profile ("<bundle>#profiles/<name>") as a member, resolving it
// through the same shared profile loader.
func TestResolveSubagent_BundleProfileMember(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root) // ships bundle profile kitProfileKey (llm: fast)
	cfg := &config.Config{
		AppPaths: []string{filepath.Join(root, ".ctxloom")},
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"fast": {Type: "mock"}},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Subagents: map[string]subagents.Subagent{
			"reviewer": {Profiles: []string{kitProfileKey}},
		},
	}

	res, err := ResolveSubagent(context.Background(), cfg, "reviewer")
	require.NoError(t, err)
	assert.Contains(t, res.Context, "FRAG-ONE", "bundle profile's composed fragment reaches context")
	assert.Equal(t, "fast", res.Label, "bundle profile's llm flows through when engine is unset")
}

// TestResolveSubagent_NotFound is the unknown-name error path.
func TestResolveSubagent_NotFound(t *testing.T) {
	root := t.TempDir()
	cfg := subagentTestConfig(root, nil)
	_, err := ResolveSubagent(context.Background(), cfg, "nope")
	assert.Error(t, err)
}

// --- LOCAL-ONLY invariant -------------------------------------------------

// TestSubagent_LocalOnly_NeverFromBundle is the structural + behavioral proof
// that subagents are LOCAL ONLY: bundles carry no Subagents field, and a bundle
// that (illegitimately) declares a `subagents:` key contributes nothing.
func TestSubagent_LocalOnly_NeverFromBundle(t *testing.T) {
	// Structural: there is no Bundle.Subagents — subagents can never be a bundle
	// item kind. (If someone adds the field, this fails and forces a redesign.)
	_, hasField := reflect.TypeOf(bundles.Bundle{}).FieldByName("Subagents")
	assert.False(t, hasField, "bundles.Bundle must have no Subagents field")

	// Behavioral: a bundle YAML carrying a `subagents:` key surfaces NO subagent —
	// the subagent loader reads only the config key + .ctxloom/subagents/, never a
	// bundle.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".ctxloom", "cache", "bundles", "evil.yaml"),
		"version: \"1.0.0\"\nsubagents:\n  smuggled:\n    engine: attacker\n    profiles: [x]\n")
	cfg := subagentTestConfig(root, nil) // no config-key, no subagents dir

	assert.Empty(t, ListSubagents(cfg), "a bundle cannot define a subagent")
	_, ok := cfg.Subagent("smuggled")
	assert.False(t, ok, "a bundle-declared subagent is never loadable")
}

// --- UNGATED invariant ----------------------------------------------------

// TestSubagent_Ungated_NotBaselined proves the subagent DEFINITION is ungated
// orchestration/config: it resolves with no trust store/baseline, and the trust
// baseline never enumerates it (its constituent bundle items still are).
func TestSubagent_Ungated_NotBaselined(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root) // kit: 1 fragment + 1 mcp + 1 hook + 1 profile
	cfg := &config.Config{
		AppPaths:  []string{filepath.Join(root, ".ctxloom")},
		Subagents: map[string]subagents.Subagent{"reviewer": {Profiles: []string{kitProfileKey}}},
	}

	// Ungated: resolves with no trust setup whatsoever.
	_, err := ResolveSubagent(context.Background(), cfg, "reviewer")
	require.NoError(t, err)

	// Not baselined: the baseline enumerates the bundle's fragment/mcp/hook (3),
	// never the subagent binding (which would make it 4+).
	loader := cfg.SeededBundleLoader(false)
	_, res := collectBaselineGrants(cfg, loader)
	assert.Equal(t, 3, res.Total,
		"trust baseline enumerates fragment/mcp/hook but NOT the subagent definition")
}
