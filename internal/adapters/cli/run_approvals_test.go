package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestValidatePermissionFlag pins fix B: an explicitly-typed --permissions value
// that isn't a known posture is a hard error up front, so a typo can't silently
// fall through to a more permissive default (e.g. claude-code's host bypass). An
// empty flag is fine (no override), and parsing is case-insensitive.
func TestValidatePermissionFlag(t *testing.T) {
	assert.NoError(t, validatePermissionFlag(""), "no override is fine")
	assert.NoError(t, validatePermissionFlag("plan"))
	assert.NoError(t, validatePermissionFlag("bypass"))
	assert.NoError(t, validatePermissionFlag("BYPASS"), "case-insensitive")
	assert.Error(t, validatePermissionFlag("plann"), "typo is rejected, not silently widened")
	assert.Error(t, validatePermissionFlag("yolo"))
}

// TestWarnPosture_PlanOneshotCancels pins the invariant: after the launch
// resolver's headless floor, plan is the only SafeHeadless posture that still
// gates a mutating call on a human (bypass never asks; default/acceptEdits
// were floored up to bypass) — so a --one-shot run that resolved to plan must
// warn loudly at startup that no human is reachable, since the engine cancels
// any gated call outright rather than hanging or running it. bypass+ONESHOT
// (nothing gates) and plan+INTERACTIVE (a human IS reachable) must both stay
// silent.
func TestWarnPosture_PlanOneshotCancels(t *testing.T) {
	cases := []struct {
		name     string
		mode     engine.Mode
		permMode agent.PermissionMode
		wantWarn bool
	}{
		{"plan + oneshot warns", engine.Structured, agent.PermissionPlan, true},
		{"bypass + oneshot stays silent", engine.Structured, agent.PermissionBypass, false},
		{"plan + interactive stays silent", engine.Interactive, agent.PermissionPlan, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			st := &runState{launch: launch.Launch{Mode: tc.mode}, permMode: tc.permMode, backendName: "mock"}
			st.warnPosture()

			if tc.wantWarn {
				assert.Contains(t, buf.String(), "--one-shot with plan permissions has no human to approve a gated call")
			} else {
				assert.Empty(t, buf.String(), "must stay silent for %s", tc.name)
			}
		})
	}
}
