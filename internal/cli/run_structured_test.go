package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestDecodeMessageLine covers the line=message escape/quote handling: a single
// typed line can carry newlines, tabs, and literal quotes, while content that
// merely contains backslashes or quotes is preserved rather than mangled.
func TestDecodeMessageLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "fix the bug", "fix the bug"},
		{"no backslash fast path", `say "hello" to 'world'`, `say "hello" to 'world'`},
		{"newline escape", `line1\nline2`, "line1\nline2"},
		{"tab escape", `col1\tcol2`, "col1\tcol2"},
		{"carriage return escape", `a\rb`, "a\rb"},
		{"escaped backslash", `a\\b`, `a\b`},
		{"escaped double quote", `say \"hi\"`, `say "hi"`},
		{"escaped single quote", `it\'s`, "it's"},
		{"bare quotes pass through", `grep "foo" bar`, `grep "foo" bar`},
		{"unknown escape preserved", `regex \d+ digits`, `regex \d+ digits`},
		{"trailing backslash preserved", `path\`, `path\`},
		{"multiple escapes", `a\nb\tc\\d`, "a\nb\tc\\d"},
		{"newline then more text", `first\nsecond\nthird`, "first\nsecond\nthird"},
		{"empty string", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, decodeMessageLine(tc.in))
		})
	}
}

// TestReadMessagesLoop: each input line becomes one message, decoded, in order;
// the loop returns at EOF.
func TestReadMessagesLoop(t *testing.T) {
	// Two lines; the second carries an escaped tab to prove decoding runs.
	in := strings.NewReader("hello\nwo\\trld\n")
	out := make(chan agent.ChatMessage, 8)

	require.NoError(t, readMessagesLoop(context.Background(), in, out))
	close(out)

	var got []string
	for m := range out {
		got = append(got, m.Text)
	}
	require.Len(t, got, 2)
	assert.Equal(t, "hello", got[0])
	assert.Equal(t, "wo\trld", got[1])
}

// TestReadMessagesLoop_NoTrailingNewline: a final line without a newline is
// still delivered.
func TestReadMessagesLoop_NoTrailingNewline(t *testing.T) {
	out := make(chan agent.ChatMessage, 2)
	require.NoError(t, readMessagesLoop(context.Background(), strings.NewReader("only line"), out))
	close(out)

	var got []string
	for m := range out {
		got = append(got, m.Text)
	}
	assert.Equal(t, []string{"only line"}, got)
}

func chatEvents(evs ...agent.ChatEvent) <-chan agent.ChatEvent {
	ch := make(chan agent.ChatEvent, len(evs))
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch
}

// TestRenderChatEvents_NDJSON: json mode emits one JSON object per event,
// discriminated by "type", with the metadata (context window, tokens) preserved.
func TestRenderChatEvents_NDJSON(t *testing.T) {
	events := chatEvents(
		agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: "m"}},
		agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}},
		agent.ChatEvent{Complete: &agent.TurnMeta{ContextWindow: 1_000_000, InputTokens: 23933, CostUSD: 0.16}},
	)
	var buf bytes.Buffer
	require.NoError(t, renderChatEvents(&buf, formatJSON, events))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3)
	var types []string
	for _, l := range lines {
		require.True(t, json.Valid([]byte(l)), "line must be valid JSON: %q", l)
		var m struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(l), &m))
		types = append(types, m.Type)
	}
	assert.Equal(t, []string{"session", "entry", "complete"}, types)
	assert.Contains(t, lines[2], `"contextWindow":1000000`)
	assert.Contains(t, lines[2], `"inputTokens":23933`)
}

// TestRenderChatEvents_Text: text mode prints entries, a context/cost status
// line for completions, and a session header.
func TestRenderChatEvents_Text(t *testing.T) {
	events := chatEvents(
		agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}},
		agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "Bash"}},
		agent.ChatEvent{Complete: &agent.TurnMeta{ContextWindow: 1_000_000, InputTokens: 23933, CostUSD: 0.1667, DurationMs: 1600}},
	)
	var buf bytes.Buffer
	require.NoError(t, renderChatEvents(&buf, formatText, events))
	out := buf.String()
	assert.Contains(t, out, "assistant: hi")
	assert.Contains(t, out, "→ Bash")
	assert.Contains(t, out, "context 23.9k/1.0M")
	assert.Contains(t, out, "$0.1667")
}

func TestRenderChatEvents_UnknownFormat(t *testing.T) {
	require.Error(t, renderChatEvents(&bytes.Buffer{}, "yaml", chatEvents()))
}

// TestRunStructuredREPL_DrivesChatRPC: stdin lines reach the Chat RPC as
// messages, and the RPC's events render to stdout. The mock models the real
// contract — the stream stays open until input half-closes, then emits the turn
// and ends — so stdin reading and rendering race the way they do in production.
func TestRunStructuredREPL_DrivesChatRPC(t *testing.T) {
	var captured []string
	captureDone := make(chan struct{})
	mock := &pb.MockClient{
		ChatFunc: func(_ context.Context, _ agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
			in := make(chan agent.ChatMessage)
			events := make(chan agent.ChatEvent)
			errs := make(chan error)
			go func() {
				for m := range in {
					captured = append(captured, m.Text)
				}
				close(captureDone)
				// Input half-closed: emit the turn, then end the stream cleanly.
				events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi back"}}
				events <- agent.ChatEvent{Complete: &agent.TurnMeta{ContextWindow: 1000}}
				close(events)
				close(errs)
			}()
			return in, events, errs, nil
		},
	}

	var out bytes.Buffer
	err := runStructuredREPL(context.Background(), mock, &pb.RunStart{Options: &pb.RunOptions{}}, nil, formatJSON, strings.NewReader("hello\n"), &out)
	require.NoError(t, err)

	<-captureDone // the message reached the RPC's input channel
	assert.Equal(t, []string{"hello"}, captured)
	assert.Contains(t, out.String(), `"content":"hi back"`)
}

// TestRunStructuredREPL_InjectsManagedMCPServers: the managed MCP set composed
// from the run's ManagedConfig — the SAME payload RunStart ships to Setup —
// reaches the Chat RPC's ChatRequest, the structured path's counterpart of the
// Setup settings write.
func TestRunStructuredREPL_InjectsManagedMCPServers(t *testing.T) {
	var captured agent.ChatRequest
	mock := &pb.MockClient{
		ChatFunc: func(_ context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
			captured = req
			in := make(chan agent.ChatMessage, 1)
			events := make(chan agent.ChatEvent)
			errs := make(chan error)
			go func() {
				for range in {
				}
				close(events)
				close(errs)
			}()
			return in, events, errs, nil
		},
	}

	managed := &agent.ManagedConfig{
		MCP:       &wire.MCPConfig{},
		BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
	}
	var out bytes.Buffer
	require.NoError(t, runStructuredREPL(context.Background(), mock, &pb.RunStart{Options: &pb.RunOptions{}},
		managed.ChatMCPServers("claude-code", ""), formatJSON, strings.NewReader(""), &out))

	require.Len(t, captured.MCPServers, 2)
	// Command names the self-exec absolute path (agent.CtxloomCommand), not
	// the bare name — see its doc for the staged-vs-installed invariant.
	assert.Equal(t, agent.ChatMCPServer{Name: "ctxloom", Command: agent.CtxloomCommand(), Args: agent.CtxloomMCPArgs}, captured.MCPServers[0])
	assert.Equal(t, "taskloom", captured.MCPServers[1].Name)
}

// TestRunStructuredREPL_PropagatesSessionHarpEnv pins the tough-cloud S4
// harp-for-structured-runs contract: run.go's AssignSession stamps
// runEnv[CTXLOOM_SESSION_HARP] into req.Options.Env unconditionally (before
// --structured is ever checked, run.go ~line 819-823), and this is the exact
// seam that env has to cross to reach GRPCClient.Chat's harp-gated transcript
// tee (chat.go: `req.Env[agent.SessionHarpEnv]`). If this ever regressed to
// an empty Env, `ctxloom run --structured` would silently go uncaptured
// again — the same class of bug this whole slice exists to close.
func TestRunStructuredREPL_PropagatesSessionHarpEnv(t *testing.T) {
	var captured agent.ChatRequest
	mock := &pb.MockClient{
		ChatFunc: func(_ context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
			captured = req
			in := make(chan agent.ChatMessage, 1)
			events := make(chan agent.ChatEvent)
			errs := make(chan error)
			go func() {
				for range in {
				}
				close(events)
				close(errs)
			}()
			return in, events, errs, nil
		},
	}

	req := &pb.RunStart{Options: &pb.RunOptions{Env: map[string]string{
		agent.SessionHarpEnv: "structured-test-harp",
	}}}
	var out bytes.Buffer
	require.NoError(t, runStructuredREPL(context.Background(), mock, req, nil, formatJSON, strings.NewReader(""), &out))

	require.NotNil(t, captured.Env)
	assert.Equal(t, "structured-test-harp", captured.Env[agent.SessionHarpEnv])
}

// TestRunStructuredREPL_StreamEndsBeforeStdinEOF: a backend that crashes/ends
// (events closed early with an error parked on errs) while stdin is still open
// must NOT hang waiting for stdin EOF — it returns with the stream error rather
// than blocking forever on the open pipe.
func TestRunStructuredREPL_StreamEndsBeforeStdinEOF(t *testing.T) {
	streamErr := errors.New("backend died")
	mock := &pb.MockClient{
		ChatFunc: func(_ context.Context, _ agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
			in := make(chan agent.ChatMessage, 8) // buffered so a pending send never blocks the test
			events := make(chan agent.ChatEvent)
			errs := make(chan error, 1)
			// Backend dies immediately: park the error, then close the stream.
			errs <- streamErr
			close(events)
			close(errs)
			return in, events, errs, nil
		},
	}

	// stdin is a pipe that never reaches EOF, so the old code would block forever.
	pr, pw := io.Pipe()
	defer pw.Close()

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- runStructuredREPL(context.Background(), mock, &pb.RunStart{Options: &pb.RunOptions{}}, nil, formatJSON, pr, &out)
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, streamErr)
	case <-time.After(5 * time.Second):
		t.Fatal("runStructuredREPL hung when the stream ended before stdin EOF")
	}
}
