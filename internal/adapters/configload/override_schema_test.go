package configload

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// warningsOfKind returns the texts of cfg's warnings of one kind, so an
// assertion can name what it expected instead of matching on substrings of the
// whole warning list.
func warningsOfKind(cfg *config.Config, kind config.WarningKind) []string {
	var out []string
	for _, w := range cfg.GetWarnings() {
		if w.Kind == kind {
			out = append(out, w.Text)
		}
	}
	return out
}

// TestLoad_ConfigSetEnumTypoIsSchemaChecked closes the asymmetry between the
// two doors into the same key: `permissions: {mode: plann}` written into config.yaml
// was refused by the per-layer schema validation, while the same value passed
// as --config-set reached the merged document having been checked against
// nothing at all. Both must now produce a validate-kind warning, which the
// strict startup gate turns into a fatal finding.
func TestLoad_ConfigSetEnumTypoIsSchemaChecked(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`version: 1
agents:
  reviewer:
    profiles: [default]
`), 0644)

	overrides := confload.Overrides{Flags: map[string]any{"agents.reviewer.permissions.mode": "plann"}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	reviewer, ok := cfg.GetConfiguredAgents()["reviewer"]
	require.True(t, ok, "fixture check: the override must have reached the binding")
	assert.Equal(t, "plann", reviewer.Permissions.Mode,
		"the refused value is still applied, so the consumer's own fail-closed handling sees what was typed")

	found := warningsOfKind(cfg, config.WarnKindValidate)
	require.Len(t, found, 1, "exactly one validate warning for the one bad override")
	assert.Contains(t, found[0], "agents.reviewer.permissions.mode", "the warning must name the key that broke")
	assert.Contains(t, strings.ToLower(found[0]), "config-set", "and the override that set it")
}

// TestLoad_ConfigSetTypeGuessIsSchemaChecked covers the second fault the same
// gate catches: --config-set and CTXLOOM_CONFIG_* values are type-GUESSED from
// the raw string, and the comma arm turns a string containing a comma into a
// list. `model=opus,sonnet` therefore reached a string-typed schema key as a
// []any — measured against a running binary as the model silently VANISHING
// from the resolved engine, with no diagnostic anywhere.
func TestLoad_ConfigSetTypeGuessIsSchemaChecked(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`version: 1
llm:
  configs:
    big:
      type: claude-code
`), 0644)

	overrides := confload.Overrides{Flags: map[string]any{"llm.configs.big.model": []any{"opus", "sonnet"}}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	entry, ok := cfg.GetLLMEntry("big")
	require.True(t, ok, "fixture check: the label must still load")
	_, isString := entry.Body["model"].(string)
	require.False(t, isString, "fixture check: the coerced list is what reached the document")

	found := warningsOfKind(cfg, config.WarnKindValidate)
	require.Len(t, found, 1)
	assert.Contains(t, found[0], "llm.configs.big.model")
}

// TestLoad_ValidOverridesWarnNothing is the false-positive control for the two
// tests above: the gate must be silent on every value a user may legitimately
// set, or it turns the override channel into a source of noise.
func TestLoad_ValidOverridesWarnNothing(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(`version: 1
agents:
  reviewer:
    profiles: [default]
`), 0644)

	overrides := confload.Overrides{Flags: map[string]any{
		"agents.reviewer.permissions.mode": "plan",
		"llm.configs.big.model":            "opus",
	}}
	cfg, err := Load(WithFS(fs), WithAppDir(appDir), WithOverrides(overrides))
	require.NoError(t, err)

	reviewer, ok := cfg.GetConfiguredAgents()["reviewer"]
	require.True(t, ok)
	require.Equal(t, "plan", reviewer.Permissions.Mode, "fixture check: the overrides really did apply")
	assert.Empty(t, cfg.GetWarnings(), "a legitimate override must produce no warning of any kind")
}
