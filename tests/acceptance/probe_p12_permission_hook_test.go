package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// The P12 verdicts, exercised without an engine. Frames are shaped like the
// stream-json claude 2.1.286 emitted for this fixture; every value the
// verdict compares is taken from the rung's own constants.

const p12TestCall = "toolu_p12"

var p12TestMarker = time.Date(2026, 10, 1, 13, 57, 22, 79_000_000, time.UTC)

func p12Line(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// p12TestStream renders a run: the gated tool_use, its tool_result at `at`,
// and a result frame carrying denials.
func p12TestStream(t *testing.T, text string, isError bool, at time.Time, denials []p12Denial) string {
	t.Helper()
	use := map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": p12TestCall, "name": p12GatedTool, "input": map[string]string{"command": "touch x"}},
	}}}
	res := map[string]any{"type": "user", "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "tool_use_id": p12TestCall, "content": text, "is_error": isError},
	}}}
	if denials == nil {
		denials = []p12Denial{}
	}
	result := map[string]any{"type": "result", "subtype": "success", "permission_denials": denials}
	return strings.Join([]string{p12Line(t, use), p12Line(t, res), p12Line(t, result)}, "\n")
}

func p12TestOutcome(t *testing.T, d p12Decision, stdout string, proof bool) p12Outcome {
	t.Helper()
	return p12Outcome{
		Cell:        probeCellID{Probe: probeP12, Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: string(d)},
		Decision:    d,
		Started:     true,
		Run:         probeRun{Stdout: stdout},
		HookInput:   []byte(p12Line(t, p12HookInput{HookEventName: p12HookEvent, ToolName: p12GatedTool})),
		MarkerAt:    p12TestMarker,
		ProofExists: proof,
	}
}

func p12RequireShape(t *testing.T, err error, want probeShape) {
	t.Helper()
	require.Error(t, err)
	got, ok := probeShapeOf(err)
	require.True(t, ok, "not a probe verdict: %v", err)
	require.Equal(t, want, got, "%v", err)
}

func TestP12_Allow(t *testing.T) {
	after := p12TestMarker.Add(30 * time.Millisecond)
	denied := []p12Denial{{ToolName: p12GatedTool, ToolUseID: p12TestCall}}

	t.Run("awaited, allowed and run is green", func(t *testing.T) {
		require.NoError(t, p12Assert(p12TestOutcome(t, p12Allow, p12TestStream(t, "", false, after, nil), true)))
	})
	t.Run("a tool_result before the hook answered is NOT-AWAITED", func(t *testing.T) {
		early := p12TestMarker.Add(-2 * time.Second)
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, p12TestStream(t, p12DeniedByHook, true, early, denied), false)), shapeNotAwaited)
	})
	t.Run("a refused call is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, p12TestStream(t, p12DeniedByHook, true, after, denied), false)), shapeDecisionIgnored)
	})
	t.Run("a missing proof file is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, p12TestStream(t, "", false, after, nil), false)), shapeDecisionIgnored)
	})
}

func TestP12_Deny(t *testing.T) {
	after := p12TestMarker.Add(30 * time.Millisecond)
	denied := []p12Denial{{ToolName: p12GatedTool, ToolUseID: p12TestCall}}

	t.Run("blocked, reported and absent is green", func(t *testing.T) {
		require.NoError(t, p12Assert(p12TestOutcome(t, p12Deny, p12TestStream(t, p12DeniedByHook, true, after, denied), false)))
	})
	t.Run("no permission_denials entry is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Deny, p12TestStream(t, p12DeniedByHook, true, after, nil), false)), shapeDecisionIgnored)
	})
	t.Run("a different tool_result is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Deny, p12TestStream(t, "", false, after, denied), false)), shapeDecisionIgnored)
	})
	t.Run("a proof file that exists is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Deny, p12TestStream(t, p12DeniedByHook, true, after, denied), true)), shapeDecisionIgnored)
	})
}

func TestP12_Silent(t *testing.T) {
	after := p12TestMarker.Add(30 * time.Millisecond)

	t.Run("refused and absent is green", func(t *testing.T) {
		require.NoError(t, p12Assert(p12TestOutcome(t, p12Silent, p12TestStream(t, "denied", true, after, nil), false)))
	})
	t.Run("a call that ran is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Silent, p12TestStream(t, "", false, after, nil), true)), shapeDecisionIgnored)
	})
	t.Run("an unrefused call is DECISION-IGNORED even with no file to show for it", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Silent, p12TestStream(t, "", false, after, nil), false)), shapeDecisionIgnored)
	})
	t.Run("a refused call whose file exists anyway is DECISION-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Silent, p12TestStream(t, "denied", true, after, nil), true)), shapeDecisionIgnored)
	})
}

// TestP12_HookScripts: the silent hook prints nothing at all; the
// prompts-none hook answers allow and the cell adds the flag.
func TestP12_HookScripts(t *testing.T) {
	silent, err := p12HookScript("/d", p12Silent)
	require.NoError(t, err)
	assert.NotContains(t, silent, "printf", "a silent hook writes no decision")
	none, err := p12HookScript("/d", p12AllowPromptsNone)
	require.NoError(t, err)
	assert.Contains(t, none, `"behavior":"allow"`)
	assert.Equal(t, []string{"--permission-prompts", "none"}, p12AllowPromptsNone.argv())
	assert.Empty(t, p12Allow.argv())
}

func TestP12_CommonHalf(t *testing.T) {
	after := p12TestMarker.Add(30 * time.Millisecond)
	green := p12TestStream(t, "", false, after, nil)

	t.Run("no gated tool_use is NOT-ATTEMPTED", func(t *testing.T) {
		stdout := p12Line(t, map[string]any{"type": "result", "permission_denials": []p12Denial{}})
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, stdout, true)), shapeNotAttempted)
	})
	t.Run("no hook input is HOOK-NOT-FIRED", func(t *testing.T) {
		o := p12TestOutcome(t, p12Allow, green, true)
		o.HookInput, o.HookInputErr = nil, fs.ErrNotExist
		p12RequireShape(t, p12Assert(o), shapeHookNotFired)
	})
	t.Run("a hook input for another event is HOOK-NOT-FIRED", func(t *testing.T) {
		o := p12TestOutcome(t, p12Allow, green, true)
		o.HookInput = []byte(p12Line(t, p12HookInput{HookEventName: claude.HookEventSessionStart, ToolName: p12GatedTool}))
		p12RequireShape(t, p12Assert(o), shapeHookNotFired)
	})
	t.Run("no marker is HOOK-NOT-FIRED", func(t *testing.T) {
		o := p12TestOutcome(t, p12Allow, green, true)
		o.MarkerAt, o.MarkerErr = time.Time{}, fs.ErrNotExist
		p12RequireShape(t, p12Assert(o), shapeHookNotFired)
	})
	t.Run("a turn with no result frame is OUTPUT-FORMAT", func(t *testing.T) {
		var frames []string
		for _, line := range strings.Split(green, "\n") {
			var f p12Frame
			require.NoError(t, json.Unmarshal([]byte(line), &f))
			if f.Type != "result" {
				frames = append(frames, line)
			}
		}
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, strings.Join(frames, "\n"), true)), shapeOutputFormat)
	})
	t.Run("a non-frame stdout line is OUTPUT-FORMAT", func(t *testing.T) {
		p12RequireShape(t, p12Assert(p12TestOutcome(t, p12Allow, green+"\nnot json", true)), shapeOutputFormat)
	})
	t.Run("a timed-out run is RUN", func(t *testing.T) {
		o := p12TestOutcome(t, p12Allow, green, true)
		o.TimedOut, o.Run.Err = true, errors.New("killed")
		p12RequireShape(t, p12Assert(o), shapeRunFailed)
	})
}

// TestP12_FixtureWire pins the two documents the fixture hands claude to the
// shapes the hooks page documents, decoded rather than string-compared.
func TestP12_FixtureWire(t *testing.T) {
	raw, err := p12SettingsJSON("/x/hook.sh")
	require.NoError(t, err)
	var settings p12Settings
	require.NoError(t, json.Unmarshal(raw, &settings))
	require.Equal(t, []p12HookMatcher{{Matcher: p12GatedTool, Hooks: []p12HookCommand{{Type: "command", Command: "/x/hook.sh"}}}}, settings.Hooks[p12HookEvent])

	for _, d := range []p12Decision{p12Allow, p12Deny} {
		script, err := p12HookScript("/x", d)
		require.NoError(t, err)
		want, err := json.Marshal(p12HookOutput{HookSpecificOutput: p12HookSpecific{HookEventName: p12HookEvent, Decision: p12HookDecision{Behavior: d}}})
		require.NoError(t, err)
		require.Contains(t, script, p12ShellQuote(string(want)))
		require.Contains(t, script, fmt.Sprintf("sleep %d", int(p12HookDelay/time.Second)))
	}
}
