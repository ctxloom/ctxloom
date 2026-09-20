package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// ClaudeCode must satisfy the optional StructuredChat capability.
var _ agent.StructuredChat = (*ClaudeCode)(nil)

// chatArgv is the argv Chat spawns for req: the Instance's Exec plus the
// stream-json protocol.
func chatArgv(t *testing.T, req agent.ChatRequest) []string {
	t.Helper()
	b := NewClaudeCode()
	inst, ex, err := b.chatExec(req)
	require.NoError(t, err)
	return (&streamJSONDriver{inst: inst}).argv(ex, engine.Turn{})
}

func TestChatArgs_StreamJSONFlags(t *testing.T) {
	joined := strings.Join(chatArgv(t, agent.ChatRequest{Model: "sonnet", Permissions: agent.PermissionBypass}), " ")
	assert.Contains(t, joined, flagPrint)
	assert.Contains(t, joined, "--input-format stream-json")
	assert.Contains(t, joined, "--output-format stream-json")
	assert.Contains(t, joined, "--verbose")
	assert.Contains(t, joined, "--model sonnet")
	assert.Contains(t, joined, "--dangerously-skip-permissions")
}

// TestChatArgs_ResumeSessionID verifies --resume is emitted with the requested
// session id, and omitted when ResumeSessionID is empty.
func TestChatArgs_ResumeSessionID(t *testing.T) {
	args := chatArgv(t, agent.ChatRequest{ResumeSessionID: "sess-123"})
	assert.True(t, argPair(args, "--resume", "sess-123"))

	args = chatArgv(t, agent.ChatRequest{})
	assert.NotContains(t, args, "--resume")
}

// TestChatArgs_MCPConfigPath verifies --mcp-config is emitted with the path
// the runner delivered, and omitted when there is none.
func TestChatArgs_MCPConfigPath(t *testing.T) {
	args := chatArgv(t, agent.ChatRequest{MCPConfigPath: "/tmp/scratch/.mcp.json"})
	assert.True(t, argPair(args, "--mcp-config", "/tmp/scratch/.mcp.json"))

	args = chatArgv(t, agent.ChatRequest{})
	assert.NotContains(t, args, "--mcp-config")
}

// TestChatArgs_NamesSessionFromHarp verifies the structured-chat session is named
// after ctxloom's harp via --name, matching the interactive path, so it's findable
// in the /resume picker.
func TestChatArgs_NamesSessionFromHarp(t *testing.T) {
	args := chatArgv(t, agent.ChatRequest{Env: map[string]string{sessionHarpEnv: "fair-pushy-cable"}})
	assert.True(t, argPair(args, "--name", "fair-pushy-cable"))
}

// TestChatArgs_NoHarpNoName verifies that without a harp in env no --name flag is
// added.
func TestChatArgs_NoHarpNoName(t *testing.T) {
	args := chatArgv(t, agent.ChatRequest{})
	assert.NotContains(t, args, "--name")
}

// TestChat_PumpsMessagesAndStreamsEvents: a user message is written to the
// transport's stdin as one NDJSON line, and the transport's stdout NDJSON is
// mapped to ChatEvents on `out`; `out` is closed on return.
func TestChat_PumpsMessagesAndStreamsEvents(t *testing.T) {
	stdout := strings.NewReader(
		`{"type":"system","subtype":"init","model":"m","mcp_servers":[]}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi there"}]}}` + "\n" +
			`{"type":"result","subtype":"success","usage":{"input_tokens":10},"modelUsage":{"m":{"contextWindow":1000,"outputTokens":3}},"total_cost_usd":0.01}` + "\n")
	var stdin bytes.Buffer

	b := NewClaudeCode()
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{&stdin}, stdout: stdout, close: func() error { return nil }}, nil
	}

	in := make(chan agent.ChatMessage, 1)
	out := make(chan agent.ChatEvent)
	in <- agent.ChatMessage{Text: "hello"}
	close(in)

	var evs []agent.ChatEvent
	collected := make(chan struct{})
	go func() {
		for ev := range out {
			evs = append(evs, ev)
		}
		close(collected)
	}()

	require.NoError(t, b.Chat(context.Background(), agent.ChatRequest{Model: "m"}, in, out))
	<-collected

	// stdin received the NDJSON user message.
	assert.Contains(t, stdin.String(), `"type":"user"`)
	assert.Contains(t, stdin.String(), `"content":"hello"`)

	// stdout mapped to session + assistant entry + completion.
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

// TestChat_StampsEntriesWithInjectedClock: the streamed entries (incl. the blank
// thinking marker) carry the backend's clock time end-to-end through Chat.
func TestChat_StampsEntriesWithInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	stdout := strings.NewReader(
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"hi"}]}}` + "\n")
	var stdin bytes.Buffer

	b := NewClaudeCode()
	b.now = func() time.Time { return fixed }
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{stdin: nopWriteCloser{&stdin}, stdout: stdout, close: func() error { return nil }}, nil
	}

	in := make(chan agent.ChatMessage, 1)
	out := make(chan agent.ChatEvent)
	in <- agent.ChatMessage{Text: "x"}
	close(in)

	var evs []agent.ChatEvent
	collected := make(chan struct{})
	go func() {
		for ev := range out {
			evs = append(evs, ev)
		}
		close(collected)
	}()
	require.NoError(t, b.Chat(context.Background(), agent.ChatRequest{}, in, out))
	<-collected

	require.Len(t, evs, 2) // blank thinking marker + assistant text
	for _, ev := range evs {
		require.NotNil(t, ev.Entry)
		assert.Equal(t, fixed, ev.Entry.Timestamp, "entry %q must be stamped with the injected clock", ev.Entry.Type)
	}
}

// TestChat_ContextCancel_Returns: cancelling ctx tears the transport down and
// returns even while the agent's stdout is still open (blocked read).
func TestChat_ContextCancel_Returns(t *testing.T) {
	pr, pw := io.Pipe() // stdout that never produces until closed
	var stdin bytes.Buffer

	b := NewClaudeCode()
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return &chatTransport{
			stdin:  nopWriteCloser{&stdin},
			stdout: pr,
			close:  func() error { _ = pw.Close(); _ = pr.Close(); return nil }, // unblock the reader
		}, nil
	}

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	go func() { //nolint:revive // drain
		for range out {
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Chat(ctx, agent.ChatRequest{}, in, out) }()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not return after context cancel")
	}
}

// TestChat_TransportOpenError_Propagates: a spawn/open failure surfaces and out
// is still closed.
func TestChat_TransportOpenError_Propagates(t *testing.T) {
	b := NewClaudeCode()
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return nil, io.ErrClosedPipe
	}
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	closed := make(chan struct{})
	go func() {
		for range out {
		}
		close(closed)
	}()
	err := b.Chat(context.Background(), agent.ChatRequest{}, in, out)
	require.Error(t, err)
	<-closed // out closed despite the open failure
}

// TestChat_ModelQuirk_Refused: ModelQuirk cannot be honored over stream-json
// (no ACP session exists to place the non-spec call after) — Chat refuses
// loudly instead of silently ignoring the field, and still closes out.
func TestChat_ModelQuirk_Refused(t *testing.T) {
	b := NewClaudeCode()
	opened := false
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		opened = true
		return nil, errors.New("must not be reached")
	}
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	closed := make(chan struct{})
	go func() {
		for range out {
		}
		close(closed)
	}()

	err := b.Chat(context.Background(), agent.ChatRequest{ModelQuirk: &agent.ModelDeliveryQuirk{Method: "x"}}, in, out)
	require.ErrorIs(t, err, ErrChatModelQuirkUnsupported)
	<-closed
	assert.False(t, opened, "Chat must refuse before ever opening a transport")
}

// TestChat_ForwardTerminal_Refused: stream-json has no terminal/* callback to
// broker, so ForwardTerminal must be refused loudly rather than silently
// never emitting a ChatEvent.Terminal.
func TestChat_ForwardTerminal_Refused(t *testing.T) {
	b := NewClaudeCode()
	opened := false
	b.openChatTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		opened = true
		return nil, errors.New("must not be reached")
	}
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	closed := make(chan struct{})
	go func() {
		for range out {
		}
		close(closed)
	}()

	err := b.Chat(context.Background(), agent.ChatRequest{ForwardTerminal: true}, in, out)
	require.ErrorIs(t, err, ErrChatForwardTerminalUnsupported)
	<-closed
	assert.False(t, opened, "Chat must refuse before ever opening a transport")
}

// TestChat_NamesTheDeliveredMCPConfig: Chat names the .mcp.json the runner
// delivered (ChatRequest.MCPConfigPath) on its argv and writes no config of
// its own; with none delivered no --mcp-config is emitted.
func TestChat_NamesTheDeliveredMCPConfig(t *testing.T) {
	var seen []string
	b := NewClaudeCode()
	b.openChatTransport = func(_ context.Context, args []string, _ map[string]string, _ string) (*chatTransport, error) {
		seen = args
		return &chatTransport{stdin: nopWriteCloser{&bytes.Buffer{}}, stdout: strings.NewReader(""), close: func() error { return nil }}, nil
	}
	run := func(req agent.ChatRequest) {
		in := make(chan agent.ChatMessage)
		out := make(chan agent.ChatEvent)
		close(in)
		go func() { //nolint:revive // drain
			for range out {
			}
		}()
		require.NoError(t, b.Chat(context.Background(), req, in, out))
	}
	delivered := filepath.Join(t.TempDir(), MCPFileName)
	run(agent.ChatRequest{MCPServers: []agent.ChatMCPServer{{Name: "s", Command: "cmd"}}, MCPConfigPath: delivered})
	assert.Equal(t, delivered, argValue(seen, "--mcp-config"))

	run(agent.ChatRequest{})
	assert.NotContains(t, seen, "--mcp-config")
}
