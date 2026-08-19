package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestResolvePermissionMode pins the top-level run's permission-posture resolver:
// precedence (--permissions flag > agent binding > engine-label config > built-in
// default), the claude-code host-bypass default (the container-isolation stopgap,
// taskloom hilly-crop), the plan collapse on backends that can't enforce read-only,
// and the headless-ONESHOT floor that upgrades a would-block posture to bypass so a
// run with no human can't hang.
func TestResolvePermissionMode(t *testing.T) {
	const claude = config.DefaultLLM // "claude-code"
	cases := []struct {
		name         string
		flag         string
		agentPerm    string
		labelPerm    string
		projectPerm  string
		backend      string
		mode         pb.ExecutionMode
		enforcesPlan bool
		want         agent.PermissionMode
	}{
		{"flag beats agent, label, and default", "plan", "bypass", "default", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		{"agent beats label and default", "", "acceptEdits", "bypass", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionAcceptEdits},
		{"label beats default", "", "", "plan", "", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
		{"claude-code default bypasses on the host", "", "", "", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionBypass},
		// The opt-out path: an EXPLICIT default must beat claude's host-bypass
		// default (ParsePermissionMode distinguishes explicit "default" from unset).
		{"explicit default beats claude host-bypass", "default", "", "", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionDefault},
		{"explicit default via label beats claude host-bypass", "", "", "default", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionDefault},
		{"other backend default prompts", "", "", "", "", "codex", pb.ExecutionMode_INTERACTIVE, true, agent.PermissionDefault},
		// An unparseable config-sourced value (agent/label) is a declaration that
		// MISSED, not an absent one: it stops the chain at the most restrictive
		// posture instead of falling through to the claude-code host stopgap. The
		// typed --permissions flag is rejected up front instead (see
		// TestValidatePermissionFlag), so it never reaches here unparseable.
		{"unparseable agent value floors to read-only, never the claude stopgap", "", "nonsense", "", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionFloor},
		{"unparseable value does not defer to a wider source below it", "", "nonsense", "bypass", "bypass", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionFloor},
		// The floor survives BOTH widening steps below it: the plan collapse on a
		// backend with no read-only tier, and the ONESHOT floor that follows it.
		// Letting either apply walks a typo back up to bypass.
		{"unparseable value is not collapsed on a non-enforcing backend", "", "nonsense", "", "", "antigravity", pb.ExecutionMode_INTERACTIVE, false, agent.PermissionFloor},
		{"unparseable value is not widened by the oneshot floor", "", "nonsense", "", "", "antigravity", pb.ExecutionMode_ONESHOT, false, agent.PermissionFloor},
		{"oneshot upgrades a would-block default to bypass", "default", "", "", "", "codex", pb.ExecutionMode_ONESHOT, true, agent.PermissionBypass},
		{"oneshot keeps safe-headless plan on an enforcing backend", "plan", "", "", "", "codex", pb.ExecutionMode_ONESHOT, true, agent.PermissionPlan},
		{"oneshot upgrades acceptEdits to bypass", "acceptEdits", "", "", "", claude, pb.ExecutionMode_ONESHOT, true, agent.PermissionBypass},
		// Fix A: a backend with no read-only tier can't honor plan. Interactively it
		// collapses to default (a human still gates each tool call — no silent
		// read-write); headless it then upgrades to bypass so it can't hang.
		{"plan on a non-enforcing backend collapses to prompt", "plan", "", "", "", "antigravity", pb.ExecutionMode_INTERACTIVE, false, agent.PermissionDefault},
		{"headless plan on a non-enforcing backend upgrades to bypass", "plan", "", "", "", "antigravity", pb.ExecutionMode_ONESHOT, false, agent.PermissionBypass},
		{"plan on an enforcing backend stays plan interactively", "plan", "", "", "", claude, pb.ExecutionMode_INTERACTIVE, true, agent.PermissionPlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePermissionMode(tc.flag, tc.agentPerm, tc.labelPerm, tc.projectPerm, tc.backend, tc.mode, tc.enforcesPlan)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRequestedPermission verifies the resolver surfaces the posture the user or
// config asked for (first parseable of flag > agent > label), independent of any
// backend collapse — the input the run uses to warn when a backend can't honor it.
func TestRequestedPermission(t *testing.T) {
	m, ok := requestedPermission("plan", "bypass", "default", "")
	assert.True(t, ok)
	assert.Equal(t, agent.PermissionPlan, m)

	m, ok = requestedPermission("", "", "bypass", "")
	assert.True(t, ok)
	assert.Equal(t, agent.PermissionBypass, m)

	_, ok = requestedPermission("", "", "", "")
	assert.False(t, ok, "nothing requested")

	_, ok = requestedPermission("nonsense", "", "", "")
	assert.False(t, ok, "an unparseable value is not a request")
}

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

// TestWarnPlanOneshotCancels pins the invariant: after resolvePermissionMode's
// ONESHOT floor, plan is the only SafeHeadless posture that still gates a
// mutating call on a human (bypass never asks; default/acceptEdits already
// floored up to bypass by this point) — so a --one-shot run that resolved to
// plan must warn loudly at startup that no human is reachable, since the engine
// cancels any gated call outright rather than
// hanging or running it. bypass+ONESHOT (nothing gates) and plan+INTERACTIVE (a
// human IS reachable) must both stay silent.
func TestWarnPlanOneshotCancels(t *testing.T) {
	cases := []struct {
		name     string
		mode     pb.ExecutionMode
		permMode agent.PermissionMode
		wantWarn bool
	}{
		{"plan + oneshot warns", pb.ExecutionMode_ONESHOT, agent.PermissionPlan, true},
		{"bypass + oneshot stays silent", pb.ExecutionMode_ONESHOT, agent.PermissionBypass, false},
		{"plan + interactive stays silent", pb.ExecutionMode_INTERACTIVE, agent.PermissionPlan, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			st := &runState{mode: tc.mode, permMode: tc.permMode}
			st.warnPlanOneshotCancels()

			if tc.wantWarn {
				assert.Contains(t, buf.String(), "--one-shot with plan permissions has no human to approve a gated call")
			} else {
				assert.Empty(t, buf.String(), "must stay silent for %s", tc.name)
			}
		})
	}
}
