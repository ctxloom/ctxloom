package config_test

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestIsLLMUserAuthored_EmptyRegistry_DefaultLabelsAreNotUserAuthored pins
// the defect `llm remove`/`llm edit`'s CRUD relies on: the default-registry overlay
// (config.LMConfig's own doc, "not a per-key overlay") fills a COMPLETELY EMPTY
// llm.configs with the WHOLE shipped registry (claude-code,
// codex, ...) so a project with none configured still resolves an engine.
// That merge happens on the READ side cfg.GetLLMLabels() reflects — so a naive
// "is label in cfg.GetLLMLabels()" check would see "claude-code" as already
// present on a project that never wrote a single llm.configs line, and
// `llm remove claude-code` would report success while deleting nothing (the
// merged entry is never persisted to begin with — see userAuthoredLM).
// IsLLMUserAuthored is what tells the two cases apart.
func TestIsLLMUserAuthored_EmptyRegistry_DefaultLabelsAreNotUserAuthored(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 6\n"), 0644))

	cfg, err := configload.Load(configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)

	require.Contains(t, cfg.GetLLMLabels(), "claude-code", "sanity: the default merge really does inject it")
	assert.False(t, cfg.IsLLMUserAuthored("claude-code"),
		"a label present only because the registry fallback filled an empty llm.configs is not user-authored")
}

// TestIsLLMUserAuthored_ExplicitEntry_IsUserAuthored is the positive case: a
// label the project's own config.yaml declares is user-authored, whether or
// not it happens to share a name with a shipped default.
func TestIsLLMUserAuthored_ExplicitEntry_IsUserAuthored(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(
		"version: 6\nllm:\n  configs:\n    big: { type: codex }\n"), 0644))

	cfg, err := configload.Load(configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.True(t, cfg.IsLLMUserAuthored("big"))
}

// TestIsLLMUserAuthored_ExplicitOverrideOfADefaultName_IsUserAuthored proves
// that DECLARING a label sharing a shipped default's name (with a DIFFERENT
// value) counts as user-authored — the default-registry overlay itself never runs once
// llm.configs is non-empty (config.LMConfig's doc), so the ambiguity this method
// exists for cannot arise here at all.
func TestIsLLMUserAuthored_ExplicitOverrideOfADefaultName_IsUserAuthored(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(
		"version: 6\nllm:\n  configs:\n    claude-code: { permissions: bypass }\n"), 0644))

	cfg, err := configload.Load(configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.True(t, cfg.IsLLMUserAuthored("claude-code"))
}

// TestIsLLMUserAuthored_UnknownLabel_IsFalse: a label absent altogether is
// obviously not user-authored.
func TestIsLLMUserAuthored_UnknownLabel_IsFalse(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 6\n"), 0644))

	cfg, err := configload.Load(configload.WithFS(fs), configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.False(t, cfg.IsLLMUserAuthored("nonexistent"))
}
