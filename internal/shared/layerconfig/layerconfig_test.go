package layerconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayer_ProjectOverridesHome(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{"editor": "nano"}}
	project := Layer{Name: "project", Values: map[string]any{"editor": "vim"}}

	merged := Merge(home, project)

	assert.Equal(t, "vim", merged["editor"], "a scalar set at project must beat the same key at home")
}

func TestLayer_HomeKeyInheritedWhenProjectSilent(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{"workspace": "worktree"}}
	// project sets an unrelated key only — "workspace" never appears in its map.
	project := Layer{Name: "project", Values: map[string]any{"default_agent": "dev"}}

	merged := Merge(home, project)

	assert.Equal(t, "worktree", merged["workspace"],
		"a key the project never mentions must be inherited from home, not dropped")
	assert.Equal(t, "dev", merged["default_agent"])
}

func TestLayer_DeepMergeNestedMaps(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{
		"llm": map[string]any{
			"configs": map[string]any{
				"a": map[string]any{"model": "home-a"},
				"b": map[string]any{"model": "home-b"},
			},
		},
	}}
	project := Layer{Name: "project", Values: map[string]any{
		"llm": map[string]any{
			"configs": map[string]any{
				"a": map[string]any{"model": "project-a"},
			},
		},
	}}

	merged := Merge(home, project)

	configs := merged["llm"].(map[string]any)["configs"].(map[string]any)
	assert.Equal(t, "project-a", configs["a"].(map[string]any)["model"],
		"a key both layers set must be overridden")
	assert.Equal(t, "home-b", configs["b"].(map[string]any)["model"],
		"a sibling key only home sets must survive the deep merge untouched")
}

func TestLayer_ListsReplaceNotConcat(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{
		"isolation_engines": []any{"claude-code", "codex", "kiro"},
	}}
	project := Layer{Name: "project", Values: map[string]any{
		"isolation_engines": []any{"codex"},
	}}

	merged := Merge(home, project)

	assert.Equal(t, []any{"codex"}, merged["isolation_engines"],
		"a list set at project must REPLACE home's list wholesale, never concatenate — "+
			"otherwise a project could never shorten or remove an inherited entry")
}

func TestLayer_ExplicitZeroValueBeatsInheritance(t *testing.T) {
	t.Run("explicit false beats inherited true", func(t *testing.T) {
		home := Layer{Name: "home", Values: map[string]any{
			"ui": map[string]any{"surround": true},
		}}
		project := Layer{Name: "project", Values: map[string]any{
			"ui": map[string]any{"surround": false},
		}}

		merged := Merge(home, project)

		assert.Equal(t, false, merged["ui"].(map[string]any)["surround"],
			"a project explicitly disabling a feature must not silently inherit home's enabled state")
	})

	t.Run("explicit empty string beats inherited non-empty", func(t *testing.T) {
		home := Layer{Name: "home", Values: map[string]any{"runtime": "container"}}
		project := Layer{Name: "project", Values: map[string]any{"runtime": ""}}

		merged := Merge(home, project)

		assert.Equal(t, "", merged["runtime"],
			"an explicit empty-string override must beat an inherited non-empty value")
	})

	t.Run("explicit zero beats inherited nonzero", func(t *testing.T) {
		home := Layer{Name: "home", Values: map[string]any{"retries": 3}}
		project := Layer{Name: "project", Values: map[string]any{"retries": 0}}

		merged := Merge(home, project)

		assert.Equal(t, 0, merged["retries"])
	})
}

func TestPrecedence_HomeProjectEnvCLI(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{"key": "home"}}
	project := Layer{Name: "project", Values: map[string]any{"key": "project"}}
	env := Layer{Name: "env", Values: map[string]any{"key": "env"}}
	cli := Layer{Name: "cli", Values: map[string]any{"key": "cli"}}

	tests := []struct {
		name   string
		layers []Layer
		want   string
	}{
		{"all four levels: CLI wins", []Layer{home, project, env, cli}, "cli"},
		{"CLI removed: env wins", []Layer{home, project, env}, "env"},
		{"env removed too: project wins", []Layer{home, project}, "project"},
		{"project removed too: home is all that's left", []Layer{home}, "home"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged := Merge(tc.layers...)
			assert.Equal(t, tc.want, merged["key"])
		})
	}
}

// TestPrecedence_BootstrapResolvedBeforeLayering pins D2: WHICH project layer
// participates is a decision made before Merge ever runs (stage 1, bootstrap —
// e.g. CTXLOOM_ROOT / walk-up / home fallback choosing a directory) — Merge
// itself has zero awareness of roots, directories, or environment variables. It
// only ever combines the Layer values it is handed. Two different bootstrap
// outcomes (as if CTXLOOM_ROOT had pointed at project A vs. project B) feed
// entirely different "project" layers into the identical merge machinery, and
// Merge faithfully reflects whichever was selected — it never re-derives or
// second-guesses the selection itself.
func TestPrecedence_BootstrapResolvedBeforeLayering(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{"name": "home-default", "shared": "h"}}
	env := Layer{Name: "env", Values: map[string]any{}}
	cli := Layer{Name: "cli", Values: map[string]any{}}

	projectA := Layer{Name: "project", Values: map[string]any{"name": "A"}}
	projectB := Layer{Name: "project", Values: map[string]any{"name": "B"}}

	mergedA := Merge(home, projectA, env, cli)
	mergedB := Merge(home, projectB, env, cli)

	assert.Equal(t, "A", mergedA["name"], "bootstrap picking project A must be reflected in the merge")
	assert.Equal(t, "B", mergedB["name"], "bootstrap picking project B must be reflected in the merge")
	assert.Equal(t, "h", mergedA["shared"], "an unrelated home key is unaffected by which project layer bootstrap chose")

	// A bootstrap-shaped key (as CTXLOOM_ROOT would be) is not special to
	// Merge if it shows up INSIDE a layer's Values: it is just an ordinary
	// setting, proving Merge does not itself interpret or act on any
	// "which directory" concept — that decision already happened upstream.
	cliWithRootLikeKey := Layer{Name: "cli", Values: map[string]any{"root": "/wherever"}}
	merged := Merge(home, projectA, env, cliWithRootLikeKey)
	assert.Equal(t, "/wherever", merged["root"], "a root-shaped key is merged like any other ordinary value")
}

// TestLayerConfig_NonStringMapKeyDoesNotSilentlyReplace is the asMap
// regression test: a map[interface{}]interface{} value (what yaml.v2, or a
// hand-built caller, produces for a YAML mapping) keyed by something other
// than a string used to make asMap bail out with (nil, false) — which
// mergeInto then treated as "not a map after all", falling through to a
// whole-value REPLACE of the ENTIRE section instead of a deep merge. That
// silently dropped every sibling key a lower layer set (home's "b" below)
// the instant ANY key in the section wasn't a plain string, with no error and
// no warning — exactly this project's characteristic failure mode.
func TestLayerConfig_NonStringMapKeyDoesNotSilentlyReplace(t *testing.T) {
	home := Layer{Name: "home", Values: map[string]any{
		"section": map[interface{}]interface{}{
			"a": "home-a",
			"b": "home-b",
			1:   "home-numeric-key",
		},
	}}
	project := Layer{Name: "project", Values: map[string]any{
		"section": map[interface{}]interface{}{
			"a": "project-a",
		},
	}}

	merged := Merge(home, project)

	section, ok := merged["section"].(map[string]any)
	require.True(t, ok, "a map[interface{}]interface{} value must still merge as a nested map, never a bare replace")
	assert.Equal(t, "project-a", section["a"], "the key both layers set is still overridden")
	assert.Equal(t, "home-b", section["b"],
		"a sibling key ONLY home sets must survive the deep merge — this is what breaks if the non-string "+
			"key aborts the whole conversion instead of being stringified")
	assert.Equal(t, "home-numeric-key", section["1"], "the non-string key is preserved (stringified), not silently dropped")
}
