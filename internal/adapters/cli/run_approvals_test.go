package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
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

// TestPostureWarning: the startup warning is computed from the RESOLVED
// policy and from whether anything presents approvals, never from the mode
// alone. A one-shot whose approver is none, or the human with nothing to
// present to, has its gated calls denied at once and says so — the latter
// naming its children, whose requests park in its queue. The posture the
// engine's own model says asks nobody (engine.PermissionModel.Ungated) is
// silent whatever the engine spells it — claude's bypassPermissions, the
// mock's bypass — and another engine's spelling is not; a reviewer decides on
// its own, and an interactive root's own asks are answered in the engine's
// UI: those are silent too.
//
// MUTATION: claude's Ungated answers "bypass" → the bypassPermissions rows go
// red.
func TestPostureWarning(t *testing.T) {
	human := engine.PermissionPolicy{Approver: engine.ApproverHuman}
	none := engine.PermissionPolicy{Approver: engine.ApproverNone}
	reviewer := engine.PermissionPolicy{Approver: engine.ApproverReviewer}
	reg := engines.Registry()
	claude, mock := permissionModelOf(reg, "claude-code"), permissionModelOf(reg, "mock")
	const deniedNone = "--one-shot with %s permissions and approver none: gated calls are denied at once, so those steps will not run"
	for _, tc := range []struct {
		name      string
		mode      engine.Mode
		policy    engine.PermissionPolicy
		model     engine.Declared[engine.PermissionModel]
		posture   string
		presented bool
		want      string
	}{
		{"one-shot, approver none", engine.Structured, none, claude, "acceptEdits", false, fmt.Sprintf(deniedNone, "acceptEdits")},
		{"one-shot, the human with nothing to present to", engine.Structured, human, claude, "plan", false,
			"--one-shot with plan permissions: no one can answer approvals in this run, so gated calls are denied at once — this run's, and those any delegated child puts to a human; set a permissions mode or a reviewer approver"},
		{"one-shot, a reviewer decides on its own", engine.Structured, reviewer, claude, "default", false, ""},
		{"one-shot, claude's bypassPermissions asks nobody", engine.Structured, human, claude, "bypassPermissions", false, ""},
		{"one-shot, claude's bypassPermissions with approver none", engine.Structured, none, claude, "bypassPermissions", false, ""},
		{"one-shot, the mock's bypass asks nobody", engine.Structured, none, mock, "bypass", false, ""},
		{"one-shot, the mock's spelling on claude asks as declared", engine.Structured, none, claude, "bypass", false, fmt.Sprintf(deniedNone, "bypass")},
		{"one-shot, an engine with no ungated posture", engine.Structured, none, noUngatedModel(claude), "bypassPermissions", false, fmt.Sprintf(deniedNone, "bypassPermissions")},
		{"one-shot, no permission model", engine.Structured, none, permissionModelOf(reg, "no-such-engine"), "bypassPermissions", false, fmt.Sprintf(deniedNone, "bypassPermissions")},
		{"structured, the human presented to", engine.Structured, human, claude, "acceptEdits", true, ""},
		{"interactive, presented", engine.Interactive, human, claude, "acceptEdits", true, ""},
		{"interactive, plain terminal: the engine's UI asks the root's own", engine.Interactive, human, claude, "acceptEdits", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, postureWarning(tc.mode, tc.policy, tc.model, tc.posture, tc.presented))
		})
	}
}

// TestWarnPosture_PrintsTheResolvedWarning: a one-shot run prints
// postureWarning's line for its own launch, and nothing where it is silent.
func TestWarnPosture_PrintsTheResolvedWarning(t *testing.T) {
	for name, tc := range map[string]struct {
		approver engine.Approver
		want     string
	}{
		"human":    {engine.ApproverHuman, "ctxloom: warning: --one-shot with plan permissions: no one can answer approvals in this run"},
		"reviewer": {engine.ApproverReviewer, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()
			st := &runState{
				cfg:         config.NewFixture(config.Fixture{}),
				launch:      launch.Launch{Mode: engine.Structured, Permission: engine.PermissionPolicy{Approver: tc.approver}},
				permMode:    "plan",
				backendName: "mock",
			}
			st.warnPosture()
			if tc.want == "" {
				assert.Empty(t, buf.String())
				return
			}
			assert.Contains(t, buf.String(), tc.want)
		})
	}
}

// TestPresentsApprovals: the root presents approvals only where the terminal
// layer is built — an interactive launch, without --plain-terminal, on a
// terminal, with a prefix key it can parse.
func TestPresentsApprovals(t *testing.T) {
	badKey := errors.New("bad prefix key")
	for _, tc := range []struct {
		name          string
		mode          engine.Mode
		plain, isTerm bool
		prefixErr     error
		want          bool
	}{
		{"interactive on a terminal presents", engine.Interactive, false, true, nil, true},
		{"one-shot", engine.Structured, false, true, nil, false},
		{"plain terminal", engine.Interactive, true, true, nil, false},
		{"no terminal", engine.Interactive, false, false, nil, false},
		{"no terminal viewer", engine.Interactive, false, true, badKey, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, presentsApprovals(tc.mode, tc.plain, tc.isTerm, tc.prefixErr))
		})
	}
}

// TestUnpresentedApprovalFinding: a root that will not present approvals and
// may delegate is told, at startup, that its children's gated calls are
// denied at once, naming the agents and the remedy. A root that presents
// them, or has nobody to delegate to, is told nothing.
func TestUnpresentedApprovalFinding(t *testing.T) {
	f, ok := unpresentedApprovalFinding(false, []string{"coder", "finder"})
	require.True(t, ok)
	assert.Equal(t, "no one can answer approvals in this run, so a delegated child's gated calls are denied at once rather than asked (agents this run may delegate to: coder, finder)", f.Text)
	assert.Contains(t, f.Remedy, "ctxloom agent edit <agent> --permissions <posture>")
	assert.Contains(t, f.Remedy, "approver: reviewer")
	assert.False(t, f.Fatal(), "advisory: the run still launches")

	_, ok = unpresentedApprovalFinding(true, []string{"coder"})
	assert.False(t, ok, "a root that presents approvals")
	_, ok = unpresentedApprovalFinding(false, nil)
	assert.False(t, ok, "a root with nobody to delegate to")
}

// TestWarnPosture_ReportsTheChildrenFinding: a root that will not present
// approvals — here an interactive one under --plain-terminal, whose own asks
// the engine's UI answers, so it gets no posture warning — still reports the
// finding with its remedy, listing the agents a binding with no may_delegate
// may launch: every configured one.
func TestWarnPosture_ReportsTheChildrenFinding(t *testing.T) {
	saved := []string{runAgent, runProfile}
	savedPlain := runPlainTerminal
	t.Cleanup(func() { runAgent, runProfile, runPlainTerminal = saved[0], saved[1], savedPlain })
	runAgent, runProfile, runPlainTerminal = "lead", "", true
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	st := &runState{
		cfg:      config.NewFixture(config.Fixture{Agents: map[string]agents.Agent{"lead": {}, "coder": {}}}),
		launch:   launch.Launch{Mode: engine.Interactive},
		permMode: "acceptEdits",
	}
	st.warnPosture()
	assert.Contains(t, buf.String(), "no one can answer approvals in this run, so a delegated child's gated calls are denied at once rather than asked (agents this run may delegate to: coder, lead)")
	assert.Contains(t, buf.String(), "approver: reviewer")
	assert.NotContains(t, buf.String(), "--one-shot", "the interactive root's own asks are the engine UI's")
}

// A flag the run could not honour as typed is named against what it runs at.
func TestWarnPosture_FlagNotHonoured(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	prev := runPermissions
	runPermissions = "bypass"
	defer func() { runPermissions = prev }()
	(&runState{cfg: config.NewFixture(config.Fixture{}), launch: launch.Launch{Mode: engine.Interactive}, permMode: "plan", backendName: "mock"}).warnPosture()
	assert.Contains(t, buf.String(), `--permissions "bypass" cannot be honoured as asked on mock; this run uses "plan"`)
}
