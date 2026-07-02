package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// chatHarness wires the ACP client's StructuredChat driver to a fakeAgent over
// two in-memory pipes and runs Chat in a goroutine. No subprocess, no auth.
type chatHarness struct {
	in      chan agent.ChatMessage
	out     chan agent.ChatEvent
	chatErr chan error
	fa      *fakeAgent
	cancel  context.CancelFunc
}

func startChat(t *testing.T, req agent.ChatRequest) *chatHarness {
	t.Helper()
	c2aR, c2aW := io.Pipe() // client → agent
	a2cR, a2cW := io.Pipe() // agent → client

	b := NewACP(nil)
	b.now = func() time.Time { return time.Unix(1700000000, 0) } // deterministic stamps
	b.openTransport = func(_ context.Context, _ []string, _ map[string]string, _ string) (*transport, error) {
		return &transport{
			stdin:  c2aW,
			stdout: a2cR,
			close: func() error {
				_ = c2aW.Close()
				_ = a2cR.Close()
				return nil
			},
		}, nil
	}

	h := &chatHarness{
		in:      make(chan agent.ChatMessage),
		out:     make(chan agent.ChatEvent, 64),
		chatErr: make(chan error, 1),
		fa:      newFakeAgent(c2aR, a2cW),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.chatErr <- b.Chat(ctx, req, h.in, h.out) }()
	t.Cleanup(cancel)
	return h
}

// collect drains out into a slice until it closes, returning a getter that waits
// for the close and yields the collected events.
func collect(out <-chan agent.ChatEvent) func() []agent.ChatEvent {
	var evs []agent.ChatEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			evs = append(evs, ev)
		}
	}()
	return func() []agent.ChatEvent {
		<-done
		return evs
	}
}

// TestChat_FullTurn drives the whole lifecycle (initialize → session/new →
// session/prompt) and asserts a scripted session/update sequence maps to the
// expected ctxloom entries — thinking, assistant text, tool_use, tool_result —
// bracketed by the Session and Complete events.
func TestChat_FullTurn(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "test-model"})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		assert.Equal(t, "session/prompt", promptReq.Method)

		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"reasoning"}}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hi there"}}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read","kind":"read","status":"pending","rawInput":{"path":"/x"}}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"the contents"}}]}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()

	require.Len(t, evs, 6)

	require.NotNil(t, evs[0].Session)
	assert.Equal(t, "test-model", evs[0].Session.Model)

	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, agent.EntryTypeThinking, evs[1].Entry.Type)
	assert.Equal(t, "reasoning", evs[1].Entry.Content)
	assert.False(t, evs[1].Entry.Timestamp.IsZero(), "entry should be stamped with receipt time")

	require.NotNil(t, evs[2].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[2].Entry.Type)
	assert.Equal(t, "Hi there", evs[2].Entry.Content)

	require.NotNil(t, evs[3].Entry)
	assert.Equal(t, agent.EntryTypeToolUse, evs[3].Entry.Type)
	assert.Equal(t, "Read", evs[3].Entry.ToolName)
	assert.JSONEq(t, `{"path":"/x"}`, string(evs[3].Entry.ToolInput))

	require.NotNil(t, evs[4].Entry)
	assert.Equal(t, agent.EntryTypeToolResult, evs[4].Entry.Type)
	assert.Equal(t, "the contents", evs[4].Entry.ToolOutput)

	require.NotNil(t, evs[5].Complete)
	assert.Equal(t, "end_turn", evs[5].Complete.StopReason)
	assert.Equal(t, "test-model", evs[5].Complete.Model)
}

// TestChat_InitializeHandshake asserts the client sends a spec-shaped initialize
// (protocolVersion, clientCapabilities.fs, clientInfo) before session/new.
func TestChat_InitializeHandshake(t *testing.T) {
	h := startChat(t, agent.ChatRequest{WorkDir: "/work"})
	events := collect(h.out)

	handshake := make(chan struct {
		init rpcMessage
		news rpcMessage
	}, 1)
	go func() {
		initReq := <-h.fa.requests
		_ = h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1})
		newReq := <-h.fa.requests
		_ = h.fa.respond(newReq.ID, map[string]any{"sessionId": "s"})
		handshake <- struct {
			init rpcMessage
			news rpcMessage
		}{initReq, newReq}
		promptReq := <-h.fa.requests
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	events()

	hs := <-handshake

	assert.Equal(t, "initialize", hs.init.Method)
	var init initializeParams
	require.NoError(t, json.Unmarshal(hs.init.Params, &init))
	assert.Equal(t, 1, init.ProtocolVersion)
	assert.True(t, init.ClientCapabilities.Fs.ReadTextFile)
	assert.True(t, init.ClientCapabilities.Fs.WriteTextFile)
	require.NotNil(t, init.ClientInfo)
	assert.Equal(t, clientName, init.ClientInfo.Name)

	assert.Equal(t, "session/new", hs.news.Method)
	var newReq struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal(hs.news.Params, &newReq))
	assert.Equal(t, "/work", newReq.Cwd)
}

// TestChat_Permission pins the agent→client permission callback: auto-approve
// selects an allow option; otherwise the client rejects.
func TestChat_Permission(t *testing.T) {
	options := []map[string]any{
		{"kind": "allow_once", "name": "Allow once", "optionId": "ao"},
		{"kind": "reject_once", "name": "Reject once", "optionId": "ro"},
	}

	cases := []struct {
		name        string
		autoApprove bool
		wantOption  string
	}{
		{"auto-approve selects allow", true, "ao"},
		{"deny selects reject", false, "ro"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startChat(t, agent.ChatRequest{AutoApprove: tc.autoApprove})
			events := collect(h.out)

			gotResp := make(chan rpcMessage, 1)
			go func() {
				sid := h.fa.serveHandshake(t)
				promptReq := <-h.fa.requests
				gotResp <- h.fa.requestPermission(sid, options)
				_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
			}()

			h.in <- agent.ChatMessage{Text: "do it"}
			close(h.in)
			require.NoError(t, <-h.chatErr)
			events()

			resp := <-gotResp
			require.Nil(t, resp.Error)
			var body permissionResult
			require.NoError(t, json.Unmarshal(resp.Result, &body))
			assert.Equal(t, outcomeSelected, body.Outcome.Outcome)
			assert.Equal(t, tc.wantOption, body.Outcome.OptionId)
		})
	}
}

// TestChat_CancelDuringTurn asserts that cancelling ctx mid-turn returns
// context.Canceled and sends the agent a session/cancel notification.
func TestChat_CancelDuringTurn(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	turnStarted := make(chan struct{})
	go func() {
		sid := h.fa.serveHandshake(t)
		<-h.fa.requests // session/prompt — never answered (a long-running turn)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working"}}`)
		close(turnStarted)
	}()

	h.in <- agent.ChatMessage{Text: "hang"}
	<-turnStarted
	h.cancel()

	assert.ErrorIs(t, <-h.chatErr, context.Canceled)
	events() // out drains and closes on return

	select {
	case n := <-h.fa.notifications:
		assert.Equal(t, "session/cancel", n.Method)
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not receive session/cancel")
	}
}

// permTestOptions is the option set the fake agent offers in permission tests.
var permTestOptions = []map[string]any{
	{"kind": "allow_once", "name": "Allow once", "optionId": "ao"},
	{"kind": "reject_once", "name": "Reject once", "optionId": "ro"},
}

// readUntilPermission consumes out until the forwarded permission request
// arrives (failing the test if the stream ends first).
func readUntilPermission(t *testing.T, out <-chan agent.ChatEvent) *agent.PermissionRequest {
	t.Helper()
	for ev := range out {
		if ev.Permission != nil {
			return ev.Permission
		}
	}
	t.Fatal("out closed before the forwarded permission request arrived")
	return nil
}

// TestChat_ForwardedPermission: under ForwardPermissions the driver surfaces
// the agent's request as a ChatEvent (options verbatim) and answers with the
// caller's selected option — the pass-through that gives an interactive host
// real approvals.
func TestChat_ForwardedPermission(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardPermissions: true})

	gotResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		gotResp <- h.fa.requestPermission(sid, permTestOptions)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "do it"}

	perm := readUntilPermission(t, h.out)
	assert.Equal(t, "run", perm.ToolName, "tool name comes from the request's toolCall title")
	require.Len(t, perm.Options, 2)
	assert.Equal(t, "ao", perm.Options[0].ID)
	assert.Equal(t, "allow_once", perm.Options[0].Kind)

	h.in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: perm.ID, OptionID: "ao"}}

	resp := <-gotResp
	require.Nil(t, resp.Error)
	var body permissionResult
	require.NoError(t, json.Unmarshal(resp.Result, &body))
	assert.Equal(t, outcomeSelected, body.Outcome.Outcome)
	assert.Equal(t, "ao", body.Outcome.OptionId)

	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range h.out {
	} // drain to close
}

// TestChat_ForwardedPermission_Dismissed: an empty-option answer resolves as a
// cancelled outcome (neither an approval nor a remembered rejection).
func TestChat_ForwardedPermission_Dismissed(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardPermissions: true})

	gotResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		gotResp <- h.fa.requestPermission(sid, permTestOptions)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "do it"}
	perm := readUntilPermission(t, h.out)
	h.in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: perm.ID}}

	resp := <-gotResp
	require.Nil(t, resp.Error)
	var body permissionResult
	require.NoError(t, json.Unmarshal(resp.Result, &body))
	assert.Equal(t, outcomeCancelled, body.Outcome.Outcome)

	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range h.out {
	}
}

// TestChat_ForwardedPermission_InputClosed: closing input while a forwarded
// request is parked resolves it as cancelled — no answer can ever arrive, so
// the turn must not stay parked forever.
func TestChat_ForwardedPermission_InputClosed(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardPermissions: true})

	gotResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		gotResp <- h.fa.requestPermission(sid, permTestOptions)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "do it"}
	readUntilPermission(t, h.out)
	close(h.in)

	resp := <-gotResp
	require.Nil(t, resp.Error)
	var body permissionResult
	require.NoError(t, json.Unmarshal(resp.Result, &body))
	assert.Equal(t, outcomeCancelled, body.Outcome.Outcome)

	require.NoError(t, <-h.chatErr)
	for range h.out {
	}
}

// TestChat_CancelTurnKeepsConversation: a CancelTurn message cancels only the
// in-flight turn — the agent gets session/cancel, the turn completes with
// stopReason "cancelled", and the SAME conversation runs a further turn.
func TestChat_CancelTurnKeepsConversation(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)

		// Turn 1: hold the prompt open until the cancel notification arrives,
		// then resolve it as cancelled (the spec's required stop reason).
		promptReq := <-h.fa.requests
		n := <-h.fa.notifications
		assert.Equal(t, "session/cancel", n.Method)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "cancelled"})

		// Turn 2 proves the conversation survived.
		promptReq = <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"still here"}}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "long job"}
	h.in <- agent.ChatMessage{CancelTurn: true}
	h.in <- agent.ChatMessage{Text: "follow-up"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()

	var stops []string
	var texts []string
	for _, ev := range evs {
		if ev.Complete != nil {
			stops = append(stops, ev.Complete.StopReason)
		}
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
			texts = append(texts, ev.Entry.Content)
		}
	}
	assert.Equal(t, []string{"cancelled", "end_turn"}, stops)
	assert.Equal(t, []string{"still here"}, texts)
}

// TestChat_QueuedMessageMidTurn: a text message arriving while a turn is in
// flight queues and runs as the next turn (the input loop no longer blocks
// inside the prompt).
func TestChat_QueuedMessageMidTurn(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	prompts := make(chan string, 2)
	go func() {
		_ = h.fa.serveHandshake(t)
		for i := 0; i < 2; i++ {
			promptReq := <-h.fa.requests
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(promptReq.Params, &p)
			if len(p.Prompt) > 0 {
				prompts <- p.Prompt[0].Text
			}
			_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
		}
	}()

	h.in <- agent.ChatMessage{Text: "first"}
	h.in <- agent.ChatMessage{Text: "second"} // lands mid-turn, must queue
	close(h.in)

	require.NoError(t, <-h.chatErr)
	events()
	assert.Equal(t, "first", <-prompts)
	assert.Equal(t, "second", <-prompts)
}

// TestChat_MCPServersAtSessionNew: caller-supplied MCP servers ride the
// session/new request in the spec's wire shape (env as name/value pairs).
func TestChat_MCPServersAtSessionNew(t *testing.T) {
	h := startChat(t, agent.ChatRequest{
		WorkDir: "/work",
		MCPServers: []agent.ChatMCPServer{
			{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"B": "2", "A": "1"}},
		},
	})
	events := collect(h.out)

	newParams := make(chan json.RawMessage, 1)
	go func() {
		initReq := <-h.fa.requests
		_ = h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1})
		newReq := <-h.fa.requests
		newParams <- newReq.Params
		_ = h.fa.respond(newReq.ID, map[string]any{"sessionId": "s"})
		promptReq := <-h.fa.requests
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	events()

	var got struct {
		McpServers []struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(<-newParams, &got))
	require.Len(t, got.McpServers, 1)
	assert.Equal(t, "tools", got.McpServers[0].Name)
	assert.Equal(t, "/bin/tools", got.McpServers[0].Command)
	assert.Equal(t, []string{"serve"}, got.McpServers[0].Args)
	require.Len(t, got.McpServers[0].Env, 2)
	assert.Equal(t, "A", got.McpServers[0].Env[0].Name, "env is sorted for a deterministic frame")
}

// TestChat_TransportError surfaces a spawn failure as a Chat error (and still
// closes out per the contract).
func TestChat_TransportError(t *testing.T) {
	b := NewACP(nil)
	b.openTransport = func(context.Context, []string, map[string]string, string) (*transport, error) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make(chan agent.ChatEvent, 1)
	in := make(chan agent.ChatMessage)
	err := b.Chat(context.Background(), agent.ChatRequest{}, in, out)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	_, ok := <-out
	assert.False(t, ok, "out must be closed even on transport failure")
}
