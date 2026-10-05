package acceptance

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The P15 verdicts, exercised without an engine.

func p15TestOutcome(t *testing.T, stdout string) p15Outcome {
	t.Helper()
	return p15Outcome{
		Cell:              probeCellID{Probe: probeP15, Engine: "claude-code", Runtime: "host", Workspace: "none"},
		Started:           true,
		Run:               probeRun{Stdout: stdout, ExitCode: -1, Err: errors.New("signal: interrupt")},
		HookPID:           4242,
		ExitedOnInterrupt: true,
		ExitAfter:         300 * time.Millisecond,
	}
}

// p15TestStream is a turn cut short at the gated call: the tool_use, and
// optionally the result frame claude may write on its way out.
func p15TestStream(t *testing.T, withResult bool) string {
	t.Helper()
	lines := []string{p12Line(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": p12TestCall, "name": p12GatedTool, "input": map[string]string{"command": "touch x"}},
	}}})}
	if withResult {
		lines = append(lines, p12Line(t, map[string]any{"type": "result", "subtype": "error_during_execution"}))
	}
	return strings.Join(lines, "\n")
}

func TestP15_Verdict(t *testing.T) {
	t.Run("a hook that died with the engine is green, with or without a result frame", func(t *testing.T) {
		require.NoError(t, p15Assert(p15TestOutcome(t, p15TestStream(t, true))))
		require.NoError(t, p15Assert(p15TestOutcome(t, p15TestStream(t, false))))
	})
	t.Run("a hook still alive after the engine exited is HOOK-ORPHANED", func(t *testing.T) {
		o := p15TestOutcome(t, p15TestStream(t, true))
		o.HookAlive = true
		p12RequireShape(t, p15Assert(o), shapeHookOrphaned)
	})
	t.Run("an engine that outlived the grace is INTERRUPT-IGNORED", func(t *testing.T) {
		o := p15TestOutcome(t, p15TestStream(t, false))
		o.ExitedOnInterrupt = false
		p12RequireShape(t, p15Assert(o), shapeInterruptIgnored)
	})
	t.Run("a hook that never started is HOOK-NOT-FIRED when the call was made", func(t *testing.T) {
		o := p15TestOutcome(t, p15TestStream(t, false))
		o.HookPID, o.HookStartErr = 0, errP15HookNeverBlocked
		p12RequireShape(t, p15Assert(o), shapeHookNotFired)
	})
	t.Run("a hook that never started is NOT-ATTEMPTED when no call was made", func(t *testing.T) {
		o := p15TestOutcome(t, p12Line(t, map[string]any{"type": "result", "subtype": "success"}))
		o.HookPID, o.HookStartErr = 0, errP15HookNeverBlocked
		p12RequireShape(t, p15Assert(o), shapeNotAttempted)
	})
	t.Run("a hook that finished its hold was never blocked: RUN", func(t *testing.T) {
		o := p15TestOutcome(t, p15TestStream(t, true))
		o.MarkerExists = true
		p12RequireShape(t, p15Assert(o), shapeRunFailed)
	})
	t.Run("an unreadable liveness is RUN, never a green", func(t *testing.T) {
		o := p15TestOutcome(t, p15TestStream(t, true))
		o.HookAliveErr = os.ErrPermission
		p12RequireShape(t, p15Assert(o), shapeRunFailed)
	})
	t.Run("a run that never started is RUN", func(t *testing.T) {
		o := p15TestOutcome(t, "")
		o.Started = false
		p12RequireShape(t, p15Assert(o), shapeRunFailed)
	})
}

func TestP15_Ending(t *testing.T) {
	saw, subtype := p15Ending(p15TestStream(t, true))
	assert.True(t, saw)
	assert.Equal(t, "error_during_execution", subtype)
	saw, _ = p15Ending(p15TestStream(t, false) + "\n{\"type\":\"assis") // a line cut by the kill
	assert.False(t, saw)
}

// TestP15_ProcessAlive reads liveness the way the cell does: this process is
// alive; a child that has exited and been reaped is not.
func TestP15_ProcessAlive(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc on this platform; the live cell runs where there is one")
	}
	alive, err := p15ProcessAlive(os.Getpid())
	require.NoError(t, err)
	assert.True(t, alive)

	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	alive, err = p15ProcessAlive(cmd.Process.Pid)
	require.NoError(t, err)
	assert.False(t, alive, "a reaped child")
}

// TestP15_FixtureWire: the hook records itself and its sleeper, holds far
// past the grace, and answers allow only after the hold; the turn's argv and
// stdin are the driver's stream-json shape.
func TestP15_FixtureWire(t *testing.T) {
	script, err := p15HookScript("/x")
	require.NoError(t, err)
	for _, want := range []string{p12ShellQuote("/x/" + p15HookPIDName), p12ShellQuote("/x/" + p15SleepPIDName), p12ShellQuote("/x/" + p12MarkerName), `"behavior":"allow"`} {
		assert.Contains(t, script, want)
	}
	assert.Greater(t, p15HookHold, p15Grace+p15HookStartWait, "the hold outlasts every wait the cell makes, so a marker means the hook was never blocked")

	args := p15Args("/x/settings.json")
	assert.Equal(t, []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--settings", "/x/settings.json", "--model", liveClaudeModel}, args)

	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	raw, err := p15UserMessage("hello")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "\n"))
	require.NoError(t, json.Unmarshal(raw, &msg))
	assert.Equal(t, "user", msg.Type)
	assert.Equal(t, "user", msg.Message.Role)
	assert.Equal(t, "hello", msg.Message.Content)
}
