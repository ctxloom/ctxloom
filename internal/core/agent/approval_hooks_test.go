package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestApprovalHook_RunsCtxloomForTheEnginesEvent: an approval hook runs
// ctxloom's own binary, naming the event the engine gave it (opaque here:
// core holds no engine vocabulary), with the engine's matcher, outliving the
// approval timeout so the human's decision can still reach it.
func TestApprovalHook_RunsCtxloomForTheEnginesEvent(t *testing.T) {
	h := ApprovalHook("SomeEngineEvent", "SomeTool", 15*time.Minute)
	assert.Equal(t, shellSingleQuote(CtxloomCommand())+" hook permission --event SomeEngineEvent", h.Command)
	assert.Equal(t, "SomeTool", h.Matcher)
	assert.Equal(t, "command", h.Type)
	assert.Equal(t, int((15*time.Minute + ApprovalHookSlack).Seconds()), h.Timeout,
		"the hook outlives the approval timeout, so a decision made at the deadline still reaches the engine")
}
