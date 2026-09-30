package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// interruptibleTransport is a transport honouring the real one's contract on
// an interrupt: when the turn's context ends it says its last words (a result
// frame, as claude does on SIGINT) and closes stdout. wrote is closed once the
// init frame is on the pipe; closed counts hard teardowns.
func interruptibleTransport(t *testing.T, lastWords string) (chatTransportFunc, *int) {
	t.Helper()
	closed := new(int)
	open := func(ctx context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, `{"type":"system","subtype":"init","session_id":"sess-int"}`+"\n")
			<-ctx.Done()
			_, _ = io.WriteString(pw, lastWords)
			_ = pw.Close()
		}()
		return &chatTransport{
			stdin:  nopWriteCloser{io.Discard},
			stdout: pr,
			close:  func() error { *closed++; _ = pw.Close(); return nil },
		}, nil
	}
	return open, closed
}

// TestTurn_Interrupt_DrainsTheLastWordsAndKeepsTheKey: ending the turn's
// context is an INTERRUPT, not a teardown. The driver keeps reading until the
// process ends, so what claude says on its way out (its result frame) is
// relayed and the native key survives for the next turn's --resume; the turn
// returns the context's error so the host knows it was cut short. The cancel
// is injected only once the init frame has been relayed — the turn is provably
// mid-flight.
func TestTurn_Interrupt_DrainsTheLastWordsAndKeepsTheKey(t *testing.T) {
	open, closed := interruptibleTransport(t, `{"type":"result","subtype":"error_during_execution","stop_reason":"interrupted","num_turns":1}`+"\n")
	d, ex := driverFor(t, structured("", 0), open, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan engine.Event, 16)
	type turnEnd struct {
		res engine.TurnResult
		err error
	}
	done := make(chan turnEnd, 1)
	go func() { res, err := d.Turn(ctx, ex, engine.Turn{Prompt: "x"}, out); done <- turnEnd{res, err} }()

	select {
	case ev := <-out:
		require.Equal(t, "session", ev.Kind, "the init frame is relayed before the interrupt")
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never relayed its init frame")
	}
	cancel()
	var end turnEnd
	select {
	case end = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted turn did not return")
	}
	require.ErrorIs(t, end.err, context.Canceled)
	assert.Equal(t, "sess-int", end.res.NativeKey, "the key the next turn resumes by survives the interrupt")
	close(out)
	var complete *agent.TurnMeta
	for _, ev := range decode(t, out) {
		if ev.Complete != nil {
			complete = ev.Complete
		}
	}
	require.NotNil(t, complete, "the result the process gave on its way out is relayed")
	assert.Equal(t, "interrupted", complete.StopReason)
	assert.Zero(t, *closed, "an interrupted process is not torn down; it ends on its own")
}

// crashingTransport ends stdout after what, then reports exit as how the
// process ended.
func crashingTransport(what string, exit error) chatTransportFunc {
	return func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{
			stdin:  nopWriteCloser{io.Discard},
			stdout: strings.NewReader(what),
			close:  func() error { return nil },
			wait:   func() error { return exit },
		}, nil
	}
}

// TestTurn_ProcessDiedMidTurn_IsTheTurnsError: a process that ends WITHOUT a
// result frame and exits in failure died mid-turn. That is the turn's error —
// the host ends the run on it — never a turn that "finished" with whatever it
// had said so far.
func TestTurn_ProcessDiedMidTurn_IsTheTurnsError(t *testing.T) {
	died := errors.New("signal: segmentation fault")
	open := crashingTransport(
		`{"type":"system","subtype":"init","session_id":"sess-1"}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"half an answ"}]}}`+"\n", died)
	d, ex := driverFor(t, structured("", 0), open, nil)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.ErrorIs(t, err, errTurnProcessDied)
	require.ErrorIs(t, err, died, "the process's own account of its death rides along")
}

// TestTurn_ResultThenFailedExit_IsACompletedTurn: once the result frame is in
// hand the turn completed; how the process exited afterwards does not unmake
// it (claude exits non-zero after an error result, which the result itself
// already reports).
func TestTurn_ResultThenFailedExit_IsACompletedTurn(t *testing.T) {
	open := crashingTransport(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n"+
			`{"type":"result","subtype":"success","stop_reason":"end_turn","num_turns":1}`+"\n",
		errors.New("exit status 1"))
	d, ex := driverFor(t, structured("", 0), open, nil)
	res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Answer)
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

// TestTurn_AccumulatesAcrossResultFrames: one turn's process can print more
// than one result. The answer and the accounting are the LAST result's; the
// denials are every result's, each joined with the reason its
// permission_denied frame gave, and ride the completion the runner keeps
// (the last one).
func TestTurn_AccumulatesAcrossResultFrames(t *testing.T) {
	stdout := strings.NewReader(
		`{"type":"system","subtype":"init","session_id":"sess-1"}` + "\n" +
			`{"type":"system","subtype":"permission_denied","tool_name":"Write","tool_use_id":"t1","message":"write needs approval"}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}` + "\n" +
			`{"type":"result","subtype":"success","stop_reason":"end_turn","num_turns":1,"permission_denials":[{"tool_name":"Write","tool_use_id":"t1","tool_input":{}}]}` + "\n" +
			`{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"t2","message":"bash needs approval"}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n" +
			`{"type":"result","subtype":"success","stop_reason":"end_turn","num_turns":2,"permission_denials":[{"tool_name":"Bash","tool_use_id":"t2","tool_input":{}}]}` + "\n")
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{io.Discard}, stdout: stdout, close: func() error { return nil }}, nil
	}
	d, ex := driverFor(t, structured("m", 0), open, nil)
	out := make(chan engine.Event, 32)
	res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "go"}, out)
	require.NoError(t, err)
	close(out)
	assert.Equal(t, "second", res.Answer, "the answer is the last result's")

	var completes []*agent.TurnMeta
	for _, ev := range decode(t, out) {
		if ev.Complete != nil {
			completes = append(completes, ev.Complete)
		}
	}
	require.Len(t, completes, 2)
	write := agent.PermissionDenial{ToolName: "Write", ToolCallID: "t1", Reason: "write needs approval", Decider: agent.DeciderPolicy}
	bash := agent.PermissionDenial{ToolName: "Bash", ToolCallID: "t2", Reason: "bash needs approval", Decider: agent.DeciderPolicy}
	assert.Equal(t, []agent.PermissionDenial{write}, completes[0].Denials)
	assert.Equal(t, []agent.PermissionDenial{write, bash}, completes[1].Denials, "the last completion carries the whole turn's denials")
	assert.Equal(t, 2, completes[1].NumTurns, "the accounting is the last result's")
}

// TestRelayTurn_StdoutOutlivingTheInterrupt_IsTornDown: a process whose stdout
// never ends after the interrupt (a grandchild holding it open) is torn down
// once twice the grace has passed, so an interrupted turn always returns.
func TestRelayTurn_StdoutOutlivingTheInterrupt_IsTornDown(t *testing.T) {
	pr, pw := io.Pipe()
	closed := make(chan struct{})
	tr := &chatTransport{
		stdin:  nopWriteCloser{io.Discard},
		stdout: pr,
		close:  func() error { close(closed); return pw.Close() },
	}
	events := make(chan agent.ChatEvent, 1)
	go func() { readChatEvents(pr, events, time.Now); close(events) }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := relayTurn(ctx, tr, events, nil, 50*time.Millisecond); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("an interrupted turn whose stdout never ended did not return")
	}
	select {
	case <-closed:
	default:
		t.Fatal("the overdue transport was not torn down")
	}
}

// TestRelayTurn_ConsumerGoneAfterInterrupt_IsNotWaitedOn: after the interrupt
// the process's last words are offered to the consumer for the grace and no
// longer — a consumer that stopped reading cannot hold the turn open.
func TestRelayTurn_ConsumerGoneAfterInterrupt_IsNotWaitedOn(t *testing.T) {
	tr := &chatTransport{stdin: nopWriteCloser{io.Discard}, stdout: strings.NewReader(""), close: func() error { return nil }}
	events := make(chan agent.ChatEvent, 1)
	events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "interrupted"}}
	close(events)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan engine.Event) // nobody reads
	done := make(chan error, 1)
	go func() { _, err := relayTurn(ctx, tr, events, out, 50*time.Millisecond); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the relay waited on a consumer that stopped reading")
	}
}
