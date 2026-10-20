package schema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/resources"
)

// TestConfigSchema_EveryLLMBranchCarriesContextWindow: the per-label
// context window is NEUTRAL, so every $defs/llmConfig branch declares it —
// a branch added for a new engine without it would refuse the key on that
// engine's labels (additionalProperties:false) and leave its window unset.
func TestConfigSchema_EveryLLMBranchCarriesContextWindow(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	require.NoError(t, err)
	var doc struct {
		Defs struct {
			LLMConfig struct {
				AnyOf []struct {
					Title      string                     `json:"title"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"anyOf"`
			} `json:"llmConfig"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Defs.LLMConfig.AnyOf)
	for _, b := range doc.Defs.LLMConfig.AnyOf {
		cw, ok := b.Properties["context_window"]
		require.True(t, ok, "llmConfig branch %q has no context_window", b.Title)
		var spec struct {
			Type    string `json:"type"`
			Minimum *int   `json:"minimum"`
		}
		require.NoError(t, json.Unmarshal(cw, &spec))
		assert.Equal(t, "integer", spec.Type, "branch %q", b.Title)
		require.NotNil(t, spec.Minimum, "branch %q: a window of 0 or less is no window", b.Title)
		assert.Equal(t, 1, *spec.Minimum, "branch %q", b.Title)
	}
}

// TestConfigValidator_ContextWindow: a positive integer window validates on
// every engine's label, typed or untyped; zero and non-integers are refused.
func TestConfigValidator_ContextWindow(t *testing.T) {
	v, err := NewConfigValidator()
	require.NoError(t, err)
	label := func(body string) []byte {
		return []byte("schema_version: 8\nllm:\n  configs:\n    big: " + body + "\n")
	}
	for _, ok := range []string{
		"{ type: claude-code, model: opus, context_window: 1000000 }",
		"{ model: haiku, context_window: 200000 }",
		"{ type: mock, context_window: 131072 }",
	} {
		assert.NoError(t, v.ValidateBytes(label(ok)), ok)
	}
	for _, bad := range []string{
		"{ type: claude-code, context_window: 0 }",
		"{ type: claude-code, context_window: big }",
		"{ type: mock, context_window: 1.5 }",
	} {
		assert.Error(t, v.ValidateBytes(label(bad)), bad)
	}
}
