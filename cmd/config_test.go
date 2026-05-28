// Tests for cmd/config.go's extracted helpers. The cobra wrappers are
// trivial composition (load config + delegate to helper + write to
// cmd.OutOrStdout), so the testable surface is the section-name switch
// and the YAML marshal step.
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// fixtureConfig returns a Config populated with a sentinel value in every
// section resolveConfigSection knows about, so each branch's return can
// be distinguished in the marshaled YAML.
func fixtureConfig() *config.Config {
	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"developer": {Bundles: []string{"alice/coding"}},
		},
	}
	cfg.Defaults.LLMPlugin = "claude-code"
	cfg.LM.Plugins = map[string]config.PluginConfig{
		"gemini": {Model: "gemini-2.5-pro"},
	}
	cfg.MCP.Servers = map[string]config.MCPServer{
		"fs": {Command: "mcp-fs"},
	}
	return cfg
}

func TestResolveConfigSection_KnownSections(t *testing.T) {
	cfg := fixtureConfig()

	cases := []struct {
		name     string
		section  string
		contains string // substring expected in the marshaled YAML
	}{
		{"defaults", "defaults", "claude-code"},
		{"llm", "llm", "gemini-2.5-pro"},
		{"mcp", "mcp", "mcp-fs"},
		{"profiles", "profiles", "developer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveConfigSection(cfg, tc.section)
			require.NoError(t, err)
			require.NotNil(t, got, "known section should return non-nil data")

			var buf bytes.Buffer
			require.NoError(t, renderConfigSection(cfg, tc.section, &buf))
			assert.Contains(t, buf.String(), tc.contains,
				"%s section's marshaled YAML should mention its sentinel value", tc.section)
		})
	}
}

func TestResolveConfigSection_UnknownSection(t *testing.T) {
	cfg := fixtureConfig()
	_, err := resolveConfigSection(cfg, "nope")
	require.Error(t, err)
	// The error message has to list valid sections so the CLI user
	// can recover without reading docs. All four must appear.
	msg := err.Error()
	for _, want := range []string{"defaults", "llm", "mcp", "profiles"} {
		assert.Contains(t, msg, want, "error must list section %q", want)
	}
	assert.Contains(t, msg, "nope", "error must echo the bad section name")
}

func TestRenderConfigSection_UnknownSurfacesError(t *testing.T) {
	// renderConfigSection must propagate resolveConfigSection's error
	// rather than swallowing it or writing partial output.
	var buf bytes.Buffer
	err := renderConfigSection(fixtureConfig(), "bogus", &buf)
	require.Error(t, err)
	assert.Empty(t, buf.String(), "no output should be written on unknown section")
}

func TestRenderConfigYAML_RoundTripsTopLevelKeys(t *testing.T) {
	// renderConfigYAML serializes the whole config; we verify only that
	// the top-level keys we expect ("llm", "defaults", "mcp", "profiles")
	// are present in the output. Full struct equivalence is yaml.Marshal's
	// problem, not ours.
	var buf bytes.Buffer
	require.NoError(t, renderConfigYAML(fixtureConfig(), &buf))

	out := buf.String()
	for _, key := range []string{"llm:", "defaults:", "mcp:", "profiles:"} {
		assert.True(t, strings.Contains(out, key),
			"full-config YAML should contain top-level %q (got: %q)", key, out)
	}
}
