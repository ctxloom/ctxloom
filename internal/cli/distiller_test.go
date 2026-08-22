package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// captureWarnings redirects clidiag's process-wide warn sink for one test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// TestNewLLMDistiller_UnresolvableLabelSaysContentWillBeStoredRaw pins the
// reachable path: a nil Distiller is not an error to any caller:
// operations stores the RAW content and every command reports success, so
// `bundle distill` could say "distilled N items" having distilled none. The
// reason has to reach the user.
func TestNewLLMDistiller_UnresolvableLabelSaysContentWillBeStoredRaw(t *testing.T) {
	t.Run("no label resolves", func(t *testing.T) {
		warn := captureWarnings(t)
		// No llm.defaults and no single llm.configs entry: PrimaryLabel (and so
		// FastLabel) returns "".
		cfg := config.NewFixture(config.Fixture{LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"a": {Type: "claude-code"},
				"b": {Type: "codex"},
			},
		}})
		require.Empty(t, cfg.FastLabel(), "fixture precondition: no label resolves")

		d, err := newLLMDistiller(cfg)
		require.NoError(t, err, "an unresolvable label is a warning, not a refusal")
		assert.Nil(t, d)
		out := warn.String()
		assert.Contains(t, out, "RAW", "the user must learn the content is stored undistilled")
		assert.Contains(t, out, "llm.defaults.fast", "and how to fix it")
	})

	t.Run("nil config", func(t *testing.T) {
		warn := captureWarnings(t)
		d, err := newLLMDistiller(nil)
		require.NoError(t, err)
		assert.Nil(t, d)
		assert.Contains(t, warn.String(), "RAW")
	})

	t.Run("explicit empty label", func(t *testing.T) {
		warn := captureWarnings(t)
		d, err := newLLMDistillerForLabel(config.NewFixture(config.Fixture{}), "")
		require.NoError(t, err)
		assert.Nil(t, d)
		assert.Contains(t, warn.String(), "RAW")
	})
}

// TestNewLLMDistiller_ResolvableLabelIsSilent is the negative control: the
// ordinary configured project builds a distiller and says nothing.
func TestNewLLMDistiller_ResolvableLabelIsSilent(t *testing.T) {
	warn := captureWarnings(t)
	cfg := config.NewFixture(config.Fixture{LM: config.LMConfig{
		Defaults: config.RoleDefaults{Fast: "fast"},
		Configs: map[string]config.LLMConfig{
			"fast": {Type: "claude-code", Body: map[string]interface{}{"model": "haiku"}},
		},
	}})

	d, err := newLLMDistiller(cfg)

	require.NoError(t, err)
	require.NotNil(t, d)
	// NotContains rather than Empty: loadDistillPrompt loads the ambient config,
	// which legitimately warns about unrelated ambient conditions (e.g. running
	// inside a linked git worktree). What must be absent is the raw-content
	// warning.
	assert.NotContains(t, warn.String(), "RAW", "a working configuration must not be warned about")
	ld, ok := d.(*llmDistiller)
	require.True(t, ok)
	assert.Equal(t, "claude-code", ld.llmName)
	assert.Equal(t, "fast", ld.llmLabel)
	assert.Equal(t, "haiku", ld.model)
	assert.NotEmpty(t, ld.prompt, "a distiller with an EMPTY prompt would silently distill against nothing")
}

// TestLoadDistillPrompt_AbsentSourcesYieldTheDefault pins the arm that is
// legitimately a fallback: nothing configured anywhere (here, no config at
// all) is an ABSENCE, and an absence gets the embedded default with no error.
// Its counterpart — a configured prompt the trust gate WITHHELD, which is a
// decision and not an absence — is TestBundleDistill_WithheldPromptRefuses.
func TestLoadDistillPrompt_AbsentSourcesYieldTheDefault(t *testing.T) {
	got, err := loadDistillPrompt(nil)
	require.NoError(t, err)
	assert.NotEmpty(t, got)
}
