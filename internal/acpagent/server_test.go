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
	in       chan agent.ChatMessage
	events   chan agent.ChatEvent
	errs     chan error
	received chan agent.ChatMessage
	closed   chan struct{}

	// optional EngineChat extensions applied by chat()
	harp            string
	modes           *SessionModes
	assembleProfile func(ctx context.Context, profile string) (string, error)
	replay          []agent.SessionEntry
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		in:       make(chan agent.ChatMessage, 8),
		events:   make(chan agent.ChatEvent, 16),
		errs:     make(chan error, 1),
		received: make(chan agent.ChatMessage, 8),
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
		Harp:            f.harp,
		Modes:           f.modes,
		AssembleProfile: f.assembleProfile,
		Replay:          f.replay,
	}
}

// pump forwards delivered messages to received (so tests can assert them).
func (f *fakeEngine) pump() {
	for msg := range f.in {
		f.received <- msg
	}
}

// receivedText waits for the next delivered USER message, failing on control
// messages so a mis-routed frame cannot pass silently.
func (f *fakeEngine) receivedText(t *testing.T) string {
	t.Helper()
	msg := f.receiveMsg(t)
	require.Nil(t, msg.Permission, "expected a user message, got a permission answer")
	require.False(t, msg.CancelTurn, "expected a user message, got a turn cancel")
	return msg.Text
}

func (f *fakeEngine) receiveMsg(t *testing.T) agent.ChatMessage {
	t.Helper()
	select {
	case msg := <-f.received:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a delivered engine message")
		return agent.ChatMessage{}
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

// testClient drives a served agent over in-memory pipes with raw frames. The
// reader goroutine routes frames by shape: responses (no method), session
// update notifications, and agent→client REQUESTS (session/request_permission)
// each land on their own channel — a server request id can numerically collide
// with a client request id, so shape, not id, routes a frame.
type testClient struct {
	t         *testing.T
	w         io.Writer
	responses chan frame
	updates   chan frame
	requests  chan frame
	nextID    int
}

func startServer(t *testing.T, open ChatOpener) *testClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go func() { _ = Serve(ctx, serverR, serverW, open) }()
	t.Cleanup(func() { cancel(); _ = clientW.Close(); _ = serverW.Close() })

	c := &testClient{
		t:         t,
		w:         clientW,
		responses: make(chan frame, 32),
		updates:   make(chan frame, 64),
		requests:  make(chan frame, 8),
	}
	go func() {
		scan := bufio.NewScanner(clientR)
		scan.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for scan.Scan() {
			var f frame
			if json.Unmarshal(scan.Bytes(), &f) != nil {
				continue
			}
			switch {
			case f.Method != "" && len(f.ID) > 0:
				c.requests <- f
			case f.Method != "":
				c.updates <- f
			default:
				c.responses <- f
			}
		}
		close(c.responses)
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

// respond answers an agent→client request frame.
func (c *testClient) respond(req frame, result string) {
	line := `{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + result + `}` + "\n"
	_, err := c.w.Write([]byte(line))
	require.NoError(c.t, err)
}

// waitResponse blocks until the response for id arrives, then returns it plus
// every session/update notification received so far (the server writes updates
// before the response, so they are already queued).
func (c *testClient) waitResponse(id int) (frame, []frame) {
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-c.responses:
			if !ok {
				c.t.Fatal("connection closed before response")
			}
			if string(f.ID) == strconv.Itoa(id) {
				return f, c.drainUpdates()
			}
		case <-deadline:
			c.t.Fatal("timed out waiting for response")
		}
	}
}

// waitRequest blocks until an agent→client request with the given method arrives.
func (c *testClient) waitRequest(method string) frame {
	select {
	case f := <-c.requests:
		require.Equal(c.t, method, f.Method)
		return f
	case <-time.After(10 * time.Second):
		c.t.Fatalf("timed out waiting for agent request %q", method)
		return frame{}
	}
}

func (c *testClient) drainUpdates() []frame {
	var out []frame
	for {
		select {
		case f := <-c.updates:
			out = append(out, f)
		default:
			return out
		}
	}
}

// handshake runs initialize + session/new and returns the session id.
func (c *testClient) handshake(cwd string) string {
	c.t.Helper()
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(c.t, resp.Error)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"`+cwd+`","mcpServers":[]}`))
	require.Nil(c.t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(c.t, json.Unmarshal(resp.Result, &newResp))
	require.NotEmpty(c.t, newResp.SessionId)
	return newResp.SessionId
}

// TestServe_FullTurn drives initialize → session/new → session/prompt against
// a scripted engine: the context lands as the first turn's lead block, entries
// stream back as session/update, and the turn ends with stopReason end_turn.
func TestServe_FullTurn(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(ctx context.Context, req OpenRequest) (*EngineChat, error) {
		assert.Equal(t, "/proj", req.Cwd)
		return eng.chat("PROJECT CONTEXT"), nil
	})

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"protocolVersion"`)
	assert.Contains(t, string(resp.Result), `"loadSession":true`)

	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	require.NotEmpty(t, newResp.SessionId)

	// Script the turn: when the message arrives, stream two entries + complete.
	go func() {
		msg := eng.receivedText(t)
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
		msg := eng.receivedText(t)
		assert.Equal(t, "again", msg, "context rides only the first turn")
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"again"}]}`))
	require.Nil(t, resp.Error)
}

// TestServe_HarpSessionID: the engine's harp becomes the ACP session id — the
// property that makes sessions addressable by session/load later.
func TestServe_HarpSessionID(t *testing.T) {
	eng := newFakeEngine()
	eng.harp = "tidy-mock-harp"
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	sid := c.handshake("/proj")
	assert.Equal(t, "tidy-mock-harp", sid)
}

// TestServe_CancelMidTurn: session/cancel cancels the in-flight TURN — the
// engine receives a CancelTurn message, the prompt resolves with stopReason
// cancelled (the spec's REQUIRED post-cancel stop reason), and the SAME
// session keeps working for the next prompt.
func TestServe_CancelMidTurn(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"slow"}]}`)
	eng.receivedText(t) // the turn is in flight
	c.notify("session/cancel", `{"sessionId":"`+sid+`"}`)

	// The engine sees the per-turn cancel and completes the turn as cancelled.
	msg := eng.receiveMsg(t)
	assert.True(t, msg.CancelTurn, "the engine must receive the per-turn cancel")
	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "cancelled"}}

	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error, "cancel must resolve the prompt, not error it")
	assert.Contains(t, string(resp.Result), `"cancelled"`)

	// The session survived: another turn runs normally.
	go func() {
		eng.receivedText(t)
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "back"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	resp, updates := c.waitResponse(c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"more"}]}`))
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"end_turn"`)
	require.Len(t, updates, 1)
	assert.Contains(t, string(updates[0].Params), "back")
}

// TestServe_CancelRace: even if the engine completes the turn normally after a
// session/cancel (the cancel raced the completion), the prompt still resolves
// with stopReason cancelled — the spec REQUIRES it.
func TestServe_CancelRace(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"quick"}]}`)
	eng.receivedText(t)
	c.notify("session/cancel", `{"sessionId":"`+sid+`"}`)
	eng.receiveMsg(t) // the CancelTurn arrives, but the engine already finished:
	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}

	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"cancelled"`)
}

// TestServe_ToolCallPairing: tool_use/tool_result pair up via generated
// toolCallIds (FIFO per tool name).
func TestServe_ToolCallPairing(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	go func() {
		eng.receivedText(t)
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "fs_read", ToolInput: json.RawMessage(`{"path":"x"}`)}}
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: "fs_read", ToolOutput: "data"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{}}
	}()
	_, updates := c.waitResponse(c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"use a tool"}]}`))
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

// TestServe_PermissionForwarding: an engine permission request forwards to the
// client as session/request_permission (options verbatim, toolCall referencing
// the open tool call), and the client's selected option rides back into the
// engine as the permission answer.
func TestServe_PermissionForwarding(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"do something risky"}]}`)
	eng.receivedText(t)

	// The engine announces the tool call, then asks permission for it.
	eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "shell", ToolInput: json.RawMessage(`{"cmd":"rm"}`)}}
	eng.events <- agent.ChatEvent{Permission: &agent.PermissionRequest{
		ID:        "perm-9",
		ToolName:  "shell",
		ToolInput: json.RawMessage(`{"cmd":"rm"}`),
		Options: []agent.PermissionOption{
			{ID: "ok", Kind: "allow_once", Name: "Allow"},
			{ID: "no", Kind: "reject_once", Name: "Reject"},
		},
	}}

	permReq := c.waitRequest("session/request_permission")
	var pr struct {
		SessionId string `json:"sessionId"`
		Options   []struct {
			OptionId string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
		ToolCall struct {
			ToolCallId string `json:"toolCallId"`
			Title      string `json:"title"`
		} `json:"toolCall"`
	}
	require.NoError(t, json.Unmarshal(permReq.Params, &pr))
	assert.Equal(t, sid, pr.SessionId)
	require.Len(t, pr.Options, 2)
	assert.Equal(t, "ok", pr.Options[0].OptionId)
	assert.Equal(t, "allow_once", pr.Options[0].Kind)
	assert.Equal(t, "shell", pr.ToolCall.Title)
	assert.Equal(t, "call-1", pr.ToolCall.ToolCallId, "the permission references the announced open tool call")

	c.respond(permReq, `{"outcome":{"outcome":"selected","optionId":"ok"}}`)

	// The decision reaches the engine as a permission answer.
	msg := eng.receiveMsg(t)
	require.NotNil(t, msg.Permission)
	assert.Equal(t, "perm-9", msg.Permission.ID)
	assert.Equal(t, "ok", msg.Permission.OptionID)

	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
}

// TestServe_PermissionForwarding_ClientError: a failed client call resolves as
// a dismissed answer (empty option) so the engine unparks with a cancelled
// outcome rather than hanging.
func TestServe_PermissionForwarding_ClientError(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"risky"}]}`)
	eng.receivedText(t)
	eng.events <- agent.ChatEvent{Permission: &agent.PermissionRequest{ID: "perm-1", ToolName: "shell"}}

	permReq := c.waitRequest("session/request_permission")
	line := `{"jsonrpc":"2.0","id":` + string(permReq.ID) + `,"error":{"code":-32603,"message":"nope"}}` + "\n"
	_, err := c.w.Write([]byte(line))
	require.NoError(t, err)

	msg := eng.receiveMsg(t)
	require.NotNil(t, msg.Permission)
	assert.Equal(t, "perm-1", msg.Permission.ID)
	assert.Empty(t, msg.Permission.OptionID, "a failed forward dismisses, never approves")

	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
}

// TestServe_McpServersPassThrough: client-supplied mcpServers at session/new
// reach the opener (env list → map).
func TestServe_McpServersPassThrough(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	opened := make(chan OpenRequest, 1)
	c := startServer(t, func(_ context.Context, req OpenRequest) (*EngineChat, error) {
		opened <- req
		return eng.chat(""), nil
	})

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/new",
		`{"cwd":"/proj","mcpServers":[{"name":"tools","command":"/bin/tools","args":["serve"],"env":[{"name":"K","value":"V"}]}]}`))
	require.Nil(t, resp.Error)

	req := <-opened
	require.Len(t, req.MCPServers, 1)
	assert.Equal(t, "tools", req.MCPServers[0].Name)
	assert.Equal(t, "/bin/tools", req.MCPServers[0].Command)
	assert.Equal(t, []string{"serve"}, req.MCPServers[0].Args)
	assert.Equal(t, map[string]string{"K": "V"}, req.MCPServers[0].Env)
}

// TestServe_SessionModes: profiles surface as modes in the session/new
// response; session/set_mode re-assembles the lead context (it rides the next
// prompt), updates the current mode, and notifies current_mode_update.
func TestServe_SessionModes(t *testing.T) {
	eng := newFakeEngine()
	eng.modes = &SessionModes{
		Current: DefaultModeID,
		Available: []SessionMode{
			{ID: DefaultModeID, Name: "default (base)"},
			{ID: "review", Name: "review"},
		},
	}
	assembled := make(chan string, 1)
	eng.assembleProfile = func(_ context.Context, profile string) (string, error) {
		assembled <- profile
		return "REVIEW CONTEXT", nil
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat("BASE CONTEXT"), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
		Modes     struct {
			CurrentModeId  string `json:"currentModeId"`
			AvailableModes []struct {
				Id   string `json:"id"`
				Name string `json:"name"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	assert.Equal(t, DefaultModeID, newResp.Modes.CurrentModeId)
	require.Len(t, newResp.Modes.AvailableModes, 2)
	assert.Equal(t, "review", newResp.Modes.AvailableModes[1].Id)

	// Switch mode BEFORE the first turn: the new profile's context replaces the
	// initial lead block. The current_mode_update notification is written
	// before the reply, so it arrives with the response's update batch.
	resp, updates := c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+newResp.SessionId+`","modeId":"review"}`))
	require.Nil(t, resp.Error)
	assert.Equal(t, "review", <-assembled)

	require.Len(t, updates, 1)
	assert.Contains(t, string(updates[0].Params), `"current_mode_update"`)
	assert.Contains(t, string(updates[0].Params), `"review"`)

	go func() {
		msg := eng.receivedText(t)
		assert.Equal(t, "REVIEW CONTEXT\n\nhello", msg, "the switched mode's context rides the next turn")
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"hello"}]}`))
	require.Nil(t, resp.Error)

	// An unknown mode errors.
	resp, _ = c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+newResp.SessionId+`","modeId":"nope"}`))
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "unknown mode")
}

// TestServe_SessionLoad: session/load replays the recorded history (user
// messages included — unlike live events) BEFORE the response, registers the
// session under the loaded id, and the session then prompts normally with the
// resume-primed lead context.
func TestServe_SessionLoad(t *testing.T) {
	eng := newFakeEngine()
	eng.replay = []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "earlier question"},
		{Type: agent.EntryTypeAssistant, Content: "earlier answer"},
	}
	go eng.pump()
	opened := make(chan OpenRequest, 1)
	c := startServer(t, func(_ context.Context, req OpenRequest) (*EngineChat, error) {
		opened <- req
		return eng.chat("RESUME PRIMED"), nil
	})

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	id := c.send("session/load", `{"sessionId":"tidy-old-harp","cwd":"/proj","mcpServers":[]}`)
	resp, updates := c.waitResponse(id)
	require.Nil(t, resp.Error)

	req := <-opened
	assert.Equal(t, "tidy-old-harp", req.ResumeHarp)
	assert.Equal(t, "/proj", req.Cwd)

	require.Len(t, updates, 2, "the recorded history replays before the load response")
	assert.Contains(t, string(updates[0].Params), `"user_message_chunk"`)
	assert.Contains(t, string(updates[0].Params), "earlier question")
	assert.Contains(t, string(updates[1].Params), `"agent_message_chunk"`)
	assert.Contains(t, string(updates[1].Params), "earlier answer")

	// The loaded session prompts normally under its harp id.
	go func() {
		msg := eng.receivedText(t)
		assert.Equal(t, "RESUME PRIMED\n\ncontinue please", msg)
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"tidy-old-harp","prompt":[{"type":"text","text":"continue please"}]}`))
	require.Nil(t, resp.Error)
}

// TestServe_SessionLoad_OpenError: a failed resume (unknown harp) surfaces as
// a JSON-RPC error on session/load.
func TestServe_SessionLoad_OpenError(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) {
		return nil, assert.AnError
	})
	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/load", `{"sessionId":"no-such-harp","cwd":"/proj","mcpServers":[]}`))
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "open ctxloom session")
}
