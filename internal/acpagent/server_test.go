package acpagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakeEngine is a scripted engine conversation: it records the messages the
// server delivers and lets the test emit events per turn.
type fakeEngine struct {
	in       chan string
	events   chan agent.ChatEvent
	errs     chan error
	received chan string
	closed   chan struct{}
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		in:       make(chan string, 4),
		events:   make(chan agent.ChatEvent, 16),
		errs:     make(chan error, 1),
		received: make(chan string, 4),
		closed:   make(chan struct{}),
	}
}

// chat converts the fake into the server's EngineChat view.
func (f *fakeEngine) chat(contextText string) *EngineChat {
	return &EngineChat{
		Context: contextText,
		In:      f.in,
		Events:  f.events,
		Errs:    f.errs,
		Close: func() {
			select {
			case <-f.closed:
			default:
				close(f.closed)
				close(f.events)
			}
		},
	}
}

// pump forwards delivered messages to received (so tests can assert them).
func (f *fakeEngine) pump() {
	for msg := range f.in {
		f.received <- msg
	}
}

// frame is the raw client-side JSON-RPC frame (independent of the production
// codec, so a codec bug cannot hide by symmetry).
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// testClient drives a served agent over in-memory pipes with raw frames.
type testClient struct {
	t      *testing.T
	w      io.Writer
	frames chan frame
	nextID int
}

func startServer(t *testing.T, open ChatOpener) *testClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go func() { _ = Serve(ctx, serverR, serverW, open) }()
	t.Cleanup(func() { cancel(); _ = clientW.Close(); _ = serverW.Close() })

	c := &testClient{t: t, w: clientW, frames: make(chan frame, 32)}
	go func() {
		scan := bufio.NewScanner(clientR)
		scan.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for scan.Scan() {
			var f frame
			if json.Unmarshal(scan.Bytes(), &f) == nil {
				c.frames <- f
			}
		}
		close(c.frames)
	}()
	return c
}

func (c *testClient) send(method string, params string) int {
	c.nextID++
	id := c.nextID
	line := `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + `}` + "\n"
	_, err := c.w.Write([]byte(line))
	require.NoError(c.t, err)
	return id
}

func (c *testClient) notify(method string, params string) {
	line := `{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `}` + "\n"
	_, err := c.w.Write([]byte(line))
	require.NoError(c.t, err)
}

// waitResponse collects frames until the response for id arrives, returning it
// plus any session/update notifications seen on the way.
func (c *testClient) waitResponse(id int) (frame, []frame) {
	var updates []frame
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				c.t.Fatal("connection closed before response")
			}
			if f.Method == "session/update" {
				updates = append(updates, f)
				continue
			}
			if string(f.ID) == strconv.Itoa(id) {
				return f, updates
			}
		case <-deadline:
			c.t.Fatal("timed out waiting for response")
		}
	}
}

// TestServe_FullTurn drives initialize → session/new → session/prompt against
// a scripted engine: the context lands as the first turn's lead block, entries
// stream back as session/update, and the turn ends with stopReason end_turn.
func TestServe_FullTurn(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(ctx context.Context, cwd string) (*EngineChat, error) {
		assert.Equal(t, "/proj", cwd)
		return eng.chat("PROJECT CONTEXT"), nil
	})

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"protocolVersion"`)

	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	require.NotEmpty(t, newResp.SessionId)

	// Script the turn: when the message arrives, stream two entries + complete.
	go func() {
		msg := <-eng.received
		assert.Equal(t, "PROJECT CONTEXT\n\nhello agent", msg, "first turn carries the assembled context as the lead block")
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}}
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi!"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()

	id := c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"hello agent"}]}`)
	resp, updates := c.waitResponse(id)
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"end_turn"`)
	require.Len(t, updates, 2)
	assert.Contains(t, string(updates[0].Params), `"agent_thought_chunk"`)
	assert.Contains(t, string(updates[0].Params), "pondering")
	assert.Contains(t, string(updates[1].Params), `"agent_message_chunk"`)
	assert.Contains(t, string(updates[1].Params), "hi!")

	// Second turn: no context prefix.
	go func() {
		msg := <-eng.received
		assert.Equal(t, "again", msg, "context rides only the first turn")
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"again"}]}`))
	require.Nil(t, resp.Error)
}

// TestServe_CancelMidTurn: session/cancel during an in-flight prompt tears the
// session down and the prompt resolves with stopReason cancelled (the spec's
// REQUIRED post-cancel stop reason).
func TestServe_CancelMidTurn(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, string) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))

	id := c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"slow"}]}`)
	<-eng.received // the turn is in flight; the engine never completes it
	c.notify("session/cancel", `{"sessionId":"`+newResp.SessionId+`"}`)

	resp, _ = c.waitResponse(id)
	require.Nil(t, resp.Error, "cancel must resolve the prompt, not error it")
	assert.Contains(t, string(resp.Result), `"cancelled"`)
}

// TestServe_ToolCallPairing: tool_use/tool_result pair up via generated
// toolCallIds (FIFO per tool name).
func TestServe_ToolCallPairing(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, string) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))

	go func() {
		<-eng.received
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "fs_read", ToolInput: json.RawMessage(`{"path":"x"}`)}}
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolOutput: "data"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{}}
	}()
	_, updates := c.waitResponse(c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"use a tool"}]}`))
	require.Len(t, updates, 2)

	var call, upd struct {
		Update struct {
			Kind       string `json:"sessionUpdate"`
			ToolCallId string `json:"toolCallId"`
		} `json:"update"`
	}
	require.NoError(t, json.Unmarshal(updates[0].Params, &call))
	require.NoError(t, json.Unmarshal(updates[1].Params, &upd))
	assert.Equal(t, "tool_call", call.Update.Kind)
	assert.Equal(t, "tool_call_update", upd.Update.Kind)
	assert.Equal(t, call.Update.ToolCallId, upd.Update.ToolCallId, "the result targets the call's generated id")
}
