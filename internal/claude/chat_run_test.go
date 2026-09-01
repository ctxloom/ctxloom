package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// ClaudeCode must satisfy the optional StructuredChat capability.
var _ agent.StructuredChat = (*ClaudeCode)(nil)

func TestChatArgs_StreamJSONFlags(t *testing.T) {
	b := &ClaudeCode{}
	joined := strings.Join(b.chatArgs(agent.ChatRequest{Model: "sonnet", Permissions: agent.PermissionBypass}, ""), " ")
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
	b := &ClaudeCode{}
	args := b.chatArgs(agent.ChatRequest{ResumeSessionID: "sess-123"}, "")
	assert.True(t, argPair(args, "--resume", "sess-123"))

	args = b.chatArgs(agent.ChatRequest{}, "")
	assert.NotContains(t, args, "--resume")
}

// TestChatArgs_MCPConfigPath verifies --mcp-config is emitted with the path
// writeChatMCPConfig produced, and omitted when there is none.
func TestChatArgs_MCPConfigPath(t *testing.T) {
	b := &ClaudeCode{}
	args := b.chatArgs(agent.ChatRequest{}, "/tmp/scratch/.mcp.json")
	assert.True(t, argPair(args, "--mcp-config", "/tmp/scratch/.mcp.json"))

	args = b.chatArgs(agent.ChatRequest{}, "")
	assert.NotContains(t, args, "--mcp-config")
}

// TestChatArgs_NamesSessionFromHarp verifies the structured-chat session is named
// after ctxloom's harp via --name, matching the interactive path, so it's findable
// in the /resume picker.
func TestChatArgs_NamesSessionFromHarp(t *testing.T) {
	b := &ClaudeCode{}
	args := b.chatArgs(agent.ChatRequest{Env: map[string]string{sessionHarpEnv: "fair-pushy-cable"}}, "")
	assert.True(t, argPair(args, "--name", "fair-pushy-cable"))
}

// TestChatArgs_NoHarpNoName verifies that without a harp in env no --name flag is
// added.
func TestChatArgs_NoHarpNoName(t *testing.T) {
	b := &ClaudeCode{}
	args := b.chatArgs(agent.ChatRequest{}, "")
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

	b := &ClaudeCode{}
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

	b := &ClaudeCode{now: func() time.Time { return fixed }}
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

	b := &ClaudeCode{}
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
	b := &ClaudeCode{}
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
	b := &ClaudeCode{}
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
	b := &ClaudeCode{}
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

// TestWriteChatMCPConfig_Empty: no servers means no file and no --mcp-config.
func TestWriteChatMCPConfig_Empty(t *testing.T) {
	path, cleanup, err := writeChatMCPConfig(nil)
	require.NoError(t, err)
	defer cleanup()
	assert.Equal(t, "", path)
}

// TestWriteChatMCPConfig_StdioPreservesEnvVerbatim: a stdio server's Env map
// (carrying the coordinator's CTXLOOM_MCP_SOCKET stamp) must reach the written
// file byte-for-byte — this is the reach-back channel a delegated child uses to
// call back to its parent, so a dropped key here breaks delegation silently.
func TestWriteChatMCPConfig_StdioPreservesEnvVerbatim(t *testing.T) {
	servers := []agent.ChatMCPServer{
		{
			Name:    "ctxloom",
			Command: "/usr/local/bin/ctxloom",
			Args:    []string{"mcp", "serve"},
			Env:     map[string]string{"CTXLOOM_MCP_SOCKET": "/run/sock.sock"},
		},
	}
	path, cleanup, err := writeChatMCPConfig(servers)
	require.NoError(t, err)
	defer cleanup()
	require.NotEqual(t, "", path)
	assert.Equal(t, MCPFileName, filepath.Base(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"CTXLOOM_MCP_SOCKET":"/run/sock.sock"`)
	assert.Contains(t, string(data), `"command":"/usr/local/bin/ctxloom"`)
}

// TestWriteChatMCPConfig_HTTPAndSSE: remote servers carry type/url/headers,
// never command/args/env.
func TestWriteChatMCPConfig_HTTPAndSSE(t *testing.T) {
	servers := []agent.ChatMCPServer{
		{Name: "remote-http", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
		{Name: "remote-sse", Transport: agent.MCPTransportSSE, URL: "https://example.com/sse"},
	}
	path, cleanup, err := writeChatMCPConfig(servers)
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"type":"http"`)
	assert.Contains(t, s, `"url":"https://example.com/mcp"`)
	assert.Contains(t, s, `"Authorization":"Bearer tok"`)
	assert.Contains(t, s, `"type":"sse"`)
	assert.NotContains(t, s, `"command"`)
}

// TestWriteChatMCPConfig_UnknownTransport_Refused: an unrecognized transport
// string must be refused loudly, not silently dropped or mis-written.
func TestWriteChatMCPConfig_UnknownTransport_Refused(t *testing.T) {
	_, cleanup, err := writeChatMCPConfig([]agent.ChatMCPServer{{Name: "bad", Transport: "carrier-pigeon"}})
	defer cleanup()
	require.ErrorIs(t, err, ErrChatMCPTransportUnsupported)
}

// TestChat_MCPConfigCleanedUpAfterChat: the scratch .mcp.json is removed once
// Chat returns — it must not leak a temp directory per chat call.
func TestChat_MCPConfigCleanedUpAfterChat(t *testing.T) {
	var seenPath string
	b := &ClaudeCode{}
	b.openChatTransport = func(_ context.Context, args []string, _ map[string]string, _ string) (*chatTransport, error) {
		seenPath = argValue(args, "--mcp-config")
		return &chatTransport{stdin: nopWriteCloser{&bytes.Buffer{}}, stdout: strings.NewReader(""), close: func() error { return nil }}, nil
	}

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)
	close(in)
	go func() { //nolint:revive // drain
		for range out {
		}
	}()

	req := agent.ChatRequest{MCPServers: []agent.ChatMCPServer{{Name: "s", Command: "cmd"}}}
	require.NoError(t, b.Chat(context.Background(), req, in, out))

	require.NotEmpty(t, seenPath)
	_, statErr := os.Stat(filepath.Dir(seenPath))
	assert.True(t, os.IsNotExist(statErr), "the mcp config scratch dir must be removed after Chat returns")
}
