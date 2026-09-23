package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestValidatePermissionFlag pins fix B: an explicitly-typed --permissions value
// that isn't a known posture is a hard error up front, so a typo can't silently
// fall through to a more permissive default than the one typed. An
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
// are refused headless) — so a --one-shot run that resolved to plan must
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

// TestWarnPosture_VerboseNamesTheEngineHostDefault: under -v, a run that
// declared nothing and landed on its engine's declared host default says
// which default decided it; without -v, or once a flag named the posture, it
// stays silent.
func TestWarnPosture_VerboseNamesTheEngineHostDefault(t *testing.T) {
	reason := operations.EnginePermissionFacts(engines.Registry(), "claude-code").HostDefaultReason
	for _, tc := range []struct {
		name      string
		verbosity int
		flag      string
		want      bool
	}{
		{"-v, nothing declared", 1, "", true},
		{"no -v", 0, "", false},
		{"-v, the flag named it", 1, "acceptEdits", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()
			prevV, prevP := runVerbosity, runPermissions
			runVerbosity, runPermissions = tc.verbosity, tc.flag
			defer func() { runVerbosity, runPermissions = prevV, prevP }()

			st := &runState{launch: launch.Launch{Mode: engine.Interactive}, permMode: agent.PermissionAcceptEdits, backendName: "claude-code"}
			st.warnPosture()

			if tc.want {
				assert.Contains(t, buf.String(), reason)
			} else {
				assert.NotContains(t, buf.String(), reason)
			}
		})
	}
}
