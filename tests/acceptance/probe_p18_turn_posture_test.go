package acceptance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The P18 verdicts, exercised without an engine.

const p18TestSession = "5f0c1d2e-0000-4000-8000-00000000p18"

// p18TestStream renders one turn: an init frame naming its session and mode,
// then a result frame.
func p18TestStream(t *testing.T, session, mode string) string {
	t.Helper()
	return strings.Join([]string{
		p12Line(t, map[string]any{"type": "system", "subtype": "init", "session_id": session, "permissionMode": mode}),
		p12Line(t, map[string]any{"type": "result", "subtype": "success", "session_id": session, "permission_denials": []p12Denial{}}),
	}, "\n")
}

// p18TestAsk is a PermissionRequest hook input for a Write, offering the
// accept-edits mode change claude suggests on an edit.
func p18TestAsk(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": p12HookEvent, "tool_name": "Write",
		"tool_input":             map[string]string{"file_path": "/r/" + p18First.Name, "content": p18First.Content},
		"permission_suggestions": []map[string]string{{"type": "setMode", "mode": p18AcceptEdits, "destination": "session"}},
	})
	require.NoError(t, err)
	return b
}

// p18Green is each variant's green outcome: both turns ran in the modes their
// settings named, on one session, and the files and asks are as the variant
// requires.
func p18Green(t *testing.T, v p18Variant) p18Outcome {
	t.Helper()
	specs := v.turns("/r")
	o := p18Outcome{Cell: probeCellID{Probe: probeP18, Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: string(v)}, Variant: v}
	for i, s := range specs {
		o.Turns[i] = p18TurnRun{Started: true, Run: probeRun{Stdout: p18TestStream(t, p18TestSession, s.Mode)}, Landed: map[string]bool{}}
	}
	switch v {
	case p18PlanResumed:
		o.Turns[0].Landed[p18First.Name] = true
		o.Turns[1].Landed[p18First.Name] = true
	case p18PlanApproved:
		o.Turns[0].Plans = 1
		o.Turns[1].Plans = 1
		o.Turns[1].Landed[p18First.Name] = true
	case p18SetModeHeld:
		o.Turns[0].Asks = [][]byte{p18TestAsk(t)}
		o.Turns[0].Landed[p18First.Name] = true
		o.Turns[1].Landed[p18First.Name] = true
		o.Turns[1].Landed[p18Second.Name] = true
	}
	return o
}

func TestP18_Verdict(t *testing.T) {
	for _, v := range p18Variants {
		t.Run(string(v)+" green", func(t *testing.T) {
			require.NoError(t, p18Assert(p18Green(t, v)))
		})
		t.Run(string(v)+" a turn in another mode than its settings named is POSTURE-NOT-APPLIED", func(t *testing.T) {
			o := p18Green(t, v)
			o.Turns[1].Run.Stdout = p18TestStream(t, p18TestSession, "default")
			if v.turns("/r")[1].Mode == "default" {
				o.Turns[1].Run.Stdout = p18TestStream(t, p18TestSession, p18Plan)
			}
			p12RequireShape(t, p18Assert(o), shapePostureNotApplied)
		})
		t.Run(string(v)+" a second turn on another session is NOT-RESUMED", func(t *testing.T) {
			o := p18Green(t, v)
			o.Turns[1].Run.Stdout = p18TestStream(t, "another-session", v.turns("/r")[1].Mode)
			p12RequireShape(t, p18Assert(o), shapeNotResumed)
		})
		t.Run(string(v)+" no init frame is OUTPUT-FORMAT", func(t *testing.T) {
			o := p18Green(t, v)
			o.Turns[0].Run.Stdout = p12Line(t, map[string]any{"type": "result", "subtype": "success"})
			p12RequireShape(t, p18Assert(o), shapeOutputFormat)
		})
		t.Run(string(v)+" a timed-out turn is RUN", func(t *testing.T) {
			o := p18Green(t, v)
			o.Turns[1].TimedOut = true
			p12RequireShape(t, p18Assert(o), shapeRunFailed)
		})
	}

	t.Run("plan-resumed: the control turn writing nothing is NOT-ATTEMPTED", func(t *testing.T) {
		o := p18Green(t, p18PlanResumed)
		o.Turns[0].Landed[p18First.Name] = false
		p12RequireShape(t, p18Assert(o), shapeNotAttempted)
	})
	t.Run("plan-resumed: the plan turn's write landing is POSTURE-NOT-HELD", func(t *testing.T) {
		o := p18Green(t, p18PlanResumed)
		o.Turns[1].Landed[p18Second.Name] = true
		p12RequireShape(t, p18Assert(o), shapePostureNotHeld)
	})

	t.Run("plan-approved: the planned file landing in the plan turn is POSTURE-NOT-HELD", func(t *testing.T) {
		o := p18Green(t, p18PlanApproved)
		o.Turns[0].Landed[p18First.Name] = true
		p12RequireShape(t, p18Assert(o), shapePostureNotHeld)
	})
	t.Run("plan-approved: no native plan after the plan turn is NO-NATIVE-PLAN", func(t *testing.T) {
		o := p18Green(t, p18PlanApproved)
		o.Turns[0].Plans = 0
		p12RequireShape(t, p18Assert(o), shapeNoNativePlan)
	})
	t.Run("plan-approved: the approved turn not carrying the plan out is APPROVED-WORK-MISSING", func(t *testing.T) {
		o := p18Green(t, p18PlanApproved)
		o.Turns[1].Landed[p18First.Name] = false
		p12RequireShape(t, p18Assert(o), shapeApprovedWorkMissing)
	})

	t.Run("setmode-held: no ask in the default turn is HOOK-NOT-FIRED", func(t *testing.T) {
		o := p18Green(t, p18SetModeHeld)
		o.Turns[0].Asks = nil
		p12RequireShape(t, p18Assert(o), shapeHookNotFired)
	})
	t.Run("setmode-held: an ask offering no accept-edits change is NO-SETMODE-OFFER", func(t *testing.T) {
		o := p18Green(t, p18SetModeHeld)
		raw, err := json.Marshal(map[string]any{"hook_event_name": p12HookEvent, "tool_name": "Write", "tool_input": map[string]string{}})
		require.NoError(t, err)
		o.Turns[0].Asks = [][]byte{raw}
		p12RequireShape(t, p18Assert(o), shapeNoSetModeOffer)
	})
	t.Run("setmode-held: the allowed write not landing is DECISION-IGNORED", func(t *testing.T) {
		o := p18Green(t, p18SetModeHeld)
		o.Turns[0].Landed[p18First.Name] = false
		p12RequireShape(t, p18Assert(o), shapeDecisionIgnored)
	})
	t.Run("setmode-held: the held turn asking again is POSTURE-NOT-HELD", func(t *testing.T) {
		o := p18Green(t, p18SetModeHeld)
		o.Turns[1].Asks = [][]byte{p18TestAsk(t)}
		p12RequireShape(t, p18Assert(o), shapePostureNotHeld)
	})
	t.Run("setmode-held: the held turn's write not landing is POSTURE-NOT-HELD", func(t *testing.T) {
		o := p18Green(t, p18SetModeHeld)
		o.Turns[1].Landed[p18Second.Name] = false
		p12RequireShape(t, p18Assert(o), shapePostureNotHeld)
	})
}

// TestP18_FixtureWire: each turn's inline --settings names exactly its mode,
// the second turn resumes the first's session, and the setmode-held hook
// prints production's own allow carrying a session accept-edits change.
func TestP18_FixtureWire(t *testing.T) {
	for _, v := range p18Variants {
		specs := v.turns("/r")
		first, second := p18Args(specs[0], ""), p18Args(specs[1], p18TestSession)
		assert.NotContains(t, first, "--resume", v)
		i := slices.Index(second, "--resume")
		require.GreaterOrEqual(t, i, 0, v)
		assert.Equal(t, p18TestSession, second[i+1], v)
		for k, args := range [][]string{first, second} {
			j := slices.Index(args, "--settings")
			require.GreaterOrEqual(t, j, 0, v)
			var doc p17Settings
			require.NoError(t, json.Unmarshal([]byte(args[j+1]), &doc), v)
			assert.Equal(t, specs[k].Mode, doc.Permissions.DefaultMode, v)
			assert.Empty(t, doc.Permissions.Allow, v)
			assert.Contains(t, args, liveClaudeModel, v)
		}
	}
	assert.Equal(t, []string{p18AcceptEdits, p18Plan}, []string{p18PlanResumed.turns("/r")[0].Mode, p18PlanResumed.turns("/r")[1].Mode})
	assert.Equal(t, []string{p18Plan, p18AcceptEdits}, []string{p18PlanApproved.turns("/r")[0].Mode, p18PlanApproved.turns("/r")[1].Mode})
	assert.Equal(t, []string{"default", p18AcceptEdits}, []string{p18SetModeHeld.turns("/r")[0].Mode, p18SetModeHeld.turns("/r")[1].Mode})

	for _, v := range []p18Variant{p18PlanResumed, p18PlanApproved} {
		ans, err := v.hookAnswer()
		require.NoError(t, err)
		assert.Empty(t, ans, "a plan variant's hook answers nothing, so any ask is refused")
	}
	ans, err := p18SetModeHeld.hookAnswer()
	require.NoError(t, err)
	var out struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior           string `json:"behavior"`
				UpdatedPermissions []struct {
					Type, Mode, Destination string
				} `json:"updatedPermissions"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(ans, &out))
	assert.Equal(t, p12HookEvent, out.HookSpecificOutput.HookEventName)
	assert.Equal(t, "allow", out.HookSpecificOutput.Decision.Behavior)
	require.Len(t, out.HookSpecificOutput.Decision.UpdatedPermissions, 1)
	assert.Equal(t, "setMode", out.HookSpecificOutput.Decision.UpdatedPermissions[0].Type)
	assert.Equal(t, p18AcceptEdits, out.HookSpecificOutput.Decision.UpdatedPermissions[0].Mode)
	assert.Equal(t, "session", out.HookSpecificOutput.Decision.UpdatedPermissions[0].Destination)

	script := p18HookScript("/d", ans)
	assert.Contains(t, script, p12ShellQuote(string(ans)))
	assert.Contains(t, script, p12ShellQuote("/d/"+p18AsksDir))
	assert.NotContains(t, p18HookScript("/d", nil), "printf")

	var home p17Settings
	raw, err := p18HomeSettingsJSON("/d/hook.sh")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &home))
	require.Len(t, home.Hooks[p12HookEvent], 1)
	assert.Empty(t, home.Hooks[p12HookEvent][0].Matcher, "every tool's ask reaches the hook, as production's approval hook is registered")
	assert.NotContains(t, string(raw), `"allow":null`)
}

// TestP12Decode_InitSessionAndMode: the shared decoder keeps the init frame's
// session id and permission mode, which every P18 verdict reads.
func TestP12Decode_InitSessionAndMode(t *testing.T) {
	s, err := p12Decode(p18TestStream(t, p18TestSession, p18Plan))
	require.NoError(t, err)
	assert.Equal(t, p18TestSession, s.SessionID)
	assert.Equal(t, p18Plan, s.PermissionMode)
}
