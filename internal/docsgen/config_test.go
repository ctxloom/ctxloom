package docsgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenConfig_DocumentsOverrideMechanism proves the generated config
// reference page tells a reader HOW to override a value from the environment
// or a CLI flag -- the precedence chain (home < project < env < CLI) and the
// CTXLOOM_CONFIG_ env-var naming convention are cross-cutting mechanics that
// apply to every field, so they can't be attached to any one schema
// property; this pins them living in the generator's own fixed prose instead
// (see GenConfig's intro paragraphs), so the mechanism is discoverable
// without a reader having to already know internal/shared/confload exists.
func TestGenConfig_DocumentsOverrideMechanism(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "config-schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{
		"title": "ctxloom configuration",
		"type": "object",
		"properties": {
			"default_agent": {"type": "string", "description": "The agent run without --agent uses."}
		}
	}`), 0o644))

	require.NoError(t, GenConfig(schemaPath, dir))

	out, err := os.ReadFile(filepath.Join(dir, "config.md"))
	require.NoError(t, err)
	doc := string(out)

	assert.Contains(t, doc, "CTXLOOM_CONFIG_", "must name the env-var prefix so a reader can construct one")
	assert.Contains(t, doc, "DEFAULT_AGENT", "must show the prefix applied to a real field so the convention is concrete, not abstract")
	assert.Contains(t, doc, "CLI flag", "must name CLI flags as an override source")
	assert.Regexp(t, `(?i)home.*project.*(environment|env).*(cli|flag)`, doc,
		"must state the full precedence order home < project < env < CLI, in that order")
}
