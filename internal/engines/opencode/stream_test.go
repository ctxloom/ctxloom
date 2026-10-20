package opencode

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// The driver's tests run real turns through kit.ProcessTurn over a scripted
// stdout. testdata/run_*.jsonl are RECORDED: `opencode run --format json
// --thinking` (opencode 1.18.4, the newest binary on the recording host)
// against a scripted OpenAI-compatible stub, with the env this package's
// Exec composes, in a throwaway home; only temp paths and response dates
// were normalized. The few inline lines below are AUTHORED from the v1.18.35
// source's event and error shapes, for what a stub cannot produce (a header
// timeout, a context overflow, a process killed mid-turn).

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }

// turnOver runs one turn whose process prints stdout and exits as exit.
func turnOver(t *testing.T, stdout []byte, exit error, opts ...func(*instance)) (engine.TurnResult, []agent.ChatEvent, error) {
	t.Helper()
	var gotEnv map[string]string
	open := func(_ context.Context, _ string, _ []string, env map[string]string, _ string) (*kit.Transport, error) {
		gotEnv = env
		return &kit.Transport{Stdin: nopWC{io.Discard}, Stdout: bytes.NewReader(stdout), Reap: func() error { return exit }}, nil
	}
	inst := bind(t, engine.Session{Label: engine.LabelConfig{Model: "qwen", Body: map[string]any{"context_window": 131072}}}, WithTransport(open))
	for _, o := range opts {
		o(inst)
	}
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	out := make(chan engine.Event, 256)
	res, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: "say pong"}, out)
	close(out)
	require.NotNil(t, gotEnv)
	return res, decodeEvents(t, out), err
}

func bind(t *testing.T, s engine.Session, opts ...Option) *instance {
	t.Helper()
	e, err := Build(opts...)
	require.NoError(t, err)
	inst, err := e.Instance(s)
	require.NoError(t, err)
	return inst.(*instance)
}

func decodeEvents(t *testing.T, out <-chan engine.Event) []agent.ChatEvent {
	t.Helper()
	var evs []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		evs = append(evs, ce)
	}
	return evs
}

// fixtures are embedded: the sandboxed TestMain moves the working
// directory.
//
//go:embed testdata/*.jsonl
var fixtures embed.FS

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fixtures.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

var errExit1 = errors.New("exit status 1")

func completions(evs []agent.ChatEvent) []*agent.TurnMeta {
	var out []*agent.TurnMeta
	for _, e := range evs {
		if e.Complete != nil {
			out = append(out, e.Complete)
		}
	}
	return out
}

func failures(evs []agent.ChatEvent) []agent.FailureKind {
	var out []agent.FailureKind
	for _, e := range evs {
		if e.Failed != nil {
			out = append(out, e.Failed.Kind)
		}
	}
	return out
}

func entriesOf(evs []agent.ChatEvent, typ agent.SessionEntryType) []*agent.SessionEntry {
	var out []*agent.SessionEntry
	for _, e := range evs {
		if e.Entry != nil && e.Entry.Type == typ {
			out = append(out, e.Entry)
		}
	}
	return out
}

// A plain answer: the session announced once (the key the next turn
// resumes by), the text as the answer, one completion with the step's
// usage and the label's window.
// Every entry time opencode reports (a user message's creation, a text
// part's end, a tool call's end) is the same instant in UTC, the zone the
// transcript recorder writes, whatever zone the host runs in. Checked on the
// mapped value itself: the wire's JSON keeps the instant but not the zone.
func TestMessageEvents_EntryTimesAreUTC(t *testing.T) {
	const ms = 1791434956526
	user := fmt.Sprintf(`{"info":{"role":"user","time":{"created":%d}},"parts":[{"type":"text","text":"hi"}]}`, ms)
	assistant := fmt.Sprintf(`{"info":{"role":"assistant"},"parts":[{"type":"text","text":"pong","time":{"end":%d}},{"type":"tool","tool":"bash","callID":"c1","state":{"status":"completed","output":"ok","time":{"end":%d}}}]}`, ms, ms)
	var entries []*agent.SessionEntry
	for _, raw := range []string{user, assistant} {
		evs, err := MessageEvents([]byte(raw))
		require.NoError(t, err)
		for _, e := range evs {
			if e.Entry != nil {
				entries = append(entries, e.Entry)
			}
		}
	}
	require.Len(t, entries, 4, "user text, assistant text, tool use, tool result")
	for _, e := range entries {
		assert.True(t, time.UnixMilli(ms).Equal(e.Timestamp), "%s: %v", e.Type, e.Timestamp)
		assert.Same(t, time.UTC, e.Timestamp.Location(), "%s is reported in UTC", e.Type)
	}
}

func TestTurn_RecordedAnswer(t *testing.T) {
	res, evs, err := turnOver(t, fixture(t, "run_simple.jsonl"), nil)
	require.NoError(t, err)
	assert.Equal(t, "ses_ee627e80bffeCZmKYTSJOyhyC6", res.NativeKey)
	assert.Equal(t, "pong", res.Answer)
	require.NotNil(t, evs[0].Session, "the session is the first event")
	text := entriesOf(evs, agent.EntryTypeAssistant)
	require.Len(t, text, 1)
	assert.True(t, time.UnixMilli(1791434956526).Equal(text[0].Timestamp), "an entry carries the time its part ended: %v", text[0].Timestamp)
	cs := completions(evs)
	require.Len(t, cs, 1)
	assert.Equal(t, "stop", cs[0].StopReason)
	assert.Equal(t, 1500, cs[0].InputTokens)
	assert.Equal(t, 3, cs[0].OutputTokens)
	assert.Equal(t, 131072, cs[0].ContextWindow)
	assert.Empty(t, failures(evs))
}

// Reasoning (emitted under --thinking) is a thinking entry beside the answer.
func TestTurn_RecordedReasoning(t *testing.T) {
	res, evs, err := turnOver(t, fixture(t, "run_think.jsonl"), nil)
	require.NoError(t, err)
	assert.Equal(t, "pong", res.Answer)
	thinking := entriesOf(evs, agent.EntryTypeThinking)
	require.Len(t, thinking, 1)
	assert.Equal(t, "The user wants pong.", thinking[0].Content)
}

// A tool step and a final step: the tool's use and result as entries (by
// call id), no completion at the tool step, one at the final; the input in
// context is the final step's, the output is summed across steps.
func TestTurn_RecordedToolCall(t *testing.T) {
	res, evs, err := turnOver(t, fixture(t, "run_tool.jsonl"), nil)
	require.NoError(t, err)
	assert.Equal(t, "pong", res.Answer)
	use, result := entriesOf(evs, agent.EntryTypeToolUse), entriesOf(evs, agent.EntryTypeToolResult)
	require.Len(t, use, 1)
	require.Len(t, result, 1)
	assert.Equal(t, "glob", use[0].ToolName)
	assert.Equal(t, "call_1", use[0].ToolCallID)
	assert.JSONEq(t, `{"pattern":"*.txt"}`, string(use[0].ToolInput))
	assert.Equal(t, "/work/notes.txt", result[0].ToolOutput)
	assert.False(t, result[0].IsError)
	cs := completions(evs)
	require.Len(t, cs, 1, "a step that stopped to call tools completes nothing")
	assert.Equal(t, 1500, cs[0].InputTokens)
	assert.Equal(t, 7+3, cs[0].OutputTokens)
}

// A call a rule denies: the failed call's result is opencode's refusal, a
// Denied names it, the turn goes on to its answer, and its one completion
// carries the denial with its reason.
func TestTurn_RecordedRuleDenial(t *testing.T) {
	res, evs, err := turnOver(t, fixture(t, "run_deny_rule.jsonl"), nil)
	require.NoError(t, err)
	assert.Equal(t, "pong", res.Answer)
	result := entriesOf(evs, agent.EntryTypeToolResult)
	require.Len(t, result, 1)
	assert.True(t, result[0].IsError)
	cs := completions(evs)
	require.Len(t, cs, 1)
	require.Len(t, cs[0].Denials, 1)
	d := cs[0].Denials[0]
	assert.Equal(t, "bash", d.ToolName)
	assert.Equal(t, "call_9", d.ToolCallID)
	assert.Contains(t, d.Reason, "a rule which prevents you")
	assert.Equal(t, agent.DeciderPolicy, d.Decider)
}

// An ask `run` rejected HALTS the turn: the stream ends after the tool step
// with no final step and a clean exit. The turn is still completed — with
// the denial on it — so the block reaches whoever asked, not a silent empty
// answer.
func TestTurn_RecordedRejectedAskCompletesTheHaltedTurn(t *testing.T) {
	_, evs, err := turnOver(t, fixture(t, "run_deny_ask.jsonl"), nil)
	require.NoError(t, err)
	cs := completions(evs)
	require.Len(t, cs, 1)
	assert.Equal(t, "tool-calls", cs[0].StopReason)
	require.Len(t, cs[0].Denials, 1)
	assert.Contains(t, cs[0].Denials[0].Reason, "The user rejected permission")
}

// A rejected credential: the one error line names the kind the coordinator
// holds the run on, and the turn completes in error, so the exit 1 that
// follows is not read as a death.
func TestTurn_RecordedRejectedCredential(t *testing.T) {
	res, evs, err := turnOver(t, fixture(t, "run_401.jsonl"), errExit1)
	require.NoError(t, err)
	assert.Equal(t, []agent.FailureKind{agent.FailureCredentialRejected}, failures(evs))
	cs := completions(evs)
	require.Len(t, cs, 1)
	assert.Equal(t, "error", cs[0].StopReason)
	assert.NotEmpty(t, res.NativeKey)
}

// errorLine is an authored error event of the v1.18.35 shape.
func errorLine(name string, data map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": "error", "timestamp": 1, "sessionID": "ses_x", "error": map[string]any{"name": name, "data": data}})
	return string(b) + "\n"
}

const stepStop = `{"type":"step_finish","timestamp":2,"sessionID":"ses_x","part":{"type":"step-finish","reason":"stop","cost":0.5,"tokens":{"input":900,"output":40,"reasoning":0,"cache":{"read":100,"write":0}}}}` + "\n"

// opencode's failure table (Q-E, Q11): each error a kind or none; every
// error completes the turn in error.
func TestTurn_FailureTable(t *testing.T) {
	status := func(code int) map[string]any {
		return map[string]any{"message": "m", "statusCode": code, "isRetryable": false}
	}
	for name, tc := range map[string]struct {
		line string
		want []agent.FailureKind
	}{
		"auth error":         {errorLine("ProviderAuthError", map[string]any{"providerID": "ctxloom", "message": "no key"}), []agent.FailureKind{agent.FailureCredentialRejected}},
		"403":                {errorLine("APIError", status(403)), []agent.FailureKind{agent.FailureCredentialRejected}},
		"429":                {errorLine("APIError", status(429)), []agent.FailureKind{agent.FailureRateLimited}},
		"502":                {errorLine("APIError", status(502)), []agent.FailureKind{agent.FailureOverloaded}},
		"503":                {errorLine("APIError", status(503)), []agent.FailureKind{agent.FailureOverloaded}},
		"529":                {errorLine("APIError", status(529)), []agent.FailureKind{agent.FailureOverloaded}},
		"header timeout":     {errorLine("APIError", map[string]any{"message": "timed out waiting for headers", "isRetryable": true, "metadata": map[string]any{"code": "ProviderHeaderTimeoutError", "timeoutMs": "300000"}}), []agent.FailureKind{agent.FailureOverloaded}},
		"connection refused": {errorLine("APIError", map[string]any{"message": "Cannot connect to API: connect ECONNREFUSED 10.0.0.9:8081", "isRetryable": true}), []agent.FailureKind{agent.FailureEndpointUnreachable}},
		"connection reset":   {errorLine("APIError", map[string]any{"message": "Connection reset by server", "isRetryable": true, "metadata": map[string]any{"code": "ECONNRESET"}}), []agent.FailureKind{agent.FailureEndpointUnreachable}},
		"dns":                {errorLine("UnknownError", map[string]any{"message": "getaddrinfo ENOTFOUND model-host"}), []agent.FailureKind{agent.FailureEndpointUnreachable}},
		"500":                {errorLine("APIError", status(500)), nil},
		"no status, other":   {errorLine("APIError", map[string]any{"message": "bad json from provider", "isRetryable": false}), nil},
		"content filter":     {errorLine("ContentFilterError", map[string]any{"message": "filtered"}), nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, evs, err := turnOver(t, []byte(tc.line), errExit1)
			require.NoError(t, err)
			assert.Equal(t, tc.want, failures(evs))
			cs := completions(evs)
			require.Len(t, cs, 1)
			assert.Equal(t, "error", cs[0].StopReason)
		})
	}
}

// A 429's Retry-After says when the limit lifts: seconds from now, or a date.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	assert.Equal(t, now.Add(30*time.Second), retryAfter(map[string]string{"retry-after": "30"}, clock))
	assert.Equal(t, time.Date(2026, 10, 7, 12, 5, 0, 0, time.UTC), retryAfter(map[string]string{"retry-after": "Wed, 07 Oct 2026 12:05:00 GMT"}, clock))
	assert.True(t, retryAfter(map[string]string{"retry-after": "soon"}, clock).IsZero())
	assert.True(t, retryAfter(nil, clock).IsZero())
	f := failure(&ocError{Name: "APIError", Data: ocErrorData{StatusCode: ptr(429), ResponseHeaders: map[string]string{"retry-after": "30"}}}, clock)
	require.NotNil(t, f)
	assert.Equal(t, now.Add(30*time.Second), f.ResetsAt)
}

func ptr[T any](v T) *T { return &v }

// A context overflow opencode recovers from (it compacts) decides nothing:
// the final step completes the turn, and nothing is held.
func TestTurn_RecoveredOverflowCompletesNormally(t *testing.T) {
	_, evs, err := turnOver(t, []byte(errorLine("ContextOverflowError", map[string]any{"message": "too long"})+stepStop), errExit1)
	require.NoError(t, err)
	assert.Empty(t, failures(evs))
	cs := completions(evs)
	require.Len(t, cs, 1)
	assert.Equal(t, "stop", cs[0].StopReason)
	assert.Equal(t, 900, cs[0].InputTokens)
	assert.Equal(t, 100, cs[0].CacheReadTokens)
	assert.Equal(t, 1000, cs[0].ContextTokens, "in context: input and cache reads together")
}

// An overflow it does not recover from completes the turn as one.
func TestTurn_UnrecoveredOverflow(t *testing.T) {
	_, evs, err := turnOver(t, []byte(errorLine("ContextOverflowError", map[string]any{"message": "too long"})), errExit1)
	require.NoError(t, err)
	assert.Empty(t, failures(evs))
	cs := completions(evs)
	require.Len(t, cs, 1)
	assert.Equal(t, "context_overflow", cs[0].StopReason)
}

// A process killed after a tool step, with no error and no refusal, died
// mid-turn: nothing completes it.
func TestTurn_KilledMidTurnIsADeath(t *testing.T) {
	lines := fixture(t, "run_tool.jsonl")
	cut := bytes.Join(bytes.SplitAfter(lines, []byte("\n"))[:3], nil) // step_start, tool_use, step_finish(tool-calls)
	_, evs, err := turnOver(t, cut, errExit1)
	require.ErrorIs(t, err, kit.ErrTurnProcessDied)
	assert.Empty(t, completions(evs))
}

// Unknown and malformed lines map to nothing; a stream with no final step
// that exits cleanly still answers with what it said.
func TestTurn_TolerantOfNoiseAndAMissingFinalStep(t *testing.T) {
	text := `{"type":"text","timestamp":1,"sessionID":"ses_y","part":{"type":"text","text":"partial","time":{"start":1,"end":2}}}` + "\n"
	res, evs, err := turnOver(t, []byte("not json\n"+`{"type":"novel_event","sessionID":"ses_y"}`+"\n"+text), nil)
	require.NoError(t, err)
	assert.Equal(t, "partial", res.Answer)
	assert.Equal(t, "ses_y", res.NativeKey)
	assert.Empty(t, completions(evs))
}

// Output and cost are session totals across the session's turns (each turn
// is a fresh process that knows only its own steps); the input in context
// is the latest step's.
func TestTurn_TotalsRunAcrossTurns(t *testing.T) {
	var stdout []byte
	open := func(context.Context, string, []string, map[string]string, string) (*kit.Transport, error) {
		return &kit.Transport{Stdin: nopWC{io.Discard}, Stdout: bytes.NewReader(stdout), Reap: func() error { return nil }}, nil
	}
	inst := bind(t, engine.Session{Label: engine.LabelConfig{Model: "qwen"}}, WithTransport(open))
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	var last *agent.TurnMeta
	for range 2 {
		stdout = []byte(stepStop)
		out := make(chan engine.Event, 16)
		_, err := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: "p"}, out)
		require.NoError(t, err)
		close(out)
		cs := completions(decodeEvents(t, out))
		require.Len(t, cs, 1)
		last = cs[0]
	}
	assert.Equal(t, 80, last.OutputTokens)
	assert.InDelta(t, 1.0, last.CostUSD, 1e-9)
	assert.Equal(t, 900, last.InputTokens)
}

// The prompt is written raw to stdin, and stdin is closed (run reads it to
// EOF).
func TestTurn_WritesTheRawPromptAndClosesStdin(t *testing.T) {
	var stdin closeRecorder
	open := func(context.Context, string, []string, map[string]string, string) (*kit.Transport, error) {
		return &kit.Transport{Stdin: &stdin, Stdout: strings.NewReader(stepStop)}, nil
	}
	inst := bind(t, engine.Session{}, WithTransport(open))
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: "multi\nline {{x}}"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "multi\nline {{x}}", stdin.String())
	assert.True(t, stdin.closed)
}

type closeRecorder struct {
	bytes.Buffer
	closed bool
}

func (c *closeRecorder) Close() error { c.closed = true; return nil }
