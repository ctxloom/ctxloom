package confload

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file pins the two koanf-specific pitfalls the migration off
// layerconfig had to design around (see confload.go's package doc,
// "Config files and merging are never routed through viper" and delim's own
// doc), plus the D1/D3 merge-semantics carryovers from the old
// layerconfig_test.go, now exercised against Merge's koanf-backed
// implementation directly instead of a hand-rolled mergeInto.

// TestKoanf_IntStaysInt is the first koanf edge: koanf.Raw() round-trips a
// value in a way that can turn an int into a float64 for some callers, so
// this package must never call it -- Merge reads its result back out via
// Unmarshal instead (see Merge's doc). A config field like `version: 6` or
// `retries: 3` must survive a merge as a Go int, not silently become a
// float64 that later trips up a type switch or an equality assertion
// downstream.
func TestKoanf_IntStaysInt(t *testing.T) {
	home := map[string]any{"version": 6, "retries": 3}
	project := map[string]any{"name": "proj"}

	merged, err := Merge(home, project)
	require.NoError(t, err)

	assert.Equal(t, 6, merged["version"])
	assert.IsType(t, int(0), merged["version"], "version must come back as an int, not float64")
	assert.Equal(t, 3, merged["retries"])
	assert.IsType(t, int(0), merged["retries"], "retries must come back as an int, not float64")
}

// TestKoanf_DottedMapKeySurvives is the second koanf edge: koanf's own
// default path delimiter is ".", which would shatter a literally-dotted
// config key -- an LLM config legitimately labeled "gpt-4.1" -- into two
// path segments ("gpt-4" and "1") the instant anything flattened or
// unflattened a path through it. This package uses delim (an ASCII Unit
// Separator, never ".") for every koanf instance and path-join, so a
// "gpt-4.1" key loaded from a file layer must survive Merge as ONE atomic
// map key, not two nested ones.
func TestKoanf_DottedMapKeySurvives(t *testing.T) {
	home := map[string]any{
		"llm": map[string]any{
			"configs": map[string]any{
				"gpt-4.1": map[string]any{"model": "gpt-4.1"},
			},
		},
	}

	merged, err := Merge(home)
	require.NoError(t, err)

	configs, ok := merged["llm"].(map[string]any)["configs"].(map[string]any)
	require.True(t, ok)
	entry, ok := configs["gpt-4.1"].(map[string]any)
	require.True(t, ok, `"gpt-4.1" must survive intact as one map key, not be split into "gpt-4"/"1"`)
	assert.Equal(t, "gpt-4.1", entry["model"])

	// The shattered-key shape must simply not exist.
	_, gotSplitParent := configs["gpt-4"]
	assert.False(t, gotSplitParent, `a dotted key must never be split into a nested "gpt-4" -> "1" structure`)
}

// TestKoanf_ListsReplaceNotConcat pins D1 (layerconfig's original name for
// this rule) against Merge's koanf-backed implementation: a higher layer's
// list REPLACES a lower layer's wholesale, never concatenates -- otherwise a
// project could never shorten or remove an inherited entry without
// inventing a negation syntax.
func TestKoanf_ListsReplaceNotConcat(t *testing.T) {
	home := map[string]any{"isolation_engines": []any{"claude-code", "codex", "kiro"}}
	project := map[string]any{"isolation_engines": []any{"codex"}}

	merged, err := Merge(home, project)
	require.NoError(t, err)

	assert.Equal(t, []any{"codex"}, merged["isolation_engines"])
}

// TestKoanf_ExplicitZeroBeatsInheritance pins D3: a project explicitly
// setting a key to its zero value (false/0/"") must beat an inherited
// non-zero value from home, and must be indistinguishable in outcome from
// any other override -- presence, not truthiness, decides. This is the
// requirement a naive "skip falsy/zero values" merge implementation fails.
func TestKoanf_ExplicitZeroBeatsInheritance(t *testing.T) {
	t.Run("explicit false beats inherited true", func(t *testing.T) {
		home := map[string]any{"ui": map[string]any{"surround": true}}
		project := map[string]any{"ui": map[string]any{"surround": false}}

		merged, err := Merge(home, project)
		require.NoError(t, err)

		assert.Equal(t, false, merged["ui"].(map[string]any)["surround"])
	})

	t.Run("explicit empty string beats inherited non-empty", func(t *testing.T) {
		home := map[string]any{"runtime": "container"}
		project := map[string]any{"runtime": ""}

		merged, err := Merge(home, project)
		require.NoError(t, err)

		assert.Equal(t, "", merged["runtime"])
	})

	t.Run("explicit zero beats inherited nonzero", func(t *testing.T) {
		home := map[string]any{"retries": 3}
		project := map[string]any{"retries": 0}

		merged, err := Merge(home, project)
		require.NoError(t, err)

		assert.Equal(t, 0, merged["retries"])
	})
}

// TestKoanf_CaseSensitiveKeysPreserved is the whole reason koanf was chosen
// over viper (see confload.go's package doc): viper's case-insensitivity is
// hardcoded (no configuration hook), which silently lowercased a real
// backend's `env: {GEMINI_API_KEY: ...}` map key in production (ctxloom
// commit 26f96c7). koanf is case-sensitive by design. This exercises three
// distinct case-sensitive shapes ctxloom's schema legitimately carries: an
// agent label (MyCoder), an env-passthrough var name (GEMINI_API_KEY), and a
// mixed-case template variable -- all surviving an ordinary Merge untouched.
func TestKoanf_CaseSensitiveKeysPreserved(t *testing.T) {
	home := map[string]any{
		"agents": map[string]any{
			"MyCoder": map[string]any{"engine": "claude-code"},
		},
	}
	project := map[string]any{
		"llm": map[string]any{
			"configs": map[string]any{
				"big": map[string]any{
					"env": map[string]any{"GEMINI_API_KEY": "secret"},
				},
			},
		},
		"profiles": map[string]any{
			"definitions": map[string]any{
				"go-developer": map[string]any{
					"variables": map[string]any{"TargetPackage": "internal/config"},
				},
			},
		},
	}

	merged, err := Merge(home, project)
	require.NoError(t, err)

	agents := merged["agents"].(map[string]any)
	_, hasMyCoder := agents["MyCoder"]
	assert.True(t, hasMyCoder, "agent label MyCoder must survive with its exact case")
	_, hasLowercased := agents["mycoder"]
	assert.False(t, hasLowercased, "must not gain a lower-cased sibling")

	envMap := merged["llm"].(map[string]any)["configs"].(map[string]any)["big"].(map[string]any)["env"].(map[string]any)
	assert.Equal(t, "secret", envMap["GEMINI_API_KEY"])
	_, hasLoweredEnvKey := envMap["gemini_api_key"]
	assert.False(t, hasLoweredEnvKey, "GEMINI_API_KEY must not be lower-cased")

	vars := merged["profiles"].(map[string]any)["definitions"].(map[string]any)["go-developer"].(map[string]any)["variables"].(map[string]any)
	assert.Equal(t, "internal/config", vars["TargetPackage"])
	_, hasLoweredVar := vars["targetpackage"]
	assert.False(t, hasLoweredVar, "the template variable name must not be lower-cased")
}
