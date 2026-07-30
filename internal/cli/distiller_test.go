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

// TestNewLLMDistiller_UnresolvableLabelSaysContentWillBeStoredRaw pins
// U035-F06's reachable path. A nil Distiller is not an error to any caller:
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

		assert.Nil(t, newLLMDistiller(cfg))
		out := warn.String()
		assert.Contains(t, out, "RAW", "the user must learn the content is stored undistilled")
		assert.Contains(t, out, "llm.defaults.fast", "and how to fix it")
	})

	t.Run("nil config", func(t *testing.T) {
		warn := captureWarnings(t)
		assert.Nil(t, newLLMDistiller(nil))
		assert.Contains(t, warn.String(), "RAW")
	})

	t.Run("explicit empty label", func(t *testing.T) {
		warn := captureWarnings(t)
		assert.Nil(t, newLLMDistillerForLabel(config.NewFixture(config.Fixture{}), ""))
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

	d := newLLMDistiller(cfg)

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

// TestLoadDistillPrompt_AlwaysYieldsAPrompt pins the measured fact that removed
// U035-F06's second "silent return nil": loadDistillPrompt has no failure mode —
// every unavailable source falls back to the embedded default — so it no longer
// returns an error for a caller to discard.
func TestLoadDistillPrompt_AlwaysYieldsAPrompt(t *testing.T) {
	assert.NotEmpty(t, loadDistillPrompt())
}
