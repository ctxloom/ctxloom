package mock

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func postureTurn(t *testing.T, ex engine.Exec, posture engine.TurnPosture) []agent.ChatEvent {
	t.Helper()
	inst, err := New().Instance(engine.Session{Mode: engine.Structured, WorkDir: t.TempDir()})
	require.NoError(t, err)
	out := make(chan engine.Event, 64)
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: "x", Posture: posture}, out)
	require.NoError(t, err)
	close(out)
	var events []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		events = append(events, ce)
	}
	return events
}

// The mock honours the turn's posture where a test can see it: the session
// it announces runs at the turn's mode (claude's init frame reports its
// permissionMode the same way), and the record carries the whole posture.
func TestTurn_HonoursThePosture(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.txt")
	ex := engine.Exec{Env: map[string]string{EnvRecordFile: record}}
	events := postureTurn(t, ex, engine.TurnPosture{Mode: "plan", Grants: []string{"Bash(ls)", "Read"}, Trust: engine.TrustTrusted})
	require.NotEmpty(t, events)
	require.NotNil(t, events[0].Session)
	assert.Equal(t, "plan", events[0].Session.PermissionMode)

	got, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Contains(t, string(got), "=== Posture ===\nmode=plan\ngrant=Bash(ls)\ngrant=Read\ntrust=trusted\n")
}

func TestTurn_NoPostureAsksForNoMode(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.txt")
	events := postureTurn(t, engine.Exec{Env: map[string]string{EnvRecordFile: record}}, engine.TurnPosture{})
	assert.Empty(t, events[0].Session.PermissionMode)
	got, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Contains(t, string(got), "=== Posture ===\nmode=\ntrust=untrusted\n")
}

func TestMock_ProvidesAnApprovalCodec(t *testing.T) {
	c, ok := New().Approvals().Get()
	require.True(t, ok, "the mock stands in for an engine the human approves for")
	ask, err := c.DecodeAsk("ask", []byte(`{"tool":"Bash","input":{"b":1,"a":2}}`))
	require.NoError(t, err)
	assert.Equal(t, engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{"a":2,"b":1}`)}, ask)
	_, err = c.DecodeAsk("ask", []byte(`{"input":{}}`))
	assert.Error(t, err, "an ask names its tool")

	raw, err := c.EncodeAnswer("ask", ask, engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash(ls)"}, SetMode: engine.Provide("default")})
	require.NoError(t, err)
	assert.JSONEq(t, `{"allow":true,"session_rules":["Bash(ls)"],"set_mode":"default"}`, string(raw))
	_, err = c.EncodeAnswer("ask", ask, engine.PermissionAnswer{Allow: true, SetMode: engine.Provide("bypass")})
	assert.Error(t, err, "an answer never switches to bypass")

	assert.NoError(t, c.ValidateRule("Bash(ls)"))
	assert.Error(t, c.ValidateRule(" "))
	assert.Error(t, c.ValidateRule("a\nb"))
	assert.Equal(t, []string{settingsRel, hooksRel}, c.RepoSurfaces())
}
