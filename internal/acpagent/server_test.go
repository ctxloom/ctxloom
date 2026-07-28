package acpagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
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
	harp         string
	modes        *SessionModes
	assembleMode func(ctx context.Context, mode SessionMode) (string, error)
	replay       []agent.SessionEntry
	llms         *SessionLLMs
	commands     *SessionCommands
	initSummary  string
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
		Harp:         f.harp,
		Modes:        f.modes,
		AssembleMode: f.assembleMode,
		Replay:       f.replay,
		LLMs:         f.llms,
		Commands:     f.commands,
		InitSummary:  f.initSummary,
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

// TestServe_ContextNotMarkedSentUntilEngineReceivesIt pins U014-F01: the
// server used to commit sess.contextSent = true synchronously in
// handlePrompt, BEFORE the turn's message was actually handed to the engine
// in runTurn's own select. A turn that lost that race — cancelled before the
// engine ever read it — still marked the lead context "sent", silently
// losing it for the rest of the session with no error anywhere.
//
// Reproduced deterministically, not by timing luck: the engine's In channel
// is UNBUFFERED and nothing ever reads it for the first turn, so runTurn's
// send can never become ready; session/cancel is sent once the turn is
// registered, so cancelTurnCh is the only case that CAN become ready — no
// race, just a guaranteed outcome.
func TestServe_ContextNotMarkedSentUntilEngineReceivesIt(t *testing.T) {
	eng := &fakeEngine{
		in:       make(chan agent.ChatMessage), // unbuffered, deliberately unread below
		events:   make(chan agent.ChatEvent, 16),
		errs:     make(chan error, 1),
		received: make(chan agent.ChatMessage, 8),
		closed:   make(chan struct{}),
	}
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) {
		return eng.chat("LEAD CONTEXT"), nil
	})
	sid := c.handshake("/proj")

	id1 := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"first"}]}`)
	// The turn is now registered (inTurn=true, synchronously, before
	// handlePrompt returned) but its message CANNOT have reached the engine —
	// nothing reads eng.in yet. Cancel it before it ever could.
	c.notify("session/cancel", `{"sessionId":"`+sid+`"}`)

	resp, _ := c.waitResponse(id1)
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"cancelled"`, "the lost-the-race turn must report cancelled, not a fabricated success")

	// The lead context must NOT have been consumed by that lost turn: start
	// reading eng.in now (as the second turn's delivery path) and prove the
	// SECOND attempt still carries "LEAD CONTEXT" — proof the first turn never
	// actually delivered it, so contextSent was never wrongly committed.
	go eng.pump()
	go func() {
		msg := eng.receivedText(t)
		assert.Equal(t, "LEAD CONTEXT\n\nsecond", msg, "a turn that lost the race to deliver must not have spent the lead context")
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"second"}]}`))
	require.Nil(t, resp.Error)
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

// TestServe_SessionModes: profile sets surface as modes in the session/new
// response; session/set_mode re-assembles the lead context (it rides the next
// prompt), updates the current mode, and notifies current_mode_update. A
// agent mode threads its whole composed profile set into the assembly.
func TestServe_SessionModes(t *testing.T) {
	eng := newFakeEngine()
	eng.modes = &SessionModes{
		Current: DefaultModeID,
		Available: []SessionMode{
			{ID: DefaultModeID, Name: "default (base)"},
			{ID: "review", Name: "review", Profiles: []string{"review"}},
			{ID: "agent:reviewer", Name: "reviewer (agent)", Profiles: []string{"r1", "r2"}, Engine: "fast"},
		},
	}
	assembled := make(chan SessionMode, 1)
	eng.assembleMode = func(_ context.Context, mode SessionMode) (string, error) {
		assembled <- mode
		return strings.ToUpper(mode.ID) + " CONTEXT", nil
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
	require.Len(t, newResp.Modes.AvailableModes, 3)
	assert.Equal(t, "review", newResp.Modes.AvailableModes[1].Id)
	assert.Equal(t, "agent:reviewer", newResp.Modes.AvailableModes[2].Id)
	assert.Equal(t, "reviewer (agent)", newResp.Modes.AvailableModes[2].Name)

	// Switch mode BEFORE the first turn: the new mode's context replaces the
	// initial lead block. The current_mode_update notification is written
	// before the reply, so it arrives with the response's update batch.
	resp, updates := c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+newResp.SessionId+`","modeId":"review"}`))
	require.Nil(t, resp.Error)
	mode := <-assembled
	assert.Equal(t, "review", mode.ID)
	assert.Equal(t, []string{"review"}, mode.Profiles)

	// CO1: switchProfile notifies BOTH surfaces on every switch (COMPAT —
	// current_mode_update alongside a configOptionUpdate reflecting the same
	// change), regardless of which method triggered it.
	require.Len(t, updates, 2)
	assert.Contains(t, string(updates[0].Params), `"current_mode_update"`)
	assert.Contains(t, string(updates[0].Params), `"review"`)
	assert.Contains(t, string(updates[1].Params), `"config_option_update"`)
	assert.Contains(t, string(updates[1].Params), `"review"`)

	go func() {
		msg := eng.receivedText(t)
		assert.Equal(t, "REVIEW CONTEXT\n\nhello", msg, "the switched mode's context rides the next turn")
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	resp, _ = c.waitResponse(c.send("session/prompt", `{"sessionId":"`+newResp.SessionId+`","prompt":[{"type":"text","text":"hello"}]}`))
	require.Nil(t, resp.Error)

	// An agent mode threads its composed profile set (and declared engine)
	// into the assembly.
	resp, _ = c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+newResp.SessionId+`","modeId":"agent:reviewer"}`))
	require.Nil(t, resp.Error)
	mode = <-assembled
	assert.Equal(t, []string{"r1", "r2"}, mode.Profiles, "the agent's whole composed set reaches the assembler")
	assert.Equal(t, "fast", mode.Engine)

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

// TestPromptText_ResourceBlocks: promptText inlines `resource` (embedded text,
// labeled by uri) and renders `resource_link` as a labeled reference line, so
// "add context" content reaches the engine. Text passes through; a binary blob
// resource (no text) has no text projection but (B2) is no longer a SILENT
// drop — it renders a visible placeholder naming what arrived instead.
func TestPromptText_ResourceBlocks(t *testing.T) {
	raw := `[
		{"type":"text","text":"look at this"},
		{"type":"resource","resource":{"uri":"file:///a.go","text":"package main","mimeType":"text/x-go"}},
		{"type":"resource_link","name":"spec","uri":"file:///spec.md","title":"Design Spec","description":"the plan"},
		{"type":"resource","resource":{"uri":"file:///bin","blob":"AAAA","mimeType":"application/octet-stream"}}
	]`
	var blocks []api.ContentBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &blocks))

	got := promptText(blocks)
	assert.Contains(t, got, "look at this", "text block passes through")
	assert.Contains(t, got, "package main", "embedded resource text is inlined")
	assert.Contains(t, got, "file:///a.go", "the embedded resource is labeled by its uri")
	assert.Contains(t, got, "file:///spec.md", "the resource link references its uri")
	assert.Contains(t, got, "Design Spec", "the resource link uses its title as the label")
	assert.NotContains(t, got, "AAAA", "raw base64 blob bytes never render as-is")
	assert.Contains(t, got, "file:///bin", "B2: a binary blob resource is no longer a silent drop — its uri is named in a visible placeholder")
	assert.Contains(t, got, "application/octet-stream", "the placeholder names the blob's mime type")
}

// TestPromptText_MediaBlocks_NeverSilentlyDropped: B2's cross-backend safety
// net — image/audio blocks have no text projection, but the flattened Text
// every native backend (claude/codex/kiro/opencode) reads must still show
// SOMETHING arrived, never nothing (this codebase's signature failure mode).
func TestPromptText_MediaBlocks_NeverSilentlyDropped(t *testing.T) {
	raw := `[
		{"type":"text","text":"what is this"},
		{"type":"image","data":"aGVsbG8=","mimeType":"image/png"},
		{"type":"audio","data":"d29ybGQ=","mimeType":"audio/wav"}
	]`
	var blocks []api.ContentBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &blocks))

	got := promptText(blocks)
	assert.Contains(t, got, "what is this")
	assert.Contains(t, got, "image content received", "an image is never silently omitted from the flattened text")
	assert.Contains(t, got, "image/png", "the placeholder names the mime type")
	assert.Contains(t, got, "audio content received", "an audio block is never silently omitted either")
	assert.Contains(t, got, "audio/wav")
	assert.NotContains(t, got, "aGVsbG8=", "raw base64 image bytes never render as text")
}

// TestHandlePrompt_ContentBlocksPayload: B2's structural intake payload
// proof — an image block in session/prompt reaches the engine via
// agent.ChatMessage.ContentBlocks losslessly (Raw carries the full original
// bytes), not just as a flattened text placeholder. This is the payload
// behind promptCapabilities.image/audio: true.
func TestHandlePrompt_ContentBlocksPayload(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	promptJSON := `{"sessionId":"` + sid + `","prompt":[
		{"type":"text","text":"describe this"},
		{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}
	]}`
	id := c.send("session/prompt", promptJSON)

	msg := eng.receiveMsg(t)
	assert.Contains(t, msg.Text, "image content received", "the flattened Text fallback still shows a visible placeholder")
	require.Len(t, msg.ContentBlocks, 2, "text + image, carried structurally")
	assert.Equal(t, "text", msg.ContentBlocks[0].Kind)
	assert.Equal(t, "describe this", msg.ContentBlocks[0].Text)
	assert.Equal(t, "image", msg.ContentBlocks[1].Kind)
	assert.Contains(t, string(msg.ContentBlocks[1].Raw), "aGVsbG8=", "the image's REAL base64 data rides Raw losslessly — no flattening at the intake layer")
	assert.Contains(t, string(msg.ContentBlocks[1].Raw), "image/png")

	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
}

// TestServe_UsageAndSessionInfo: the engine's one-time ChatSessionInfo is
// DECOMPOSED across three spec-shaped channels (G13, correcting IR4's
// whole-blob-in-`_meta` answer): Model rides CO1's SessionConfigOption
// ("model" category — proved with a payload assertion in
// TestL0_AgentEmittedFrames, since it requires SessionLLMs to be configured
// and this fakeEngine has none), only PermissionMode/MCPServers ride the
// REAL "session_info_update" frame's `_meta` object (see
// internal/acpagent/wire.go's ctxloomSessionInfoUpdate doc comment for the
// full per-fact evidence), and a turn's completion accounting rides ahead of
// the prompt response as a usage_update (context gauge + cost) — which
// already carries ContextWindow, so it is NOT duplicated into `_meta`
// either.
func TestServe_UsageAndSessionInfo(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	go func() {
		eng.receivedText(t)
		// Model/ContextWindow are still populated here (a real backend
		// reports them) to prove the WIRE layer drops them from `_meta`
		// deliberately, not because the engine never reported them.
		eng.events <- agent.ChatEvent{Session: &agent.ChatSessionInfo{
			Model:          "claude-sonnet",
			PermissionMode: "default",
			ContextWindow:  200000,
			MCPServers:     []agent.MCPStatus{{Name: "tools", Status: "connected"}},
		}}
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{InputTokens: 53000, ContextWindow: 200000, CostUSD: 0.045, StopReason: "end_turn"}}
	}()

	resp, updates := c.waitResponse(c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"hello"}]}`))
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"end_turn"`)

	// session_info_update, the assistant chunk, then usage_update (ahead of the reply).
	require.Len(t, updates, 3)
	var joined string
	for _, u := range updates {
		joined += string(u.Params)
	}
	assert.Contains(t, joined, `"sessionUpdate":"session_info_update"`, "IR4: the REAL spec discriminator, not ctxloom's old bespoke top-level name")
	assert.NotContains(t, joined, `"sessionUpdate":"ctxloom_session_info"`, "the old (schema-invalid) top-level variant name must be gone")
	assert.Contains(t, joined, `"_meta":{"ctxloom_session_info"`, "ctxloom's own header rides the spec's sanctioned _meta extension channel, not the frame's identity")
	assert.Contains(t, joined, `"permissionMode":"default"`, "PermissionMode has no spec home — it stays in _meta")
	assert.Contains(t, joined, "connected")
	assert.NotContains(t, joined, "claude-sonnet", "G13: Model no longer duplicates into _meta")
	assert.Contains(t, joined, `"usage_update"`)
	assert.Contains(t, joined, `"used":53000`)
	assert.Contains(t, joined, `"size":200000`, "ContextWindow rides usage_update's size, not _meta")
	assert.Contains(t, joined, `"amount":0.045`)
}

// TestServe_UsageOmittedWhenEmpty: a completion with no accounting emits no
// usage_update (the gauge would be meaningless), so a bare turn streams only
// its content updates.
func TestServe_UsageOmittedWhenEmpty(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	go func() {
		eng.receivedText(t)
		eng.events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}}
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()

	resp, updates := c.waitResponse(c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"hello"}]}`))
	require.Nil(t, resp.Error)
	require.Len(t, updates, 1, "only the assistant chunk — no usage_update for an empty completion")
	assert.NotContains(t, string(updates[0].Params), "usage_update")
}

// TestServe_AdvertisesLLMs: a session advertising LLMs surfaces them in the
// session/new response as a models state (availableModels + currentModelId).
func TestServe_AdvertisesLLMs(t *testing.T) {
	eng := newFakeEngine()
	eng.llms = &SessionLLMs{
		Current:   "fast",
		Available: []LLMInfo{{ID: "primary", Name: "primary"}, {ID: "fast", Name: "fast"}},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	var newResp struct {
		Models struct {
			CurrentModelId  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelId string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	assert.Equal(t, "fast", newResp.Models.CurrentModelId)
	require.Len(t, newResp.Models.AvailableModels, 2)
	assert.Equal(t, "primary", newResp.Models.AvailableModels[0].ModelId)
	assert.Equal(t, "fast", newResp.Models.AvailableModels[1].ModelId)
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

// TestServe_Initialize_AdvertisesCapabilitiesTruthfully pins the EXACT set of
// capabilities ctxloom's agent role claims today: loadSession + embedded
// context + mcp http/sse (B3, gap G11 — mcpServersFromACP now accepts an
// editor-supplied http/sse server; see handleInitialize's doc comment for
// what happens when the SESSION'S chosen engine can't actually take one).
// mcp acp and image/audio/session-management stay false — a client reading
// this response must never be told about support ctxloom does not actually
// have (see handleInitialize's doc comment for why each is true or false
// today).
func TestServe_Initialize_AdvertisesCapabilitiesTruthfully(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return nil, assert.AnError })
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)

	var got api.InitializeResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))

	assert.EqualValues(t, 1, got.ProtocolVersion)
	assert.True(t, got.AgentCapabilities.LoadSession)
	assert.True(t, got.AgentCapabilities.PromptCapabilities.EmbeddedContext)
	assert.True(t, got.AgentCapabilities.PromptCapabilities.Image, "B2 landed: handlePrompt carries image blocks through structurally (contentBlocksFromACP) instead of dropping them")
	assert.True(t, got.AgentCapabilities.PromptCapabilities.Audio, "B2 landed: handlePrompt carries audio blocks through structurally instead of dropping them")
	assert.False(t, got.AgentCapabilities.McpCapabilities.Acp, "ACP-transport MCP servers have no seam yet — mcpServersFromACP rejects them loudly rather than forwarding a server that could never connect")
	assert.True(t, got.AgentCapabilities.McpCapabilities.Http, "B3 (gap G11): mcpServersFromACP now accepts an editor-supplied HTTP MCP server")
	assert.True(t, got.AgentCapabilities.McpCapabilities.Sse, "B3 (gap G11): mcpServersFromACP now accepts an editor-supplied SSE MCP server")
	assert.Nil(t, got.AgentCapabilities.SessionCapabilities.Close)
	assert.Nil(t, got.AgentCapabilities.SessionCapabilities.Delete)
	assert.Nil(t, got.AgentCapabilities.SessionCapabilities.List)
	assert.Nil(t, got.AgentCapabilities.SessionCapabilities.Resume)
	assert.Nil(t, got.AgentCapabilities.Auth.Logout, "ctxloom needs no authentication today")
	assert.NotNil(t, got.AuthMethods, "authMethods must be a wire-present [] , never a bare omitted/null field")
	assert.Empty(t, got.AuthMethods)

	// Payload assertion: the raw wire bytes actually carry these claims, not
	// just the decoded Go struct (a decoder default could otherwise mask a
	// missing field).
	raw := string(resp.Result)
	assert.Contains(t, raw, `"embeddedContext":true`)
	assert.Contains(t, raw, `"image":true`)
	assert.Contains(t, raw, `"audio":true`)
	assert.Contains(t, raw, `"authMethods":[]`)
	assert.Contains(t, raw, `"http":true`)
	assert.Contains(t, raw, `"sse":true`)
}

// TestServe_Initialize_VersionNegotiation: a client offering a NEWER protocol
// version than ctxloom speaks gets min(clientVersion, ours) back, never the
// client's own (unspeakable) version echoed as if agreed to.
func TestServe_Initialize_VersionNegotiation(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return nil, assert.AnError })
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":5,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	var got api.InitializeResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.EqualValues(t, 1, got.ProtocolVersion, "min(5, ours=1) == 1")
}

// TestServe_Initialize_RefusesBelowFloor: a client below the version floor
// (there is no lower integer version this codebase speaks — see
// protocolFloor) is refused with a clean, named error, never silently
// answered as if a real negotiation happened.
func TestServe_Initialize_RefusesBelowFloor(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return nil, assert.AnError })
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":0,"clientCapabilities":{}}`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "unsupported protocolVersion 0")
	assert.Contains(t, resp.Error.Message, "ctxloom speaks ACP protocol version 1")
}

// TestServe_Authenticate_AnswersCleanly: authenticate is a CORE ACP method
// and must exist and answer — never the generic method-not-found a truly
// unrecognized method gets. ctxloom advertises authMethods: [], so any
// methodId here is invalid; the error must say so specifically.
func TestServe_Authenticate_AnswersCleanly(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return nil, assert.AnError })
	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("authenticate", `{"methodId":"nope"}`))
	require.NotNil(t, resp.Error)
	assert.NotEqual(t, jsonrpc.CodeMethodNotFound, resp.Error.Code, "must not be indistinguishable from a truly-unrecognized method")
	assert.Contains(t, resp.Error.Message, "no auth methods are configured")
	assert.Contains(t, resp.Error.Message, "nope")
}

// TestServe_Logout_AnswersCleanly: logout is likewise a CORE ACP method that
// must exist and answer — ctxloom never advertises auth.logout and never
// authenticates, so it answers honestly rather than method-not-found.
func TestServe_Logout_AnswersCleanly(t *testing.T) {
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return nil, assert.AnError })
	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("logout", `{}`))
	require.NotNil(t, resp.Error)
	assert.NotEqual(t, jsonrpc.CodeMethodNotFound, resp.Error.Code, "must not be indistinguishable from a truly-unrecognized method")
	assert.Contains(t, resp.Error.Message, "does not support authentication")
}

// TestMcpServersFromACP_HttpSse: an editor-supplied http/sse McpServer (B3,
// gap G11) is accepted and carried onward as an agent.ChatMCPServer with
// Transport/URL/Headers set — proving the editor's server actually reaches
// ctxloom's internal shape, not just that initialize claims support.
func TestMcpServersFromACP_HttpSse(t *testing.T) {
	got := mcpServersFromACP([]api.McpServer{
		{Stdio: &api.McpServerStdio{Name: "local", Command: "/bin/x", Args: []string{"a"}}},
		{Http: &api.McpServerHttpInline{Name: "remote-http", Url: "https://example.com/mcp", Headers: []api.HttpHeader{{Name: "Authorization", Value: "Bearer tok"}}}},
		{Sse: &api.McpServerSseInline{Name: "remote-sse", Url: "https://example.com/sse"}},
	})
	require.Len(t, got, 3)
	assert.Equal(t, agent.ChatMCPServer{Name: "local", Command: "/bin/x", Args: []string{"a"}}, got[0])
	assert.Equal(t, agent.ChatMCPServer{Name: "remote-http", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}}, got[1])
	assert.Equal(t, agent.ChatMCPServer{Name: "remote-sse", Transport: agent.MCPTransportSSE, URL: "https://example.com/sse"}, got[2])
}

// TestMcpServersFromACP_RejectsUnreachableVariants: an McpServer::Acp entry
// (no seam ctxloom can actually reach) and a malformed entry with no variant
// set at all are BOTH dropped rather than forwarded — never silently: they
// simply do not appear in the output (see mcpServersFromACP's doc comment
// for the clidiag.Warn side of this, which is stderr-only and not unit
// tested here, but the functional guarantee — nothing broken reaches the
// engine — is what this test pins).
func TestMcpServersFromACP_RejectsUnreachableVariants(t *testing.T) {
	got := mcpServersFromACP([]api.McpServer{
		{Acp: &api.McpServerAcpInline{Name: "acp-server"}},
		{}, // no variant set at all
		{Stdio: &api.McpServerStdio{Name: "kept", Command: "/bin/x"}},
	})
	require.Len(t, got, 1, "only the valid stdio entry should survive")
	assert.Equal(t, "kept", got[0].Name)
}
