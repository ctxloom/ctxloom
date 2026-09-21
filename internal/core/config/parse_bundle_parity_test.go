package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// TestParseBundle_DivergesFromRawUnmarshalOnLegacySchema is the divergence
// this row is about, shown rather than asserted: the canonical parser applies
// the schema upgrade and the raw unmarshal silently drops the renamed key.
func TestParseBundle_DivergesFromRawUnmarshalOnLegacySchema(t *testing.T) {
	legacy := []byte(`
prompts:
  greet:
    content: hello
`)

	var raw bundles.Bundle
	require.NoError(t, yaml.Unmarshal(legacy, &raw))
	assert.Empty(t, raw.Commands,
		"a raw unmarshal drops the legacy key entirely — this is the silent-no-op the upgrade pipeline exists to prevent")

	parsed, err := bundles.ParseBundle(legacy)
	require.NoError(t, err)
	assert.Contains(t, parsed.Commands, "greet",
		"bundles.ParseBundle migrates prompts: to commands: — the two parsers do NOT agree, so which one a reader uses is a correctness question, not a style one")
}
