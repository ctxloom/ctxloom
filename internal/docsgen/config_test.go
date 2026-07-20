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

	require.NoError(t, GenConfig(&Product{Bin: "ctxloom"}, schemaPath, dir))

	out, err := os.ReadFile(filepath.Join(dir, "config.md"))
	require.NoError(t, err)
	doc := string(out)

	assert.Contains(t, doc, "CTXLOOM_CONFIG_", "must name the env-var prefix so a reader can construct one")
	assert.Contains(t, doc, "DEFAULT_AGENT", "must show the prefix applied to a real field so the convention is concrete, not abstract")
	assert.Contains(t, doc, "--config-set", "must name --config-set as the CLI override mechanism")
	assert.Contains(t, doc, "--config-set default_agent=<value>", "must show --config-set applied to a real field, dotted-path form")
	assert.Regexp(t, `(?i)home.*project.*(environment|env).*set`, doc,
		"must state the full precedence order home < project < env < --config-set, in that order")
}

// TestGenConfig_UsesProductsOwnConventions proves a SECOND product's
// generated config page states ITS OWN env-var prefix and home-dir
// convention, not ctxloom's hardcoded ones — the bug this parametrization
// (Product threaded through GenConfig) fixes: before it, every product's
// page said "CTXLOOM_CONFIG_" and "~/.ctxloom/config.yaml" regardless of
// which binary it was actually documenting.
func TestGenConfig_UsesProductsOwnConventions(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "taskloom-config-schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{
		"title": "taskloom configuration",
		"type": "object",
		"properties": {
			"homing": {"type": "string", "description": "Task-store homing mode."}
		}
	}`), 0o644))

	require.NoError(t, GenConfig(&Product{Bin: "taskloom"}, schemaPath, dir))

	out, err := os.ReadFile(filepath.Join(dir, "config.md"))
	require.NoError(t, err)
	doc := string(out)

	assert.Contains(t, doc, "TASKLOOM_CONFIG_", "must use taskloom's own env-var prefix")
	assert.NotContains(t, doc, "CTXLOOM_CONFIG_", "must never leak ctxloom's env-var prefix onto another product's page")
	assert.Contains(t, doc, "~/.taskloom/config.yaml", "must name taskloom's own home config path")
	assert.NotContains(t, doc, "~/.ctxloom/config.yaml", "must never leak ctxloom's own home config path")
	assert.Contains(t, doc, schemaPath, "must cite the schema path it was actually generated from")
}
