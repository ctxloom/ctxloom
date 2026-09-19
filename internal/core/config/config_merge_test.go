package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMergeDefaultConfig_NonEmptyRegistryUntouched pins the determinism rule: a
// user who configured any LLMs owns the registry. The embedded default is a
// whole-registry fallback for the empty case, never a per-key overlay — so it
// must not inject default labels that would defeat the single-entry rule.
func TestMergeDefaultConfig_NonEmptyRegistryUntouched(t *testing.T) {
	cfg := &Config{lm: LMConfig{Configs: map[string]LLMConfig{
		"only": {Type: "claude-code", Body: map[string]interface{}{"model": "haiku"}},
	}}}

	mergeDefaultConfig(cfg)

	assert.Len(t, cfg.lm.Configs, 1, "merge must not inject default labels into a non-empty registry")
	assert.Contains(t, cfg.lm.Configs, "only")
	assert.Empty(t, cfg.lm.Defaults.Primary, "roles stay unset so the single-entry rule resolves them")
}

// TestMergeDefaultConfig_EmptyAdoptsDefault verifies the empty case still
// resolves a primary + fast role from the embedded default (out-of-box distill).
func TestMergeDefaultConfig_EmptyAdoptsDefault(t *testing.T) {
	cfg := &Config{}

	mergeDefaultConfig(cfg)

	assert.NotEmpty(t, cfg.lm.Configs, "empty registry should adopt the embedded default")
	assert.NotEmpty(t, cfg.FastLabel(), "fast role must resolve out of the box")
}

// TestSingleEntryRule_ResolvesBothRoles pins that one configured entry is the
// one used for every role, with no explicit role assignment.
func TestSingleEntryRule_ResolvesBothRoles(t *testing.T) {
	cfg := &Config{lm: LMConfig{Configs: map[string]LLMConfig{
		"solo": {Type: "claude-code", Body: map[string]interface{}{"model": "opus"}},
	}}}

	assert.Equal(t, "solo", cfg.PrimaryLabel())
	assert.Equal(t, "solo", cfg.FastLabel())
}
