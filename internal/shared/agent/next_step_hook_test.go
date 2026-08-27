package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewNextStepHook_RidesTurnEndNotSessionEnd pins the seam choice, which is
// the load-bearing decision in this feature. session_end is too late (no live
// agent holds the context) and is not the same event on every engine; TurnEnd
// is routed by every backend and fires while the agent can still be read.
//
// MUTATION — register the hook on any other lifecycle in
// appendManagedDynamicHooks, or change what NewNextStepHook invokes — turns
// this red.
func TestNewNextStepHook_InvokesTheNextStepCallback(t *testing.T) {
	h := NewNextStepHook()

	assert.Equal(t, "command", h.Type)
	assert.Contains(t, h.Command, "hook next-step",
		"the hook must invoke the next-step callback, not some other subcommand")
	assert.Greater(t, h.Timeout, ToolReflectTimeout,
		"reading a transcript needs longer than a hook that only reads its own stdin")
	assert.Equal(t, NextStepTimeout, h.Timeout)
}

// TestNewNextStepHook_QuotesTheBinaryPath pins that the self-exec path is
// shell-quoted, so a ctxloom binary living under a path with a space in it
// cannot break the command split.
//
// MUTATION — interpolate CtxloomCommand() bare instead of through
// shellSingleQuote — turns this red.
func TestNewNextStepHook_QuotesTheBinaryPath(t *testing.T) {
	h := NewNextStepHook()
	assert.True(t, strings.HasPrefix(h.Command, "'"),
		"the binary path must be single-quoted: %q", h.Command)
	assert.Contains(t, h.Command, shellSingleQuote(CtxloomCommand()))
}

// TestNewNextStepHook_TakesNoHarpArgument pins that the harp is resolved from
// the environment at FIRE time rather than baked into the installed command.
// apply-hooks writes these settings once, and they outlive the session that
// wrote them — a harp interpolated here would send every later session's
// capture into the first session's directory.
//
// MUTATION — add a --harp flag carrying a resolved harp to the command — red.
func TestNewNextStepHook_TakesNoHarpArgument(t *testing.T) {
	h := NewNextStepHook()
	assert.NotContains(t, h.Command, "--harp",
		"the installed command outlives its session; the harp must come from %s at fire time", SessionHarpEnv)
	assert.Equal(t, "hook next-step", h.Command[strings.LastIndex(h.Command, "hook next-step"):],
		"the callback must take no arguments: %q", h.Command)
}
