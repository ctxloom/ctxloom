package configload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestLoad_ProjectAgentBindingReplacesHomesWholesale(t *testing.T) {
	home := testsupport.Isolate(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ctxloom", "config.yaml"), []byte(`schema_version: 7
agents:
  reviewer:
    permissions:
      claude-code:
        mode: bypass
    runtime: container
`), 0o644))

	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`schema_version: 7
agents:
  reviewer:
    profiles: [default]
`), 0644)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	reviewer, ok := cfg.GetConfiguredAgents()["reviewer"]
	require.True(t, ok, "the project's own agent must still be present")

	// MUTATION TARGET: with layerMergeFunc removed (falling back to
	// koanf's default deep merge), these two would leak in from home.
	assert.True(t, reviewer.Permissions.IsZero(), "the project's binding replaces home's same-named one whole, so home's permissions do not fuse into it")
	assert.Equal(t, "", reviewer.Runtime, "home's runtime must not leak in either -- the whole binding comes from the layer that named it")
	assert.Equal(t, []string{"default"}, reviewer.Profiles, "the project's own field must survive untouched")
}

// TestAgentBindingMergeFunc_AgentNamedOnlyByLowerLayerSurvives proves the
// MERGE mechanism's own half of decision 3's trade-off: an agent
// the higher layer doesn't mention at all must still come through from the
// lower layer untouched at the merge step -- "whichever layer names the
// agent wins" is not "a lower layer's agent is wiped just because a higher
// layer exists".
func TestAgentBindingMergeFunc_AgentNamedOnlyByLowerLayerSurvives(t *testing.T) {
	dest := map[string]any{
		"agents": map[string]any{
			"personal": map[string]any{"profiles": []any{"dev"}},
		},
	}
	src := map[string]any{
		"agents": map[string]any{
			"other": map[string]any{"profiles": []any{"default"}},
		},
	}
	require.NoError(t, layerMergeFunc(src, dest))

	agents := dest["agents"].(map[string]any)
	require.Contains(t, agents, "personal", "an agent the higher layer never names must survive the merge untouched")
	assert.Equal(t, map[string]any{"profiles": []any{"dev"}}, agents["personal"])
	require.Contains(t, agents, "other")
}

// TestAgentBindingMergeFunc_ReplacesWholesale is a narrow unit test of
// ctxloom's own merge func, independent of the full Load pipeline.
func TestAgentBindingMergeFunc_ReplacesWholesale(t *testing.T) {
	dest := map[string]any{
		"agents": map[string]any{
			"reviewer": map[string]any{"permissions": map[string]any{"mode": "bypass"}, "coordinator": true, "runtime": "container"},
		},
	}
	src := map[string]any{
		"agents": map[string]any{
			"reviewer": map[string]any{"profiles": []any{"default"}},
		},
	}
	require.NoError(t, layerMergeFunc(src, dest))

	agents := dest["agents"].(map[string]any)
	reviewer := agents["reviewer"].(map[string]any)
	assert.Equal(t, map[string]any{"profiles": []any{"default"}}, reviewer)
}

func TestAgentBindingMergeFunc_NonAgentKeysStillDeepMerge(t *testing.T) {
	dest := map[string]any{"editor": map[string]any{"command": "vim", "args": []any{"-p"}}}
	src := map[string]any{"editor": map[string]any{"command": "nano"}}
	require.NoError(t, layerMergeFunc(src, dest))

	editor := dest["editor"].(map[string]any)
	assert.Equal(t, "nano", editor["command"], "the higher layer's value must win")
	assert.Equal(t, []any{"-p"}, editor["args"], "an untouched sibling field must survive (deep merge, not replace)")
}

// TestLoad_ConfigSetPatchesOneAgentFieldWithoutWipingSiblings is the
// end-to-end regression test for a bug MEASURED against a running binary
// while building this seam: --config-set targeting ONE field of an agent the
// project already declares used to wipe out every OTHER field of that same
// agent (profiles, engine, ...), because ApplyOverrides used to merge the
// flag layer through the SAME atomic-replace-aware path (layerMergeFunc)
// Load's file-layer merge uses — the flag's one-field patch "named" the
// agent, so layerMergeFunc replaced the WHOLE binding with just that
// field. Fixed by confload.ApplyOverrides always resolving overrides through
// the package's plain Merge, never a Product's own MergeFunc (see
// internal/shared/confload's TestApplyOverrides_FlagOverride_
// NeverGoesThroughProductMergeFunc for the generic, product-agnostic proof).
func TestLoad_ConfigSetPatchesOneAgentFieldWithoutWipingSiblings(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`schema_version: 7
agents:
  reviewer:
    profiles: [default]
    llm: claude-code
`), 0644)

	overrides := confload.Overrides{Flags: map[string]any{"agents.reviewer.permissions.claude-code.mode": "bypass"}}
	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	reviewer, ok := cfg.GetConfiguredAgents()["reviewer"]
	require.True(t, ok)
	assert.Equal(t, "bypass", reviewer.Permissions.Engines["claude-code"]["mode"], "the override itself must still apply")
	assert.Equal(t, []string{"default"}, reviewer.Profiles, "a sibling field the override never touched must survive")
	assert.Equal(t, "claude-code", reviewer.LLM, "same for a second untouched sibling field")
}

// TestLayerMergeFunc_CompanionsUnionAcrossLayers: a project's `companions`
// ADDS to home's, never replaces it — the lower layer's names first, in its
// order, then each name only the higher layer adds, deduplicated.
func TestLayerMergeFunc_CompanionsUnionAcrossLayers(t *testing.T) {
	dest := map[string]any{"companions": []any{"ltk", "acme"}}
	src := map[string]any{"companions": []any{"beta", "acme"}}
	require.NoError(t, layerMergeFunc(src, dest))
	assert.Equal(t, []any{"ltk", "acme", "beta"}, dest["companions"])
}

// TestLayerMergeFunc_ProjectCannotRemoveAHomeCompanion: an explicitly empty
// project list is not a way to unregister what home registered.
func TestLayerMergeFunc_ProjectCannotRemoveAHomeCompanion(t *testing.T) {
	dest := map[string]any{"companions": []any{"ltk"}}
	src := map[string]any{"companions": []any{}}
	require.NoError(t, layerMergeFunc(src, dest))
	assert.Equal(t, []any{"ltk"}, dest["companions"])
}

// TestLoad_ProjectCompanionsAddToHomes runs the whole layered Load: the
// effective list is home's names plus the project's.
func TestLoad_ProjectCompanionsAddToHomes(t *testing.T) {
	home := testsupport.Isolate(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ctxloom", "config.yaml"), []byte(`schema_version: 7
companions: [ltk, acme]
`), 0o644))

	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`schema_version: 7
companions: [beta]
`), 0644)

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, []string{"ltk", "acme", "beta"}, cfg.GetCompanions())
}
