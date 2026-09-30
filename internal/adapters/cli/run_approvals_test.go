package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestValidatePermissionFlag pins fix B: an explicitly-typed --permissions value
// that no engine names is a hard error up front, so a typo can't silently
// fall through to anything else. An empty flag is fine (no override), and
// matching ignores case. The vocabulary is every registered engine's.
func TestValidatePermissionFlag(t *testing.T) {
	known := operations.PostureNames(engines.Registry())
	assert.Contains(t, known, "acceptEdits", "claude's modes are named")
	assert.NoError(t, validatePermissionFlag(known, ""), "no override is fine")
	assert.NoError(t, validatePermissionFlag(known, "plan"))
	assert.NoError(t, validatePermissionFlag(known, "bypass"))
	assert.NoError(t, validatePermissionFlag(known, "BYPASS"), "case-insensitive")
	assert.Error(t, validatePermissionFlag(known, "plann"), "typo is rejected, not silently widened")
	assert.Error(t, validatePermissionFlag(known, "dontAsk"), "a retired mode names no engine's")
}

// TestWarnPosture_PlanOneshotCancels pins the invariant: a --one-shot run
// has nobody at the engine, so under every posture that still gates a call
// on a human (all but bypass) the engine denies each gated call outright —
// the run must say so at startup, naming the posture. bypass+ONESHOT
// (nothing gates) and plan+INTERACTIVE (a human IS reachable) stay silent.
func TestWarnPosture_PlanOneshotCancels(t *testing.T) {
	cases := []struct {
		name     string
		mode     engine.Mode
		permMode string
		wantWarn bool
	}{
		{"plan + oneshot warns", engine.Structured, "plan", true},
		{"default + oneshot warns", engine.Structured, "default", true},
		{"acceptEdits + oneshot warns", engine.Structured, "acceptEdits", true},
		{"bypass + oneshot stays silent", engine.Structured, "bypass", false},
		{"plan + interactive stays silent", engine.Interactive, "plan", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			st := &runState{launch: launch.Launch{Mode: tc.mode}, permMode: tc.permMode, backendName: "mock"}
			st.warnPosture()

			if tc.wantWarn {
				assert.Contains(t, buf.String(), "--one-shot with "+tc.permMode+" permissions has no human to approve a gated call")
			} else {
				assert.Empty(t, buf.String(), "must stay silent for %s", tc.name)
			}
		})
	}
}

// A flag the run could not honour as typed is named against what it runs at.
func TestWarnPosture_FlagNotHonoured(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	prev := runPermissions
	runPermissions = "bypass"
	defer func() { runPermissions = prev }()
	(&runState{launch: launch.Launch{Mode: engine.Interactive}, permMode: "plan", backendName: "mock"}).warnPosture()
	assert.Contains(t, buf.String(), `--permissions "bypass" cannot be honoured as asked on mock; this run uses "plan"`)
}
