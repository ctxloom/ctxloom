package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The driver's tests speak a neutral line codec: each stdout line is one
// agent.ChatEvent as JSON, so nothing here knows any engine's format.

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// eventMapper decodes each line as an agent.ChatEvent; end is what End emits.
type eventMapper struct {
	end   []agent.ChatEvent
	ended int
}

func (m *eventMapper) Map(line []byte) []agent.ChatEvent {
	var ev agent.ChatEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	return []agent.ChatEvent{ev}
}

func (m *eventMapper) End() []agent.ChatEvent { m.ended++; return m.end }

// testTurn is a ProcessTurn over open: argv is the Exec's args plus "--turn",
// the prompt is written as one line.
func testTurn(open TransportFunc) ProcessTurn {
	return ProcessTurn{
		Name: "eng",
		Argv: func(ex engine.Exec, _ engine.Turn) ([]string, error) {
			return append(append([]string{}, ex.Args...), "--turn"), nil
		},
		WritePrompt: func(w io.Writer, prompt string) error {
			_, err := io.WriteString(w, prompt+"\n")
			return err
		},
		NewMapper: func() LineMapper { return &eventMapper{} },
		Open:      open,
	}
}

// lines is stdout carrying evs, one JSON event per line.
func lines(t *testing.T, evs ...agent.ChatEvent) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range evs {
		raw, err := json.Marshal(ev)
		require.NoError(t, err)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// fixed opens a transport whose stdout is what and whose process exits as
// exit.
func fixed(what string, exit error) TransportFunc {
	return func(context.Context, string, []string, map[string]string, string) (*Transport, error) {
		return &Transport{Stdin: nopWriteCloser{io.Discard}, Stdout: strings.NewReader(what), Reap: func() error { return exit }}, nil
	}
}

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

func assistant(text string) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: text}}
}

// TestTurn_OpensTheExecWritesThePromptAndRelays: the process is the Exec's
// binary, env and work dir with the engine's argv; the prompt goes to stdin,
// which is then closed; every event is relayed in order, the key and answer
// come back, and a clean exit is reported as 0.
func TestTurn_OpensTheExecWritesThePromptAndRelays(t *testing.T) {
	var stdin bytes.Buffer
	stdinClosed := false
	var gotBinary, gotDir string
	var gotArgs []string
	var gotEnv map[string]string
	open := func(_ context.Context, binary string, args []string, env map[string]string, dir string) (*Transport, error) {
		gotBinary, gotArgs, gotEnv, gotDir = binary, args, env, dir
		stdout := lines(t, agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: "k9"}}, assistant("hi"), agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
		return &Transport{Stdin: closeRecorder{&stdin, &stdinClosed}, Stdout: strings.NewReader(stdout)}, nil
	}
	ex := engine.Exec{Binary: "/bin/eng", Args: []string{"-x"}, Env: map[string]string{"E": "1"}, WorkDir: "/w"}
	out := make(chan engine.Event, 8)
	res, err := testTurn(open).Turn(context.Background(), ex, engine.Turn{Prompt: "hello"}, out)
	require.NoError(t, err)
	close(out)

	assert.Equal(t, "/bin/eng", gotBinary)
	assert.Equal(t, []string{"-x", "--turn"}, gotArgs)
	assert.Equal(t, map[string]string{"E": "1"}, gotEnv)
	assert.Equal(t, "/w", gotDir)
	assert.Equal(t, "hello\n", stdin.String())
	assert.True(t, stdinClosed, "stdin is closed once the prompt is written")
	assert.Equal(t, "k9", res.NativeKey)
	assert.Equal(t, "hi", res.Answer)
	require.NotNil(t, res.ExitCode)
	assert.Equal(t, 0, *res.ExitCode)
	evs := decode(t, out)
	require.Len(t, evs, 3)
	assert.NotNil(t, evs[0].Session)
	assert.NotNil(t, evs[1].Entry)
	assert.NotNil(t, evs[2].Complete)
}

type closeRecorder struct {
	io.Writer
	closed *bool
}

func (c closeRecorder) Close() error { *c.closed = true; return nil }

// TestTurn_EndEmitsAfterTheLastLine: what only the whole stream decides is
// relayed after every line, once, stamped like any other event, and folds
// into the turn (here: End's completion makes a failed exit a finished turn).
func TestTurn_EndEmitsAfterTheLastLine(t *testing.T) {
	at := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	m := &eventMapper{end: []agent.ChatEvent{
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "late"}},
		{Complete: &agent.TurnMeta{StopReason: "error"}},
	}}
	d := testTurn(fixed(lines(t, assistant("early")), errors.New("exit status 1")))
	d.NewMapper = func() LineMapper { return m }
	d.Now = func() time.Time { return at }
	out := make(chan engine.Event, 8)
	res, err := d.Turn(context.Background(), engine.Exec{}, engine.Turn{}, out)
	require.NoError(t, err, "End's completion is the turn's")
	close(out)
	assert.Equal(t, 1, m.ended)
	assert.Equal(t, "earlylate", res.Answer)
	evs := decode(t, out)
	require.Len(t, evs, 3)
	assert.Equal(t, "late", evs[1].Entry.Content)
	assert.Equal(t, at, evs[1].Entry.Timestamp)
	assert.Equal(t, "error", evs[2].Complete.StopReason)
}

func TestStampEntryTime_StampsWhenZero(t *testing.T) {
	at := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking}}
	out := stampEntryTime(ev, func() time.Time { return at })
	require.NotNil(t, out.Entry)
	assert.Equal(t, at, out.Entry.Timestamp)
}

func TestStampEntryTime_PreservesExisting(t *testing.T) {
	existing := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	called := false
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Timestamp: existing}}
	out := stampEntryTime(ev, func() time.Time { called = true; return time.Now() })
	assert.Equal(t, existing, out.Entry.Timestamp)
	assert.False(t, called, "the clock is not consulted when the entry has a time")
}

func TestStampEntryTime_NonEntryUntouched(t *testing.T) {
	called := false
	ev := agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: "m"}}
	out := stampEntryTime(ev, func() time.Time { called = true; return time.Now() })
	assert.Nil(t, out.Entry)
	assert.False(t, called)
}

// TestTurn_AccumulatesAcrossCompletions: one process can complete more than
// once. The answer is the LAST completion's text; the denials are every
// completion's, each joined with its Denied reason, and the last completion
// carries the whole turn's.
func TestTurn_AccumulatesAcrossCompletions(t *testing.T) {
	stdout := lines(t,
		agent.ChatEvent{Denied: &agent.PermissionDenial{ToolCallID: "t1", Reason: "write needs approval"}},
		assistant("first"),
		agent.ChatEvent{Complete: &agent.TurnMeta{Denials: []agent.PermissionDenial{{ToolName: "Write", ToolCallID: "t1"}}}},
		agent.ChatEvent{Denied: &agent.PermissionDenial{ToolCallID: "t2", Reason: "bash needs approval"}},
		assistant("sec"), assistant("ond"),
		agent.ChatEvent{Complete: &agent.TurnMeta{Denials: []agent.PermissionDenial{{ToolName: "Write", ToolCallID: "t1"}, {ToolName: "Bash", ToolCallID: "t2", Reason: "own"}}}},
	)
	out := make(chan engine.Event, 16)
	res, err := testTurn(fixed(stdout, nil)).Turn(context.Background(), engine.Exec{}, engine.Turn{}, out)
	require.NoError(t, err)
	close(out)
	assert.Equal(t, "second", res.Answer)
	var completes []*agent.TurnMeta
	for _, ev := range decode(t, out) {
		if ev.Complete != nil {
			completes = append(completes, ev.Complete)
		}
	}
	require.Len(t, completes, 2)
	write := agent.PermissionDenial{ToolName: "Write", ToolCallID: "t1", Reason: "write needs approval"}
	bash := agent.PermissionDenial{ToolName: "Bash", ToolCallID: "t2", Reason: "own"}
	assert.Equal(t, []agent.PermissionDenial{write}, completes[0].Denials)
	assert.Equal(t, []agent.PermissionDenial{write, bash}, completes[1].Denials, "a call is counted once; its own reason wins")
}

// TestTurn_NoCompletion_AnswerIsEverythingSaid: a process that ended
// cleanly without a completion answers with all it said.
func TestTurn_NoCompletion_AnswerIsEverythingSaid(t *testing.T) {
	res, err := testTurn(fixed(lines(t, assistant("half "), assistant("said")), nil)).Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "half said", res.Answer)
}

// TestTurn_ProcessDied_NamesTheEngineAndWrapsBoth: no completion and a
// failed exit is a death, worded with the engine's name and carrying both
// the sentinel and the process's own account.
func TestTurn_ProcessDied_NamesTheEngineAndWrapsBoth(t *testing.T) {
	died := errors.New("exit status 9")
	_, err := testTurn(fixed(lines(t, assistant("half")), died)).Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.ErrorIs(t, err, ErrTurnProcessDied)
	require.ErrorIs(t, err, died)
	assert.Equal(t, "eng: the turn's process died before it answered: exit status 9", err.Error())
}

// TestTurn_CompletionThenFailedExit_IsACompletedTurn: once a completion is
// in hand, how the process exited does not unmake the turn.
func TestTurn_CompletionThenFailedExit_IsACompletedTurn(t *testing.T) {
	stdout := lines(t, assistant("done"), agent.ChatEvent{Complete: &agent.TurnMeta{}})
	res, err := testTurn(fixed(stdout, errors.New("exit status 1"))).Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Answer)
}

// TestTurn_UnencodableEvent_IsTheTurnsErrorAndTearsDown: an event that
// cannot be relayed ends the turn: the transport is torn down and the error,
// named for the engine, is the turn's.
func TestTurn_UnencodableEvent_IsTheTurnsErrorAndTearsDown(t *testing.T) {
	torn := 0
	d := testTurn(func(context.Context, string, []string, map[string]string, string) (*Transport, error) {
		return &Transport{Stdin: nopWriteCloser{io.Discard}, Stdout: strings.NewReader("x\n"), Teardown: func() error { torn++; return nil }}, nil
	})
	d.NewMapper = func() LineMapper {
		return &eventMapper{end: []agent.ChatEvent{{Complete: &agent.TurnMeta{CostUSD: math.NaN()}}}}
	}
	out := make(chan engine.Event, 4)
	res, err := d.Turn(context.Background(), engine.Exec{}, engine.Turn{}, out)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "eng: encoding a turn event: "), err.Error())
	assert.Equal(t, 1, torn)
	assert.Nil(t, res.ExitCode, "a process the turn tore down reports no status of its own")
}

// TestTurn_ArgvAndOpenErrors: an argv the engine refuses spawns nothing; a
// spawn failure is the turn's error.
func TestTurn_ArgvAndOpenErrors(t *testing.T) {
	refused := errors.New("refused")
	opened := false
	d := testTurn(func(context.Context, string, []string, map[string]string, string) (*Transport, error) {
		opened = true
		return nil, io.ErrClosedPipe
	})
	d.Argv = func(engine.Exec, engine.Turn) ([]string, error) { return nil, refused }
	_, err := d.Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.ErrorIs(t, err, refused)
	assert.False(t, opened)

	_, err = testTurn(d.Open).Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

// TestTurn_PromptWriteFailure_TearsDownAndDrains: a stdin that refuses the
// prompt tears the transport down; the reader is drained, so the turn
// returns.
func TestTurn_PromptWriteFailure_TearsDownAndDrains(t *testing.T) {
	torn := 0
	pr, pw := io.Pipe()
	d := testTurn(func(context.Context, string, []string, map[string]string, string) (*Transport, error) {
		return &Transport{Stdin: nopWriteCloser{io.Discard}, Stdout: pr, Teardown: func() error { torn++; return pw.Close() }}, nil
	})
	d.WritePrompt = func(io.Writer, string) error { return io.ErrShortWrite }
	_, err := d.Turn(context.Background(), engine.Exec{}, engine.Turn{}, nil)
	require.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, 1, torn)
}

// TestTurn_Interrupt_DrainsTheLastWords: ending ctx is an INTERRUPT. The
// driver keeps reading until the process ends, so what it says on its way
// out is relayed; the key survives; the turn returns ctx's error; the
// process is not torn down.
func TestTurn_Interrupt_DrainsTheLastWords(t *testing.T) {
	torn := 0
	d := testTurn(func(ctx context.Context, _ string, _ []string, _ map[string]string, _ string) (*Transport, error) {
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, lines(t, agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: "k-int"}}))
			<-ctx.Done()
			_, _ = io.WriteString(pw, lines(t, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "interrupted"}}))
			_ = pw.Close()
		}()
		return &Transport{Stdin: nopWriteCloser{io.Discard}, Stdout: pr, Teardown: func() error { torn++; return pw.Close() }}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan engine.Event, 8)
	type end struct {
		res engine.TurnResult
		err error
	}
	done := make(chan end, 1)
	go func() { res, err := d.Turn(ctx, engine.Exec{}, engine.Turn{}, out); done <- end{res, err} }()
	select {
	case ev := <-out:
		require.Equal(t, "session", ev.Kind)
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never relayed its first event")
	}
	cancel()
	var got end
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted turn did not return")
	}
	require.ErrorIs(t, got.err, context.Canceled)
	assert.Equal(t, "k-int", got.res.NativeKey)
	assert.Nil(t, got.res.ExitCode)
	close(out)
	evs := decode(t, out)
	require.Len(t, evs, 1)
	assert.Equal(t, "interrupted", evs[0].Complete.StopReason)
	assert.Zero(t, torn)
}

// TestTurn_StdoutOutlivingTheInterrupt_IsTornDown: a process whose stdout
// never ends after the interrupt (a grandchild holding it open) is torn
// down once twice the grace has passed, so an interrupted turn returns.
func TestTurn_StdoutOutlivingTheInterrupt_IsTornDown(t *testing.T) {
	pr, pw := io.Pipe()
	closed := make(chan struct{})
	d := testTurn(func(context.Context, string, []string, map[string]string, string) (*Transport, error) {
		return &Transport{Stdin: nopWriteCloser{io.Discard}, Stdout: pr, Teardown: func() error { close(closed); return pw.Close() }}, nil
	})
	d.Grace = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := d.Turn(ctx, engine.Exec{}, engine.Turn{}, nil); done <- err }()
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

// TestTurn_ConsumerGoneAfterInterrupt_IsNotWaitedOn: after the interrupt the
// last words are offered to the consumer for the grace and no longer.
func TestTurn_ConsumerGoneAfterInterrupt_IsNotWaitedOn(t *testing.T) {
	d := testTurn(fixed(lines(t, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "interrupted"}}), nil))
	d.Grace = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan engine.Event) // nobody reads
	done := make(chan error, 1)
	go func() { _, err := d.Turn(ctx, engine.Exec{}, engine.Turn{}, out); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the relay waited on a consumer that stopped reading")
	}
}

// TestProcessTurn_GraceDefaults: an unset grace is DefaultInterruptGrace.
func TestProcessTurn_GraceDefaults(t *testing.T) {
	assert.Equal(t, DefaultInterruptGrace, ProcessTurn{}.grace())
	assert.Equal(t, time.Second, ProcessTurn{Grace: time.Second}.grace())
}
