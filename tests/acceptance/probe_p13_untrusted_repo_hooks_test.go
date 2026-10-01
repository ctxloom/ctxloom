package acceptance

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The P13 verdicts, argv and fixture wire, exercised without an engine. Frames
// are shaped like the stream-json claude 2.1.286 emitted for this fixture.

const p13TestCall = "toolu_p13"

// p13TestStream renders a run: one Bash tool_use, its tool_result, and a
// result frame.
func p13TestStream(t *testing.T, text string, isError bool) string {
	t.Helper()
	use := map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": p13TestCall, "name": p12GatedTool, "input": map[string]string{"command": "echo hi"}},
	}}}
	res := map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "tool_use_id": p13TestCall, "content": text, "is_error": isError},
	}}}
	result := map[string]any{"type": "result", "subtype": "success"}
	return strings.Join([]string{p12Line(t, use), p12Line(t, res), p12Line(t, result)}, "\n")
}

func p13TestOutcome(v p13Variant, stdout string, fired map[string]bool) p13Outcome {
	return p13Outcome{
		Cell:    probeCellID{Probe: probeP13, Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: string(v)},
		Variant: v,
		Started: true,
		Run:     probeRun{Stdout: stdout},
		Fired:   fired,
	}
}

var (
	p13AllFired  = map[string]bool{p13PreToolEvent: true, p13SessionStartEvent: true}
	p13NoneFired = map[string]bool{}
	// The echo's output as claude 2.1.286 relayed it, shell startup noise first.
	p13Echoed = "/home/u/.zshenv:.:1: no such file or directory: /tmp/x/home/.cargo/env\nhi"
)

func TestP13_UntrustedFires(t *testing.T) {
	t.Run("both hooks fired and the echo ran is green", func(t *testing.T) {
		require.NoError(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, p13Echoed, false), p13AllFired)))
	})
	for event := range p13Markers {
		t.Run("a silent "+event+" hook is REPO-HOOK-SILENT", func(t *testing.T) {
			fired := map[string]bool{p13PreToolEvent: true, p13SessionStartEvent: true}
			fired[event] = false
			p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, p13Echoed, false), fired)), shapeRepoHookSilent)
		})
	}
}

func TestP13_SettingSourcesSuppresses(t *testing.T) {
	t.Run("no hook fired and the echo ran is green", func(t *testing.T) {
		require.NoError(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false), p13NoneFired)))
	})
	for event := range p13Markers {
		t.Run("a leaked "+event+" hook is REPO-HOOK-LEAKED", func(t *testing.T) {
			p12RequireShape(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false), map[string]bool{event: true})), shapeRepoHookLeaked)
		})
	}
	t.Run("no hook fired but the echo never ran is ECHO-NOT-RUN, not green", func(t *testing.T) {
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Suppresses, p13TestStream(t, "Permission denied", true), p13NoneFired)), shapeEchoNotRun)
	})
}

func TestP13_CommonHalf(t *testing.T) {
	t.Run("output that only CONTAINS the letters is not the echo", func(t *testing.T) {
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, p13TestStream(t, "this is not it", false), p13AllFired)), shapeEchoNotRun)
	})
	t.Run("no Bash call is NOT-ATTEMPTED", func(t *testing.T) {
		stream := p12Line(t, map[string]any{"type": "result", "subtype": "success"})
		p12RequireShape(t, p13Assert(p13TestOutcome(p13Fires, stream, p13AllFired)), shapeNotAttempted)
	})
	t.Run("a timed-out run is a RUN failure", func(t *testing.T) {
		o := p13TestOutcome(p13Fires, "", p13AllFired)
		o.TimedOut = true
		p12RequireShape(t, p13Assert(o), shapeRunFailed)
	})
	t.Run("an unreadable marker is a RUN failure, never a verdict on the hooks", func(t *testing.T) {
		o := p13TestOutcome(p13Suppresses, p13TestStream(t, p13Echoed, false), p13NoneFired)
		o.MarkerErr = errors.New("permission denied")
		p12RequireShape(t, p13Assert(o), shapeRunFailed)
	})
}

// TestP13_Args: the suppressing arm differs from the firing arm by EXACTLY the
// flags ctxloom's repo trust launches an untrusted repo with, and both carry
// the flag-scope allow, the prompt and the pinned model.
func TestP13_Args(t *testing.T) {
	fires, supp := p13Args(p13Fires), p13Args(p13Suppresses)
	require.Equal(t, append(slices.Clone(fires), "--setting-sources", "user", "--strict-mcp-config"), supp)
	for _, want := range [][]string{{"-p", p13Prompt}, {"--settings", p13FlagSettings}, {"--model", liveClaudeModel}} {
		i := slices.Index(fires, want[0])
		require.GreaterOrEqual(t, i, 0, "%s missing", want[0])
		require.Equal(t, want[1], fires[i+1])
	}
	require.NotContains(t, fires, "--setting-sources")
}

// TestP13_FixtureWire: the committed settings register one command hook per
// p13Markers event, each touching its marker under the cell dir (outside the
// repo), with the PreToolUse hook matched on the gated tool.
func TestP13_FixtureWire(t *testing.T) {
	raw, err := p13RepoSettingsJSON("/cell")
	require.NoError(t, err)
	var got p12Settings
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Hooks, len(p13Markers))
	for event, marker := range p13Markers {
		ms := got.Hooks[event]
		require.Len(t, ms, 1, event)
		require.Len(t, ms[0].Hooks, 1, event)
		require.Equal(t, "command", ms[0].Hooks[0].Type)
		require.Equal(t, "touch "+p12ShellQuote("/cell/"+marker), ms[0].Hooks[0].Command)
		if event == p13PreToolEvent {
			require.Equal(t, p12GatedTool, ms[0].Matcher)
		}
	}
	var flag map[string]any
	require.NoError(t, json.Unmarshal([]byte(p13FlagSettings), &flag), "the flag-scope settings must be valid JSON")
}
