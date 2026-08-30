package acpagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// writeFrame puts one frame on the wire ACP actually specifies: newline-
// delimited JSON, one frame per line. Tests here compose params as readable
// multi-line string literals, so the assembled frame is COMPACTED before it is
// written — an embedded newline does not make a frame prettier on this wire,
// it splits it into several unparseable ones, which is what a real peer would
// make of it too.
func (c *testClient) writeFrame(frame string) {
	c.t.Helper()
	var line bytes.Buffer
	require.NoError(c.t, json.Compact(&line, []byte(frame)), "the test composed a frame that is not JSON")
	line.WriteByte('\n')
	_, err := c.w.Write(line.Bytes())
	require.NoError(c.t, err)
}

func (c *testClient) send(method string, params string) int {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.writeFrame(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + `}`)
	return id
}

func (c *testClient) notify(method string, params string) {
	c.t.Helper()
	c.writeFrame(`{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `}`)
}

// respond answers an agent→client request frame.
func (c *testClient) respond(req frame, result string) {
	c.t.Helper()
	c.writeFrame(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + result + `}`)
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

// TestServe_ContextNotMarkedSentUntilEngineReceivesIt pins that the
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

// TestServe_PermissionForwarding_ClientError: when the connected client
// answers session/request_permission with a protocol error instead of an
// outcome — a client that is reachable but could not (or would not) actually
// decide — the request is never silently dropped (RULED 2026-08-30, "queue
// and record; fail loud if unanswerable"): the still-connected client is
// told, via a session/update, that a decision was needed and could not be
// made. The engine still unparks promptly with a dismissed answer (empty
// option) — refusing the action rather than hanging the turn waiting on an
// answer that will never come; see recordUnansweredPermission's doc.
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

	// The unanswerable request must be RECORDED and SURFACED to the
	// still-connected client — not just a dismissed answer with no trace.
	notice := c.waitUpdate()
	assert.Contains(t, string(notice.Params), `"agent_message_chunk"`)
	assert.Contains(t, string(notice.Params), "shell", "the notice names the tool the unanswered request was for")
	assert.Contains(t, string(notice.Params), "could not be delivered", "the notice says a decision was needed and never made, not just that the turn continued")

	msg := eng.receiveMsg(t)
	require.NotNil(t, msg.Permission)
	assert.Equal(t, "perm-1", msg.Permission.ID)
	assert.Empty(t, msg.Permission.OptionID, "a failed forward still refuses the action, never approves")

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
		Current: operations.DefaultModeID,
		Available: []SessionMode{
			{ID: operations.DefaultModeID, Name: "default (base)"},
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
	assert.Equal(t, operations.DefaultModeID, newResp.Modes.CurrentModeId)
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

// TestServe_SessionLoad_ReplaysNothingWarnsVisibly pins that a recorded
// history whose every entry maps to ZERO session/update frames (here: a
// user entry with empty Content, which replayEntry explicitly drops) used to
// let session/load reply success having replayed nothing — the editor sees
// an empty transcript for a session that is NOT actually empty, with no
// indication anything was lost. It must now say so, visibly, before the
// load response.
func TestServe_SessionLoad_ReplaysNothingWarnsVisibly(t *testing.T) {
	eng := newFakeEngine()
	eng.replay = []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: ""}, // replayEntry maps this to nil
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	id := c.send("session/load", `{"sessionId":"tidy-old-harp","cwd":"/proj","mcpServers":[]}`)
	resp, updates := c.waitResponse(id)
	require.Nil(t, resp.Error)

	var sawWarning bool
	for _, u := range updates {
		if strings.Contains(string(u.Params), "agent_message_chunk") && strings.Contains(string(u.Params), "1 recorded history entries") {
			sawWarning = true
		}
	}
	assert.True(t, sawWarning, "session/load must visibly warn the editor when a non-empty recorded history replayed zero frames, got updates: %v", updates)
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
		Available: []operations.LLMInfo{{ID: "primary", Name: "primary"}, {ID: "fast", Name: "fast"}},
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

// TestServe_CancelWithUndecodableParamsWarns pins that a session/cancel
// notification whose params fail to decode used to return with no trace at
// all, so a user's cancel disappeared silently — while an UNKNOWN method on
// the very same path already warned. A cancel that cannot be decoded is
// exactly the case a human is watching for, so it must be at least as loud as
// a frame nobody sent on purpose.
func TestServe_CancelWithUndecodableParamsWarns(t *testing.T) {
	var sink syncWriter
	restore := clidiag.SetSink(&sink)
	defer restore()

	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	// sessionId is a string on the wire; a number cannot decode.
	c.notify("session/cancel", `{"sessionId":123}`)

	// Order the assertion behind a round-trip the server must answer AFTER
	// the notification it read first, so the warn (if any) has landed.
	resp, _ := c.waitResponse(c.send("session/set_mode", `{"sessionId":"`+sid+`","modeId":"nope"}`))
	require.NotNil(t, resp.Error)

	assert.Contains(t, sink.String(), "session/cancel",
		"an undecodable session/cancel must be reported, not silently dropped")
}

// syncWriter is a mutex-guarded buffer: clidiag's sink is written from the
// server's read-loop goroutine while the test reads it.
type syncWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// errAfterHandshakeWriter fails every write once armed, so a session/update
// notification the server sends AFTER a session is registered fails the way a
// dropped editor connection makes it fail.
type errAfterHandshakeWriter struct {
	mu      sync.Mutex
	failing bool
}

func (w *errAfterHandshakeWriter) arm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failing = true
}

func (w *errAfterHandshakeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failing {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

// TestOpenSession_FailureAfterRegistrationReleasesTheSession pins that a
// step that fails AFTER openSession has published the session into s.sessions
// (the init summary, the commands update, the history replay) replied with an
// error but left the session registered, holding a live engine conversation
// nothing would ever tear down before server exit.
//
// For session/load the id is the caller's own harp, and openSession refuses a
// fixed id that is already live — so the harp became permanently
// unloadable for the rest of the connection: every retry answered "session
// already active" for a session no client had ever been given.
func TestOpenSession_FailureAfterRegistrationReleasesTheSession(t *testing.T) {
	eng := newFakeEngine()
	eng.initSummary = "ISOLATION: none" // makes emitSessionInitSummary actually notify
	go eng.pump()

	w := &errAfterHandshakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{
		open:     func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil },
		ctx:      ctx,
		sessions: make(map[api.SessionId]*session),
	}
	s.conn = jsonrpc.NewConn(strings.NewReader(""), w, nil, s)
	s.conn.Start(ctx)

	w.arm()
	var gotErr *jsonrpc.Error
	s.handleSessionLoad(json.RawMessage(`{"sessionId":"tidy-old-harp","cwd":"/proj","mcpServers":[]}`),
		func(_ any, rerr *jsonrpc.Error) { gotErr = rerr })

	require.NotNil(t, gotErr, "the notification failed, so session/load must fail")
	assert.Nil(t, s.lookup("tidy-old-harp"),
		"a session/load that failed after registration must not keep the harp occupied — the client was never given this session")
}

// TestOpenSession_LostRaceOnFixedIDIsRefusedNotRenamed pins that
// openSession's duplicate check for a FIXED (session/load) id ran before the
// engine was opened, and the registration that followed re-tested the map
// under the lock with a fallback that MINTED a generated "ctxloom-N" id. So a
// session/load that lost the race was registered under an id the caller never
// asked for and was never told about — session/load's response body carries
// no sessionId, so the client goes on addressing the harp, which belongs to
// somebody else's session. Silently answering a different question is never
// better than refusing.
func TestOpenSession_LostRaceOnFixedIDIsRefusedNotRenamed(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{ctx: ctx, sessions: make(map[api.SessionId]*session)}
	// The competing registration lands while THIS open is in flight — exactly
	// the window between the pre-check and the registration.
	s.open = func(context.Context, OpenRequest) (*EngineChat, error) {
		s.mu.Lock()
		s.sessions["tidy-old-harp"] = &session{id: "tidy-old-harp"}
		s.mu.Unlock()
		return eng.chat(""), nil
	}

	sess, rerr := s.openSession(OpenRequest{ResumeHarp: "tidy-old-harp"}, "tidy-old-harp", nil)

	require.NotNil(t, rerr, "the requested harp was taken while we opened — that must be refused")
	assert.Nil(t, sess)
	assert.Contains(t, rerr.Message, "tidy-old-harp")
}

// TestRunTurn_EventsClosedWithoutErrsStillResolvesTheTurn pins that
// when the engine's Events channel closes mid-turn and the turn was not
// cancelled, runTurn replied with engineError(<-sess.engine.Errs) — a BARE
// blocking receive. That is only safe if whoever closed Events also closed
// (or wrote) Errs. The production producer does close both from one defer,
// but that is a coincidence of one implementation, not a contract EngineChat
// states: cmd/acpl1harness's engine closes Events and never touches Errs at
// all. Against a producer like that the receive parks forever, so the
// session/prompt request never resolves and the turn runner leaks — the
// client is left waiting on a reply that can no longer be produced.
func TestRunTurn_EventsClosedWithoutErrsStillResolvesTheTurn(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"do work"}]}`)
	eng.receivedText(t) // the turn is registered and the message delivered

	// A producer that ends the conversation by closing Events alone — no
	// cancel, no session teardown, nothing ever sent on or closing Errs.
	// Marking the fake closed first keeps the harness's own Close a no-op, so
	// the teardown at test exit cannot re-close what we closed here.
	close(eng.closed)
	close(eng.events)

	select {
	case f := <-c.responses:
		require.Equal(t, strconv.Itoa(id), string(f.ID))
	case <-time.After(10 * time.Second):
		t.Fatal("session/prompt never resolved: runTurn is parked on a bare receive from an Errs channel nobody will ever close")
	}
}

// frameFailingWriter fails only the frames whose bytes contain `reject`, and
// writes everything else. jsonrpc.Conn marshals and writes each frame
// independently (writeFrame), with no sticky error, so a per-FRAME failure —
// an unmarshalable payload, a short write — really can drop one notification
// while the reply that follows lands normally.
type frameFailingWriter struct {
	mu     sync.Mutex
	reject string
}

func (w *frameFailingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reject != "" && strings.Contains(string(p), w.reject) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

// TestSwitchProfile_FailedModeNotificationFailsTheRequest pins that
// switchProfile warned and continued when either of its two session/update
// notifications failed, then returned nil — so session/set_mode and
// session/set_config_option answered SUCCESS to a client that never received
// the current_mode_update telling it which profile it is now in. emitUpdate,
// on the very same connection, already treats a failed notification as fatal
// to the request; this path was the sole divergence.
func TestSwitchProfile_FailedModeNotificationFailsTheRequest(t *testing.T) {
	eng := newFakeEngine()
	eng.modes = &SessionModes{
		Current:   operations.DefaultModeID,
		Available: []SessionMode{{ID: operations.DefaultModeID, Name: "default"}, {ID: "review", Name: "review"}},
	}
	eng.assembleMode = func(context.Context, SessionMode) (string, error) { return "REVIEW CTX", nil }
	go eng.pump()

	w := &frameFailingWriter{reject: "current_mode_update"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{
		open:     func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil },
		ctx:      ctx,
		sessions: make(map[api.SessionId]*session),
	}
	s.conn = jsonrpc.NewConn(strings.NewReader(""), w, nil, s)
	s.conn.Start(ctx)

	sess, rerr := s.openSession(OpenRequest{}, "", nil)
	require.Nil(t, rerr)

	var gotErr *jsonrpc.Error
	var replied bool
	s.handleSetMode(json.RawMessage(`{"sessionId":"`+string(sess.id)+`","modeId":"review"}`),
		func(_ any, rerr *jsonrpc.Error) { replied, gotErr = true, rerr })

	require.True(t, replied)
	assert.NotNil(t, gotErr,
		"the client never got the current_mode_update, so set_mode must not answer success")
}

// TestInitialize_AgentInfoVersionIsTheBuildStamp pins that agentVersion
// used to be the constant "1.0.0", reported to every connected editor as
// agentInfo.version: not the build stamp internal/version.Version carries, never
// bumped, and naming a release ctxloom has never made — so the one field the
// ACP handshake reserves for "which build am I talking to" answered with a
// number identifying nothing.
//
// A MISSING-SEAM row (template section 4, class 1): the defect WAS the absent
// injection point, so the honest test could not go red, only fail to compile.
// It landed first as a characterization asserting the frozen "1.0.0" and is
// inverted here now that SetAgentVersion exists.
func TestInitialize_AgentInfoVersionIsTheBuildStamp(t *testing.T) {
	restore := agentVersion
	t.Cleanup(func() { agentVersion = restore })
	SetAgentVersion("v0.7.0-pre1-abcd1234")

	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)

	var got struct {
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "ctxloom", got.AgentInfo.Name)
	assert.Equal(t, "v0.7.0-pre1-abcd1234", got.AgentInfo.Version,
		"the handshake must report the running build, not a frozen literal")
}

// TestSetAgentVersion_EmptyKeepsTheDefault: an unstamped build passes "" and
// must not blank the field out — an absent version is less useful than the
// honest "dev" placeholder, and agentInfo.version is a required string.
func TestSetAgentVersion_EmptyKeepsTheDefault(t *testing.T) {
	restore := agentVersion
	t.Cleanup(func() { agentVersion = restore })
	SetAgentVersion("v1.2.3")
	SetAgentVersion("")
	assert.Equal(t, "v1.2.3", agentVersion)
}

// TestServe_SessionLoad_ResponseBodyCarriesTheSessionState pins that
// session/load's response BODY was asserted nowhere. TestServe_SessionLoad
// checked only that resp.Error was nil and then moved on to the replay
// notifications; TestL1_SessionLoad_ReplaysBeforeResponse discards the raw
// result entirely; and the L0 capture validates it against
// $defs/LoadSessionResponse, which declares no `required` array at all — so
// `{}` is a schema-VALID load response. A regression that stopped populating
// modes, models or configOptions would therefore have passed every gate: the
// resumed session would silently lose its profile switcher and model
// selector in the editor's UI, with a green suite.
func TestServe_SessionLoad_ResponseBodyCarriesTheSessionState(t *testing.T) {
	eng := newFakeEngine()
	eng.replay = []agent.SessionEntry{{Type: agent.EntryTypeAssistant, Content: "earlier answer"}}
	eng.modes = &SessionModes{
		Current:   operations.DefaultModeID,
		Available: []SessionMode{{ID: operations.DefaultModeID, Name: "default"}, {ID: "review", Name: "review"}},
	}
	eng.assembleMode = func(context.Context, SessionMode) (string, error) { return "REVIEW CTX", nil }
	eng.llms = &SessionLLMs{Current: "primary", Available: []operations.LLMInfo{{ID: "primary", Name: "primary"}}}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat("RESUME"), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, _ := c.waitResponse(c.send("session/load", `{"sessionId":"tidy-old-harp","cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	var got struct {
		Modes *struct {
			CurrentModeId  string `json:"currentModeId"`
			AvailableModes []struct {
				Id string `json:"id"`
			} `json:"availableModes"`
		} `json:"modes"`
		Models *struct {
			CurrentModelId string `json:"currentModelId"`
		} `json:"models"`
		ConfigOptions []struct {
			Id       string `json:"id"`
			Category string `json:"category"`
		} `json:"configOptions"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &got))

	require.NotNil(t, got.Modes, "a resumed session must report its mode state, or the editor loses the profile switcher")
	assert.Equal(t, operations.DefaultModeID, got.Modes.CurrentModeId)
	assert.Len(t, got.Modes.AvailableModes, 2)

	require.NotNil(t, got.Models, "a resumed session must report its model state")
	assert.Equal(t, "primary", got.Models.CurrentModelId)

	var categories []string
	for _, o := range got.ConfigOptions {
		categories = append(categories, o.Category)
	}
	assert.Contains(t, categories, "model", "CO1's spec-general model selector must ride the load response too")
}
