package acceptance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The P17 verdict, exercised without an engine.

// p17TestStream renders a turn whose gated calls printed outputs, one call
// per output, then a result frame.
func p17TestStream(t *testing.T, outputs ...string) string {
	t.Helper()
	var lines []string
	for i, out := range outputs {
		id := p12TestCall + string(rune('a'+i))
		lines = append(lines,
			p12Line(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
				{"type": "tool_use", "id": id, "name": p12GatedTool, "input": map[string]string{"command": "echo " + out}},
			}}}),
			p12Line(t, map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": false},
			}}}))
	}
	lines = append(lines, p12Line(t, map[string]any{"type": "result", "subtype": "success", "permission_denials": []p12Denial{}}))
	return strings.Join(lines, "\n")
}

func p17TestOutcome(stdout string) p17Outcome {
	fired := map[string]bool{}
	for event := range p13Markers {
		fired[event] = true
	}
	return p17Outcome{
		Cell:    probeCellID{Probe: probeP17, Engine: "claude-code", Runtime: "host", Workspace: "none"},
		Started: true,
		Run:     probeRun{Stdout: stdout},
		Fired:   fired,
	}
}

func TestP17_Verdict(t *testing.T) {
	t.Run("both allows applied and every home hook fired is green", func(t *testing.T) {
		require.NoError(t, p17Assert(p17TestOutcome(p17TestStream(t, p17InlineOutput, p17HomeOutput))))
	})
	t.Run("one compound call printing both lines is green", func(t *testing.T) {
		require.NoError(t, p17Assert(p17TestOutcome(p17TestStream(t, p17InlineOutput+"\n"+p17HomeOutput))))
	})
	t.Run("the inline allow not applying is INLINE-SETTINGS-IGNORED", func(t *testing.T) {
		p12RequireShape(t, p17Assert(p17TestOutcome(p17TestStream(t, p17HomeOutput))), shapeInlineSettingsIgnored)
	})
	t.Run("the home allow not applying is HOME-SETTINGS-DROPPED", func(t *testing.T) {
		p12RequireShape(t, p17Assert(p17TestOutcome(p17TestStream(t, p17InlineOutput))), shapeHomeSettingsDropped)
	})
	for event := range p13Markers {
		t.Run("a silent home "+event+" hook is HOME-SETTINGS-DROPPED", func(t *testing.T) {
			o := p17TestOutcome(p17TestStream(t, p17InlineOutput, p17HomeOutput))
			o.Fired[event] = false
			p12RequireShape(t, p17Assert(o), shapeHomeSettingsDropped)
		})
	}
	t.Run("no gated call is NOT-ATTEMPTED", func(t *testing.T) {
		p12RequireShape(t, p17Assert(p17TestOutcome(p17TestStream(t))), shapeNotAttempted)
	})
}

// TestP17_FixtureWire: the home settings carry the marker hooks and the home
// allow; the inline document carries only the keys ctxloom's per-turn
// posture document does, and the argv passes it as JSON, not as a path.
func TestP17_FixtureWire(t *testing.T) {
	var home p17Settings
	raw, err := p17HomeSettingsJSON("/x")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &home))
	assert.Equal(t, []string{p17HomeAllow}, home.Permissions.Allow)
	assert.Empty(t, home.Permissions.DefaultMode)
	assert.Equal(t, p13MarkerHooks("/x"), home.Hooks)

	var inline p17Settings
	require.NoError(t, json.Unmarshal([]byte(p17InlineSettings), &inline))
	assert.Equal(t, []string{p17InlineAllow}, inline.Permissions.Allow)
	assert.NotEmpty(t, inline.Permissions.DefaultMode)
	assert.Empty(t, inline.Hooks)

	args := p17Args()
	i := slices.Index(args, "--settings")
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, p17InlineSettings, args[i+1])
	j := slices.Index(args, "--setting-sources")
	require.GreaterOrEqual(t, j, 0)
	assert.Equal(t, "user", args[j+1])
}
