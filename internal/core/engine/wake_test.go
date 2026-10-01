package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The wake line is the owner's ruled wording (D6): a pure trigger that names
// the nonce and says where the mail arrives. Pinned as a literal so a
// rewording is a deliberate, reviewed change to model-visible text.
func TestWakeText_IsTheRuledTriggerLine(t *testing.T) {
	assert.Equal(t, "ctxloom: wake 0123456789abcdef (mail delivered at turn start)", engine.WakeText("0123456789abcdef"))
}

func TestWakeText_RoundTripsThroughWakeNonce(t *testing.T) {
	got, ok := engine.WakeNonce(engine.WakeText("0123456789abcdef"))
	require.True(t, ok)
	assert.Equal(t, "0123456789abcdef", got)

	got, ok = engine.WakeNonce("  " + engine.WakeText("0123456789abcdef") + "\n")
	assert.True(t, ok, "surrounding whitespace is transport, not content")
	assert.Equal(t, "0123456789abcdef", got)
}

// A prompt that merely MENTIONS the wake text is a human's prompt: reading it
// as a wake would let the hook block — erase — what the human typed. A
// self-sent post reaches the hook as the BARE line, so nothing here unwraps
// an envelope either: a wrapped line is not a wake.
func TestWakeNonce_OnlyTheWholePromptIsAWake(t *testing.T) {
	text := engine.WakeText("0123456789abcdef")
	for _, prompt := range []string{
		"",
		"please look at the mail",
		"why did I see `" + text + "`?",
		text + " and also fix the build",
		"<cross-session-message from-mode=\"bypass\">\n" + text + "\n</cross-session-message>",
		"ctxloom: wake ../../etc/passwd (mail delivered at turn start)",
		"ctxloom: wake 0123456789ABCDEF (mail delivered at turn start)",
		"ctxloom: wake 0123456789abcde (mail delivered at turn start)",
		"ctxloom: mail pending (wake 0123456789abcdef)",
	} {
		_, ok := engine.WakeNonce(prompt)
		assert.False(t, ok, "prompt %q", prompt)
	}
}
