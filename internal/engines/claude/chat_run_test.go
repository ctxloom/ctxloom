package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// driverFor builds the kind with the transport seam injected and binds an
// instance to s; it returns the driver and the exec the turn runs over.
func driverFor(t *testing.T, s engine.Session, open chatTransportFunc, now func() time.Time, presented ...present.Presentation) (engine.StructuredDriver, engine.Exec) {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	c := kind.(Claude)
	c.open = open
	c.now = now
	inst, err := c.Instance(s)
	require.NoError(t, err)
	ex, err := inst.Exec(presented)
	require.NoError(t, err)
	return inst.Drivers()[0], ex
}

// turnArgv is the argv one structured turn spawns for s: the Instance's
// Exec plus the stream-json protocol.
func turnArgv(t *testing.T, s engine.Session, in engine.Turn, presented ...present.Presentation) []string {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	inst, err := kind.Instance(s)
	require.NoError(t, err)
	ex, err := inst.Exec(presented)
	require.NoError(t, err)
	return (&streamJSONDriver{inst: inst.(*instance)}).argv(ex, in)
}

func structured(model string, perm agent.PermissionMode) engine.Session {
	return engine.Session{Mode: engine.Structured, Label: engine.LabelConfig{Model: model}, Permission: perm}
}

func TestTurnArgs_StreamJSONFlags(t *testing.T) {
	joined := strings.Join(turnArgv(t, structured("sonnet", agent.PermissionBypass), engine.Turn{}), " ")
	assert.Contains(t, joined, flagPrint)
	assert.Contains(t, joined, "--input-format stream-json")
	assert.Contains(t, joined, "--output-format stream-json")
	assert.Contains(t, joined, "--verbose")
	assert.Contains(t, joined, "--model sonnet")
	assert.Contains(t, joined, "--dangerously-skip-permissions")
}

// TestTurnArgs_Resume verifies --resume is emitted with the key the turn
// names, and omitted on a first turn.
func TestTurnArgs_Resume(t *testing.T) {
	args := turnArgv(t, structured("", 0), engine.Turn{Resume: "sess-123"})
	assert.True(t, argPair(args, "--resume", "sess-123"))

	args = turnArgv(t, structured("", 0), engine.Turn{})
	assert.NotContains(t, args, "--resume")
}

// TestTurnArgs_MCPConfigPath verifies --mcp-config is emitted with the path
// the runner delivered, and omitted when there is none.
func TestTurnArgs_MCPConfigPath(t *testing.T) {
	delivered := present.Presentation{HostPath: "/tmp/scratch/.mcp.json", EnginePath: "/tmp/scratch/.mcp.json", Args: []string{flagMCPConfig, "/tmp/scratch/.mcp.json"}}
	args := turnArgv(t, structured("", 0), engine.Turn{}, delivered)
	assert.True(t, argPair(args, "--mcp-config", "/tmp/scratch/.mcp.json"))

	args = turnArgv(t, structured("", 0), engine.Turn{})
	assert.NotContains(t, args, "--mcp-config")
}

// TestTurnArgs_NamesSessionFromHarp verifies the structured session is named
// after ctxloom's harp via --name, matching the interactive path, so it is
// findable in the /resume picker.
func TestTurnArgs_NamesSessionFromHarp(t *testing.T) {
	s := structured("", 0)
	s.Identity = sessions.Identity{Harp: "fair-pushy-cable"}
	args := turnArgv(t, s, engine.Turn{})
	assert.True(t, argPair(args, "--name", "fair-pushy-cable"))
}

// TestTurnArgs_NoHarpNoName verifies that without a harp no --name flag is
// added.
func TestTurnArgs_NoHarpNoName(t *testing.T) {
	assert.NotContains(t, turnArgv(t, structured("", 0), engine.Turn{}), "--name")
}

// decode reads the events a turn relayed.
func decode(t *testing.T, out <-chan engine.Event) []agent.ChatEvent {
	t.Helper()
	var evs []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		evs = append(evs, ce)
	}
	return evs
}

// TestTurn_WritesTheMessageAndRelaysEvents: the turn's prompt is written to
// the transport's stdin as one NDJSON user message, the transport's stdout
// NDJSON is relayed as chat events, and the result carries the answer and
// the native key.
func TestTurn_WritesTheMessageAndRelaysEvents(t *testing.T) {
	stdout := strings.NewReader(
		`{"type":"system","subtype":"init","model":"m","session_id":"sess-9","mcp_servers":[]}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi there"}]}}` + "\n" +
			`{"type":"result","subtype":"success","usage":{"input_tokens":10},"modelUsage":{"m":{"contextWindow":1000,"outputTokens":3}},"total_cost_usd":0.01}` + "\n")
	var stdin bytes.Buffer
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{&stdin}, stdout: stdout, close: func() error { return nil }}, nil
	}
	d, ex := driverFor(t, structured("m", 0), open, nil)
	out := make(chan engine.Event, 16)
	res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "hello"}, out)
	require.NoError(t, err)
	close(out)

	assert.Contains(t, stdin.String(), `"type":"user"`)
	assert.Contains(t, stdin.String(), `"content":"hello"`)
	assert.Equal(t, "hi there", res.Answer)
	assert.Equal(t, "sess-9", res.NativeKey, "the native key the next turn resumes by")

	evs := decode(t, out)
	require.Len(t, evs, 3)
	require.NotNil(t, evs[0].Session)
	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, "hi there", evs[1].Entry.Content)
	require.NotNil(t, evs[2].Complete)
	assert.Equal(t, 1000, evs[2].Complete.ContextWindow)
}

func TestStampEntryTime_StampsWhenZero(t *testing.T) {
	// claude-code's stream-json carries no per-event time, so a fresh chat entry
	// has a zero timestamp — stampEntryTime fills it from the injected clock.
	fixed := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking}}
	out := stampEntryTime(ev, func() time.Time { return fixed })
	require.NotNil(t, out.Entry)
	assert.Equal(t, fixed, out.Entry.Timestamp)
}

func TestStampEntryTime_PreservesExisting(t *testing.T) {
	// A transcript-derived entry already carries its own timestamp; the clock must
	// not override it (and must not even be consulted).
	existing := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	called := false
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Timestamp: existing}}
	out := stampEntryTime(ev, func() time.Time { called = true; return time.Now() })
	assert.Equal(t, existing, out.Entry.Timestamp)
	assert.False(t, called, "clock must not be consulted when the entry already has a timestamp")
}

func TestStampEntryTime_NonEntryUntouched(t *testing.T) {
	// Non-entry events (complete/session) have no timestamp field to stamp.
	called := false
	ev := agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: "opus"}}
	out := stampEntryTime(ev, func() time.Time { called = true; return time.Now() })
	assert.Nil(t, out.Entry)
	assert.False(t, called)
}

// TestTurn_StampsEntriesWithInjectedClock: the relayed entries (incl. the
// blank thinking marker) carry the kind's clock time end-to-end through a
// turn.
func TestTurn_StampsEntriesWithInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	stdout := strings.NewReader(
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"hi"}]}}` + "\n")
	var stdin bytes.Buffer
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{&stdin}, stdout: stdout, close: func() error { return nil }}, nil
	}
	d, ex := driverFor(t, structured("", 0), open, func() time.Time { return fixed })
	out := make(chan engine.Event, 16)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, out)
	require.NoError(t, err)
	close(out)

	evs := decode(t, out)
	require.Len(t, evs, 2) // blank thinking marker + assistant text
	for _, ev := range evs {
		require.NotNil(t, ev.Entry)
		assert.Equal(t, fixed, ev.Entry.Timestamp, "entry %q must be stamped with the injected clock", ev.Entry.Type)
	}
}

// TestTurn_ContextCancel_Returns: cancelling ctx tears the transport down
// and returns even while the engine's stdout is still open (blocked read).
func TestTurn_ContextCancel_Returns(t *testing.T) {
	pr, pw := io.Pipe() // stdout that never produces until closed
	var stdin bytes.Buffer
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{
			stdin:  nopWriteCloser{&stdin},
			stdout: pr,
			close:  func() error { _ = pw.Close(); _ = pr.Close(); return nil }, // unblock the reader
		}, nil
	}
	d, ex := driverFor(t, structured("", 0), open, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := d.Turn(ctx, ex, engine.Turn{Prompt: "x"}, nil); done <- err }()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("the turn did not return after context cancel")
	}
}

// TestTurn_TransportOpenError_Propagates: a spawn/open failure surfaces as
// the turn's error.
func TestTurn_TransportOpenError_Propagates(t *testing.T) {
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return nil, io.ErrClosedPipe
	}
	d, ex := driverFor(t, structured("", 0), open, nil)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

// TestTurn_SpawnsTheExecsBinary: the process a turn spawns is the Exec's
// binary with the Exec's argv plus the protocol, in the Exec's working
// directory — the driver composes nothing of its own.
func TestTurn_SpawnsTheExecsBinary(t *testing.T) {
	var gotBinary, gotDir string
	var gotArgs []string
	open := func(_ context.Context, binary string, args []string, _ map[string]string, dir string) (*chatTransport, error) {
		gotBinary, gotArgs, gotDir = binary, args, dir
		return &chatTransport{stdin: nopWriteCloser{&bytes.Buffer{}}, stdout: strings.NewReader(""), close: func() error { return nil }}, nil
	}
	s := structured("", 0)
	s.Label.Binary = "/opt/claude"
	s.WorkDir = "/work"
	d, ex := driverFor(t, s, open, nil)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/opt/claude", gotBinary)
	assert.Equal(t, "/work", gotDir)
	assert.Contains(t, gotArgs, flagPrint)
	assert.True(t, argPair(gotArgs, flagInputFormat, "stream-json"))
}
