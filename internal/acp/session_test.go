package acp

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/coder/acp-go-sdk"
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
	return startChatWithClock(t, req, func() time.Time { return time.Unix(1700000000, 0) })
}

// startChatWithClock is startChat with an injected clock, for tests that
// assert self-measured timing (the fixed default keeps stamps deterministic).
func startChatWithClock(t *testing.T, req agent.ChatRequest, now func() time.Time) *chatHarness {
	t.Helper()
	c2aR, c2aW := io.Pipe() // client → agent
	a2cR, a2cW := io.Pipe() // agent → client

	b := NewACP()
	b.now = now
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
	assert.Equal(t, "sess-1", evs[0].Session.SessionID, "the native session id rides the Session event (the coordinator's resume handle)")

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

// TestChat_TurnMetaAccounting: the real usage_update variant and ctxloom's
// own (renamed, non-colliding — see L0 checklist B3) ctxloom_session_info
// extension — the shapes ctxloom's own acp agent emits; protocol v1 itself
// delivers no usage anywhere else — fold into the turn's Complete meta
// instead of being dropped as malformed, and the turn duration is
// self-measured off the clock.
func TestChat_TurnMetaAccounting(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var tick atomic.Int64
	h := startChatWithClock(t, agent.ChatRequest{Model: "requested-model"}, func() time.Time {
		return base.Add(time.Duration(tick.Add(1)) * 100 * time.Millisecond)
	})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"ctxloom_session_info","model":"real-model","permissionMode":"default","contextWindow":150000}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"usage_update","used":53000,"size":200000,"cost":{"amount":0.045,"currency":"USD"}}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()

	// Session info + Complete only: the accounting variants yield no entries.
	require.Len(t, evs, 2)
	require.NotNil(t, evs[0].Session)
	meta := evs[1].Complete
	require.NotNil(t, meta)
	assert.Equal(t, "end_turn", meta.StopReason)
	assert.Equal(t, "real-model", meta.Model, "the agent-reported model outranks the requested one")
	assert.Equal(t, 53000, meta.InputTokens)
	assert.Equal(t, 200000, meta.ContextWindow, "usage_update's window size outranks session_info")
	assert.InDelta(t, 0.045, meta.CostUSD, 1e-9)
	assert.Positive(t, meta.DurationMs, "duration is self-measured (ACP carries no timing)")
}

// TestChat_TurnMetaDefaults: with no accounting variants on the wire (every
// spec-only adapter today), the Complete meta carries what protocol v1
// actually delivers — stop reason, the requested model echoed back, and the
// self-measured duration; the token/cost fields stay zero ("unknown").
func TestChat_TurnMetaDefaults(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "test-model"})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()
	require.Len(t, evs, 3)
	meta := evs[2].Complete
	require.NotNil(t, meta)
	assert.Equal(t, "test-model", meta.Model)
	assert.Zero(t, meta.InputTokens)
	assert.Zero(t, meta.ContextWindow)
	assert.Zero(t, meta.CostUSD)
	assert.Zero(t, meta.DurationMs, "a fixed clock measures a zero-length turn")
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
	var init api.InitializeRequest
	require.NoError(t, json.Unmarshal(hs.init.Params, &init))
	assert.EqualValues(t, 1, init.ProtocolVersion)
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

// TestChat_Permission pins the agent→client permission callback: a bypass
// posture selects an allow option; every non-bypass posture rejects. The ACP
// driver distinguishes only bypass (allow-without-prompt) — plan and acceptEdits
// have no read-only/edit-scoped ACP mapping here, so they collapse to the same
// reject as default. This test pins that collapse so it stays intentional.
func TestChat_Permission(t *testing.T) {
	options := []map[string]any{
		{"kind": "allow_once", "name": "Allow once", "optionId": "ao"},
		{"kind": "reject_once", "name": "Reject once", "optionId": "ro"},
	}

	cases := []struct {
		name       string
		perm       agent.PermissionMode
		wantOption string
	}{
		{"bypass selects allow", agent.PermissionBypass, "ao"},
		{"default selects reject", agent.PermissionDefault, "ro"},
		{"acceptEdits collapses to reject", agent.PermissionAcceptEdits, "ro"},
		{"plan collapses to reject", agent.PermissionPlan, "ro"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startChat(t, agent.ChatRequest{Permissions: tc.perm})
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

// TestChat_MCPServersHttpSse_EngineSupports proves an editor-supplied http/sse
// MCP server (B3, gap G11) actually REACHES the engine: when the connected
// engine's own initialize response advertises mcpCapabilities.http/sse,
// mcpServersToACP forwards both in the spec's discriminated-union shape
// (verified from the RAW wire bytes, not just the decoded Go struct) and no
// drop is reported.
func TestChat_MCPServersHttpSse_EngineSupports(t *testing.T) {
	h := startChat(t, agent.ChatRequest{
		WorkDir: "/work",
		MCPServers: []agent.ChatMCPServer{
			{Name: "remote-http", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
			{Name: "remote-sse", Transport: agent.MCPTransportSSE, URL: "https://example.com/sse"},
		},
	})
	events := collect(h.out)

	newParams := make(chan json.RawMessage, 1)
	go func() {
		initReq := <-h.fa.requests
		_ = h.fa.respond(initReq.ID, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"mcpCapabilities": map[string]any{"http": true, "sse": true}},
		})
		newReq := <-h.fa.requests
		newParams <- newReq.Params
		_ = h.fa.respond(newReq.ID, map[string]any{"sessionId": "s"})
		promptReq := <-h.fa.requests
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	evs := events()

	raw := string(<-newParams)
	assert.Contains(t, raw, `"type":"http"`)
	assert.Contains(t, raw, `"url":"https://example.com/mcp"`)
	assert.Contains(t, raw, `"Authorization"`)
	assert.Contains(t, raw, `"type":"sse"`)
	assert.Contains(t, raw, `"url":"https://example.com/sse"`)

	for _, ev := range evs {
		if ev.Session != nil {
			assert.Empty(t, ev.Session.MCPServers, "both entries were accepted by the engine's advertised capabilities — nothing should be reported as dropped")
		}
	}
}

// TestChat_MCPServersHttp_EngineDoesNotSupport proves the OTHER half of the
// contract: when the connected engine does NOT advertise mcpCapabilities.http,
// ctxloom must NOT send the http entry (would be a protocol violation) — but
// must NEVER silently drop it either. It is folded into the session's
// ChatSessionInfo.MCPServers as a named, reasoned status (this codebase's
// established loud-status mechanism — see internal/cli/run_structured.go's
// rendering of it), and the raw session/new bytes are proven to omit it.
func TestChat_MCPServersHttp_EngineDoesNotSupport(t *testing.T) {
	h := startChat(t, agent.ChatRequest{
		WorkDir: "/work",
		MCPServers: []agent.ChatMCPServer{
			{Name: "stdio-tool", Command: "/bin/tools"},
			{Name: "remote-http", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp"},
		},
	})
	events := collect(h.out)

	newParams := make(chan json.RawMessage, 1)
	go func() {
		initReq := <-h.fa.requests
		// No mcpCapabilities in the response at all: http/sse default false.
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
	evs := events()

	raw := string(<-newParams)
	assert.Contains(t, raw, `"stdio-tool"`, "the stdio entry is the protocol's unconditional baseline — always sent")
	assert.NotContains(t, raw, "remote-http", "an http entry MUST NOT be sent to an engine that never advertised mcpCapabilities.http")

	var sawStatus bool
	for _, ev := range evs {
		if ev.Session == nil {
			continue
		}
		for _, s := range ev.Session.MCPServers {
			if s.Name == "remote-http" {
				sawStatus = true
				assert.Contains(t, s.Status, "unsupported", "the dropped entry's status must say WHY, never a bare 'withheld'")
				assert.Contains(t, s.Status, "http")
			}
		}
	}
	assert.True(t, sawStatus, "a dropped http entry must be reported in ChatSessionInfo.MCPServers, never silently discarded")
}

// TestChat_ResumeSessionLoad: a ChatRequest with ResumeSessionID, against an
// agent that advertises the loadSession capability, resumes via session/load
// (never session/new) under the SAME session id — and the replay history
// session/load sends mid-call (per the ACP spec) flows to `out` as ordinary
// ChatEvents ahead of the new turn's events.
func TestChat_ResumeSessionLoad(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "test-model", ResumeSessionID: "sess-old"})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.Equal(t, "initialize", initReq.Method)
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"loadSession": true},
		}))

		loadReq := <-h.fa.requests
		require.Equal(t, "session/load", loadReq.Method)
		var params struct {
			SessionId string `json:"sessionId"`
		}
		require.NoError(t, json.Unmarshal(loadReq.Params, &params))
		assert.Equal(t, "sess-old", params.SessionId)

		// The replay: history session/load sends as session/update while
		// the call is still in flight.
		_ = h.fa.sessionUpdate("sess-old", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"prior turn"}}`)
		require.NoError(t, h.fa.respond(loadReq.ID, nil))

		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "continue"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()
	require.Len(t, evs, 3)
	// The replay arrives WHILE session/load is still in flight — ahead of
	// the Session event, which fires only once setup() returns.
	require.NotNil(t, evs[0].Entry, "replayed history arrives ahead of Session")
	assert.Equal(t, "prior turn", evs[0].Entry.Content)
	require.NotNil(t, evs[1].Session, "Session event fires once setup (session/load) returns")
	require.NotNil(t, evs[2].Complete)
}

// TestChat_ResumeSessionLoad_CapabilityMissing fails loud (never silently
// falls back to session/new) when the agent does not advertise loadSession:
// starting fresh under a resumed id's name would silently discard the
// caller's requested continuity.
func TestChat_ResumeSessionLoad_CapabilityMissing(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "test-model", ResumeSessionID: "sess-old"})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))
	}()

	close(h.in)
	err := <-h.chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loadSession")
	assert.Empty(t, events(), "no events besides the closed channel — setup never got far enough for a Session event")
}

// TestChat_ProtocolVersionMismatch fails loud (never silently continues a
// conversation shaped by a version this SDK does not decode) when the engine
// negotiates any protocol version other than the one this client speaks —
// the isolation-must-not-negotiate discipline applied to protocol versions.
func TestChat_ProtocolVersionMismatch(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.Equal(t, "initialize", initReq.Method)
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 2}))
	}()

	close(h.in)
	err := <-h.chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protocol version 2")
	assert.Contains(t, err.Error(), "only speaks version 1")
	assert.Empty(t, events(), "setup never got far enough for a Session event")
}

// TestChat_AuthRequired fails loud — never hangs — when the engine answers
// session/new with the spec's auth_required error: ctxloom drives no
// interactive authenticate flow yet, so the session-open error must name the
// method(s) the engine advertised at initialize rather than leaving the
// caller to decode a bare -32000.
func TestChat_AuthRequired(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{
			"protocolVersion": 1,
			"authMethods": []map[string]any{
				{"type": "env_var", "id": "api-key", "name": "API Key", "vars": []any{}},
			},
		}))

		newReq := <-h.fa.requests
		require.Equal(t, "session/new", newReq.Method)
		require.NoError(t, h.fa.respondError(newReq.ID, -32000, "Authentication required"))
	}()

	close(h.in)
	err := <-h.chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires authentication")
	assert.Contains(t, err.Error(), "api-key (API Key)")
	assert.Contains(t, err.Error(), "does not yet drive an interactive authenticate flow")
	assert.Empty(t, events(), "setup never got far enough for a Session event")
}

// TestChat_AuthRequired_NoMethodsAdvertised covers the degenerate case: an
// engine that answers auth_required without ever having advertised a method
// at initialize. The error must still be loud and must not crash rendering
// an empty method list.
func TestChat_AuthRequired_NoMethodsAdvertised(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))

		newReq := <-h.fa.requests
		require.Equal(t, "session/new", newReq.Method)
		require.NoError(t, h.fa.respondError(newReq.ID, -32000, "Authentication required"))
	}()

	close(h.in)
	err := <-h.chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires authentication")
	assert.Contains(t, err.Error(), "none advertised")
	assert.Empty(t, events())
}

// TestChat_SessionNewError_NotAuthRequired pins that a session/new failure
// for an UNRELATED reason (any code but -32000) passes through unchanged —
// authRequiredErr must not repaint every session-open failure as an auth
// problem.
func TestChat_SessionNewError_NotAuthRequired(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))

		newReq := <-h.fa.requests
		require.NoError(t, h.fa.respondError(newReq.ID, -32603, "boom"))
	}()

	close(h.in)
	err := <-h.chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.NotContains(t, err.Error(), "requires authentication")
	assert.Empty(t, events())
}

// TestChat_TransportError surfaces a spawn failure as a Chat error (and still
// closes out per the contract).
func TestChat_TransportError(t *testing.T) {
	b := NewACP()
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
