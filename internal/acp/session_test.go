package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/coder/acp-go-sdk"
	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	return startChatWithExit(t, req, now, nil)
}

// startChatWithExit is startChatWithClock with the transport's teardown result
// under the test's control — the seam that stands in for a spawned engine's
// process exit status, which is exactly what transport.close returns in
// production (spawnHostTransport's cmd.Wait error). nil is the healthy engine.
func startChatWithExit(t *testing.T, req agent.ChatRequest, now func() time.Time, exit error) *chatHarness {
	t.Helper()
	c2aR, c2aW := io.Pipe() // client → agent
	a2cR, a2cW := io.Pipe() // agent → client

	b := NewACP()
	b.now = now
	b.openTransport = func(context.Context, transportRequest) (*transport, error) {
		return &transport{
			stdin:  c2aW,
			stdout: a2cR,
			close: func() error {
				_ = c2aW.Close()
				_ = a2cR.Close()
				return exit
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

// TestChat_TurnMetaAccounting: the real usage_update variant — the shape
// ctxloom's own acp agent emits; protocol v1 itself delivers no usage
// anywhere else — folds into the turn's Complete meta instead of being
// dropped as malformed, and the turn duration is self-measured off the
// clock. A ctxloom-emitted session_info_update's
// `_meta.ctxloom_session_info` blob no longer carries model/contextWindow at
// all (those ride CO1's SessionConfigOption and usage_update's `size`
// instead — see consumeMetaUpdate's doc comment), so this test no longer
// sends one; TestChat_ForeignSessionInfoUpdateIgnored below proves a
// session_info_update frame (ctxloom's own residual one, or a genuinely
// foreign one) is harmlessly absorbed as a no-op either way.
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
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"usage_update","used":53000,"size":200000,"cost":{"amount":0.045,"currency":"USD"}}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()

	// Session info + Complete only: the accounting variant yields no entry.
	require.Len(t, evs, 2)
	require.NotNil(t, evs[0].Session)
	meta := evs[1].Complete
	require.NotNil(t, meta)
	assert.Equal(t, "end_turn", meta.StopReason)
	assert.Equal(t, "requested-model", meta.Model, "G13: no ctxloom session-info echo can override this anymore — the requested model stands")
	assert.Equal(t, 53000, meta.InputTokens)
	assert.Equal(t, 200000, meta.ContextWindow, "usage_update's window size")
	assert.InDelta(t, 0.045, meta.CostUSD, 1e-9)
	assert.Positive(t, meta.DurationMs, "duration is self-measured (ACP carries no timing)")
}

// ACP's usage_update is CUMULATIVE by specification —
// api.UsageUpdate types `cost` as "Cumulative session cost" and `used` as
// "Tokens currently in context" — and TurnMeta.InputTokens/CostUSD carry those
// values through UNCHANGED. That is deliberate, not an accident of naming, and
// every reader depends on it: run_structured renders "context used/window",
// acpagent's usageUpdateWire re-emits a usage_update whose `used`/`cost` must
// still be cumulative for the editor's own gauge, and agentcoord's
// usageFromMeta bills the LAST turn's meta as the session total.
//
// Turning these into per-turn deltas would silently corrupt all three. This
// pins the cumulative contract across two turns: turn two reports the latest
// cumulative figures, not a delta and not a sum.
func TestChat_TurnMetaCarriesCumulativeUsageAcrossTurns(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "m"})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)

		req1 := <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"usage_update","used":1000,"size":200000,"cost":{"amount":0.010,"currency":"USD"}}`)
		require.NoError(t, h.fa.respond(req1.ID, map[string]any{"stopReason": "end_turn"}))

		req2 := <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"usage_update","used":2500,"size":200000,"cost":{"amount":0.030,"currency":"USD"}}`)
		require.NoError(t, h.fa.respond(req2.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "one"}
	h.in <- agent.ChatMessage{Text: "two"}
	close(h.in)
	require.NoError(t, <-h.chatErr)

	var metas []*agent.TurnMeta
	for _, ev := range events() {
		if ev.Complete != nil {
			metas = append(metas, ev.Complete)
		}
	}
	require.Len(t, metas, 2)

	assert.Equal(t, 1000, metas[0].InputTokens)
	assert.InDelta(t, 0.010, metas[0].CostUSD, 1e-9)

	assert.Equal(t, 2500, metas[1].InputTokens,
		"the engine's cumulative 'tokens currently in context' rides through — not the 1500 delta, not a 3500 sum")
	assert.InDelta(t, 0.030, metas[1].CostUSD, 1e-9,
		"the engine's cumulative session cost rides through — not the 0.020 delta")
}

// TestChat_ForeignSessionInfoUpdateIgnored: a "session_info_update" frame —
// whether it's ctxloom's OWN residual PermissionMode/MCPServers `_meta`
// payload, or a genuinely foreign engine's real title/timestamp update — is
// harmlessly absorbed as a no-op by the client (consumeMetaUpdate no
// longer special-cases this discriminator at all; it falls through to the
// strict api.SessionUpdate decode and lands in mapSessionUpdate's default
// case). It must NOT crash or drop the turn, and must NOT fabricate a
// Model/ContextWindow value, proven here with a real payload rather than
// asserted by inspection.
func TestChat_ForeignSessionInfoUpdateIgnored(t *testing.T) {
	h := startChat(t, agent.ChatRequest{Model: "test-model"})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"session_info_update","title":"a real title update","updatedAt":"2026-07-16T00:00:00Z"}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"session_info_update","_meta":{"ctxloom_session_info":{"permissionMode":"default"}}}`)
		_ = h.fa.sessionUpdate(sid, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	evs := events()
	// Both the foreign title update AND ctxloom's own residual _meta payload
	// contribute NOTHING — only the handshake's own Session event, the
	// assistant chunk, and Complete carry through (matching
	// TestChat_FullTurn's baseline shape).
	require.Len(t, evs, 3)
	require.NotNil(t, evs[0].Session)
	assert.Equal(t, "test-model", evs[0].Session.Model, "the handshake's own Session event, untouched by either frame")
	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[1].Entry.Type)
	meta := evs[2].Complete
	require.NotNil(t, meta)
	assert.Equal(t, "test-model", meta.Model, "no fabricated override — the requested model stands")
	assert.Zero(t, meta.ContextWindow, "neither frame carries usage_update's size, so no gauge is reported")
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

// TestChat_ForwardedPermission_NoRaceOnConcurrentCancel pins that Chat's
// `defer close(out)` used to race the forwardPermission goroutine it spawns
// with a bare `go` and never joins. registerPermission's Add(1)/forwardPermission's
// Done() (via forwardGoroutines, session.go) close that window — teardown()
// now Wait()s on every in-flight forwarder before returning, so close(out)
// cannot run while one is still attempting its own s.send() call.
//
// The race needs the permission request dispatched (registerPermission called,
// forwardPermission goroutine spawned) as close as possible to the parent ctx
// being cancelled — waiting for the fake agent's requestPermission reply would
// serialize past exactly that window, so this writes the request frame
// directly and cancels immediately after, across enough iterations that an
// unsynchronized version reliably crashes the whole test binary with "panic:
// send on closed channel" (the review's own repro measured 96/200).
func TestChat_ForwardedPermission_NoRaceOnConcurrentCancel(t *testing.T) {
	const iterations = 200
	for i := 0; i < iterations; i++ {
		h := startChat(t, agent.ChatRequest{ForwardPermissions: true})

		sid := h.fa.serveHandshake(t)
		h.in <- agent.ChatMessage{Text: "do it"}
		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method, "iteration %d", i)

		reqID := atomic.AddInt64(&h.fa.nextID, 1)
		params, merr := json.Marshal(map[string]any{
			"sessionId": sid,
			"options":   permTestOptions,
			"toolCall":  map[string]any{"toolCallId": "tc1", "title": "run"},
		})
		require.NoError(t, merr)
		require.NoError(t, h.fa.writeFrame(rpcMessage{
			Method: "session/request_permission",
			ID:     json.RawMessage(strconv.FormatInt(reqID, 10)),
			Params: params,
		}))
		// No synchronization between the write above and the cancel below is
		// deliberate: it is exactly the absence of synchronization that lets
		// the race happen at all, in production as much as here.
		h.cancel()

		// Chat legitimately returns ctx.Err() on a cancelled ctx (that is not
		// the bug) — what this test guards is that returning it, and the
		// close(out) that follows, never races a forwardPermission goroutine's
		// own send into a panic that would crash the whole process instead of
		// just this one conversation.
		require.ErrorIs(t, <-h.chatErr, context.Canceled, "iteration %d", i)
		for range h.out {
		}
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

// Messages arriving while a turn is in flight queue with no bound.
// That is reachable from off-box: a gRPC client streams ChatInput_UserMessage
// frames (internal/lm/grpc/chat.go's chatMessageFromInput), each able to carry
// full ContentBlocks including base64 media, and every one lands in `queued`.
//
// The loop CANNOT push back by blocking on `in` — Chat's contract is that the
// input loop keeps draining while a prompt is in flight, which is the only
// reason a permission answer or a cancel can reach a parked engine at all. So
// the queue stays unbounded on purpose and the depth is reported instead: a
// signal, where there was none, without dropping a message or stalling the
// callbacks.
func TestChat_UnboundedQueueDepthIsReported(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	release := make(chan struct{})
	go func() {
		_ = h.fa.serveHandshake(t)
		first := <-h.fa.requests
		<-release // hold turn one open while the rest of the messages pile up
		require.NoError(t, h.fa.respond(first.ID, map[string]any{"stopReason": "end_turn"}))
		for {
			req, ok := <-h.fa.requests
			if !ok {
				return
			}
			_ = h.fa.respond(req.ID, map[string]any{"stopReason": "end_turn"})
		}
	}()

	// One starts the turn; the rest queue behind it.
	h.in <- agent.ChatMessage{Text: "start the turn"}
	for i := 0; i < queueDepthWarnAt+1; i++ {
		h.in <- agent.ChatMessage{Text: "queued " + strconv.Itoa(i)}
	}
	close(release)
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Contains(t, warnings.String(), "queued",
		"a queue growing without bound must say so — there is no other backpressure signal available")
}

// A conversation that never backs up stays silent.
func TestChat_ShallowQueueIsSilent(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshake(t)
		for {
			req, ok := <-h.fa.requests
			if !ok {
				return
			}
			_ = h.fa.respond(req.ID, map[string]any{"stopReason": "end_turn"})
		}
	}()

	h.in <- agent.ChatMessage{Text: "one"}
	h.in <- agent.ChatMessage{Text: "two"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Empty(t, warnings.String(), "an ordinary conversation must not warn about its queue")
}

// The spawned engine's exit status is captured — spawnHostTransport's
// close returns cmd.Wait's error — and then thrown away by teardown's
// `_ = conn.Close()`. An ACP agent that dies non-zero after a conversation was
// reported as an unqualified success, with the one piece of evidence about why
// discarded on the way out.
//
// The conversation itself still succeeds: its turns were delivered, and failing
// a good conversation on a shutdown status would be the wrong trade. What must
// not happen is silence.
func TestChat_EngineExitStatusIsNotDiscarded(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChatWithExit(t, agent.ChatRequest{}, func() time.Time { return time.Unix(1700000000, 0) },
		errors.New("exit status 3"))
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)

	assert.NoError(t, <-h.chatErr, "a delivered conversation is still a success")
	for range events() {
	}

	assert.Contains(t, warnings.String(), "exit status 3",
		"the engine's own exit status must reach the operator, not be dropped by teardown")
}

// A healthy engine — teardown reporting no error — stays silent.
func TestChat_CleanEngineExitIsSilent(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Empty(t, warnings.String(), "a clean shutdown must not warn")
}

// chatSession.caps is assigned AFTER conn.Start has already launched
// the read loop, so "never mutated afterward" cannot be what makes it safe to
// read — a reader that started before the write is racing it regardless of what
// happens later. What actually makes it safe is narrower: the write and every
// read happen on Chat's own loop goroutine, and no read-loop handler touches
// caps at all.
//
// This test holds that narrower invariant. The agent floods session/update
// notifications the instant session/new is answered, so the read loop is
// actively running handlers in the window where Chat assigns sess.caps; the
// turn that follows then exercises the capability gate. Under -race, any future
// read of caps from a notification or request handler is a reported data race
// here rather than a production heisenbug.
func TestChat_CapsAreNotReadFromTheReadLoop(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		initReq := <-h.fa.requests
		require.Equal(t, "initialize", initReq.Method)
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true}},
		}))

		newReq := <-h.fa.requests
		require.Equal(t, "session/new", newReq.Method)
		require.NoError(t, h.fa.respond(newReq.ID, map[string]any{"sessionId": "sess-1"}))

		// Overlap window: these run on the read loop while Chat is assigning
		// sess.caps from setup's return.
		for i := 0; i < 64; i++ {
			_ = h.fa.sessionUpdate("sess-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`)
		}

		promptReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	// A block whose delivery is decided by caps.Prompt.Image, so the read side
	// of the field is genuinely exercised.
	h.in <- agent.ChatMessage{
		Text:          "look",
		ContentBlocks: []agent.ContentBlock{{Kind: "image", Raw: json.RawMessage(`{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}`)}},
	}
	close(h.in)
	require.NoError(t, <-h.chatErr)

	var complete int
	for _, ev := range events() {
		if ev.Complete != nil {
			complete++
		}
	}
	assert.Equal(t, 1, complete, "the conversation must complete normally while the read loop was busy")
}

// A CancelTurn that arrives with no turn in flight used to be
// dropped on the floor — no session/cancel, no event, no diagnostic. That is
// a genuine race for any client driving this over the gRPC or ACP door: the
// turn completes in the instant between the user pressing cancel and the
// message arriving, and the user is left with a cancel that did nothing and no
// way to tell it did nothing. Cancelling nothing must still SAY so.
func TestChat_CancelTurnWithNoTurnInFlightIsReported(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshake(t)
		// One prompt only: the stray cancel must NOT produce a session/cancel
		// notification, since there is no turn for the agent to abandon.
		promptReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{CancelTurn: true}
	// The loop is single-threaded, so a message accepted after the cancel
	// proves the cancel was already processed.
	h.in <- agent.ChatMessage{Text: "still fine"}
	close(h.in)

	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Contains(t, warnings.String(), "no turn in flight",
		"a cancel with nothing to cancel must be reported, not silently dropped")

	select {
	case n := <-h.fa.notifications:
		t.Fatalf("a stray cancel must not reach the agent as %s — there is no turn to abandon", n.Method)
	default:
	}
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
// MCP server actually REACHES the engine: when the connected
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

// TestSetup_ProtocolVersionFloorIsWhatMakesStrictEqualitySafe pins the fact
// that setup's strict `!= api.ProtocolVersionNumber` check rests on.
//
// The ACP handshake lets an agent answer initialize with "the protocol version
// the client specified if supported by the agent, or the latest protocol
// version supported by the agent" — so an agent MAY name a version other than
// the one asked for, and the spec's instruction to the client is to disconnect
// only "if it doesn't support this version". Refusing every non-equal answer
// is therefore stricter than the spec requires, and it is harmless ONLY while
// this client speaks the protocol's lowest version: there is then no lower
// version for it to have decoded and accepted instead, and every non-equal
// answer really is a version it cannot decode.
//
// The day the pinned SDK raises this constant, that stops being true — an
// agent still on version 1 would be refused despite ctxloom being able to
// decode it. This assertion fails on that bump, on purpose, so the question is
// re-opened by whoever makes it rather than silently answered by a constant.
func TestSetup_ProtocolVersionFloorIsWhatMakesStrictEqualitySafe(t *testing.T) {
	assert.Equal(t, 1, api.ProtocolVersionNumber,
		"strict protocol-version equality is only spec-safe while this client speaks the LOWEST ACP version; a bump re-opens whether setup must accept a lower version it can decode")
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

// TestChat_MultimodalDelivery_CapableEngine: when the connected engine
// advertises image/audio/embeddedContext support, an outbound ChatMessage's
// ContentBlocks deliver as REAL ACP content blocks (not a flattened
// placeholder) — the payload proof that an image/audio block reaches the
// engine.
func TestChat_MultimodalDelivery_CapableEngine(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})

	promptParams := make(chan json.RawMessage, 1)
	go func() {
		initReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(initReq.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"promptCapabilities": map[string]any{"image": true, "audio": true, "embeddedContext": true},
			},
		}))
		newReq := <-h.fa.requests
		require.NoError(t, h.fa.respond(newReq.ID, map[string]any{"sessionId": "sess-mm"}))
		promptReq := <-h.fa.requests
		promptParams <- promptReq.Params
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{
		Text: "look",
		ContentBlocks: []agent.ContentBlock{
			{Kind: "text", Text: "look", Raw: json.RawMessage(`{"type":"text","text":"look"}`)},
			{Kind: "image", Raw: json.RawMessage(`{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}`)},
			{Kind: "audio", Raw: json.RawMessage(`{"type":"audio","data":"d29ybGQ=","mimeType":"audio/wav"}`)},
			{Kind: "resource", Raw: json.RawMessage(`{"type":"resource","resource":{"uri":"file:///a.go","text":"package main","mimeType":"text/x-go"}}`)},
		},
	}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range collect(h.out)() {
	}

	var got struct {
		Prompt []api.ContentBlock `json:"prompt"`
	}
	require.NoError(t, json.Unmarshal(<-promptParams, &got))
	require.Len(t, got.Prompt, 4)
	require.NotNil(t, got.Prompt[0].Text)
	assert.Equal(t, "look", got.Prompt[0].Text.Text)
	require.NotNil(t, got.Prompt[1].Image, "the image block rides as a REAL ImageBlock, not a text placeholder")
	assert.Equal(t, "aGVsbG8=", got.Prompt[1].Image.Data)
	assert.Equal(t, "image/png", got.Prompt[1].Image.MimeType)
	require.NotNil(t, got.Prompt[2].Audio, "the audio block rides as a REAL AudioBlock")
	assert.Equal(t, "d29ybGQ=", got.Prompt[2].Audio.Data)
	require.NotNil(t, got.Prompt[3].Resource, "the resource block rides as a REAL embedded resource")
}

// TestChat_MultimodalDelivery_IncapableEngine_FlattensWithWarning: when the
// connected engine did NOT advertise image/audio support, an image/audio
// content block is NEVER silently dropped — it degrades to a visible text
// placeholder naming exactly what happened and why, so the model (and a
// transcript viewer) sees the warning instead of the turn silently missing
// content. Proves the flatten-WITH-warning path end to end, including the
// wire payload the engine actually receives.
func TestChat_MultimodalDelivery_IncapableEngine_FlattensWithWarning(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})

	promptParams := make(chan json.RawMessage, 1)
	go func() {
		// serveHandshake responds with bare {"protocolVersion":1} — zero-value
		// PromptCapabilities (image/audio/embeddedContext all false): an
		// engine that advertised NOTHING.
		_ = h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		promptParams <- promptReq.Params
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{
		Text: "look",
		ContentBlocks: []agent.ContentBlock{
			{Kind: "text", Text: "look", Raw: json.RawMessage(`{"type":"text","text":"look"}`)},
			{Kind: "image", Raw: json.RawMessage(`{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}`)},
		},
	}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range collect(h.out)() {
	}

	var got struct {
		Prompt []api.ContentBlock `json:"prompt"`
	}
	require.NoError(t, json.Unmarshal(<-promptParams, &got))
	require.Len(t, got.Prompt, 2)
	require.NotNil(t, got.Prompt[0].Text)
	assert.Equal(t, "look", got.Prompt[0].Text.Text)

	require.NotNil(t, got.Prompt[1].Text, "the image degrades to a TEXT block — never a silent drop, and never sent as an image the engine didn't advertise support for")
	warning := got.Prompt[1].Text.Text
	assert.Contains(t, warning, "image content received but not delivered", "the warning names what happened")
	assert.Contains(t, warning, "does not advertise image support", "the warning names why")
	assert.Contains(t, warning, "image/png", "the warning names the mime type")
	assert.NotContains(t, warning, "aGVsbG8=", "the raw base64 bytes are never included in the warning text")
}

// TestChat_MultimodalDelivery_NoContentBlocks_Unchanged pins the hard
// constraint: a caller that never populates ContentBlocks (every current
// caller besides acpagent's session/prompt intake — run --structured,
// oneshot Execute, the interactive pty driver) gets the EXACT SAME single
// flattened TextBlock delivery as before this slice, regardless of the
// connected engine's capabilities.
func TestChat_MultimodalDelivery_NoContentBlocks_Unchanged(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})
	events := collect(h.out)

	promptParams := make(chan json.RawMessage, 1)
	go func() {
		_ = h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		promptParams <- promptReq.Params
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hello"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	events()

	var got struct {
		Prompt []api.ContentBlock `json:"prompt"`
	}
	require.NoError(t, json.Unmarshal(<-promptParams, &got))
	require.Len(t, got.Prompt, 1, "exactly one block — the flattened text, exactly as before B2")
	require.NotNil(t, got.Prompt[0].Text)
	assert.Equal(t, "hello", got.Prompt[0].Text.Text)
}

// TestChat_TerminalDeclined_Honestly: ctxloom's client role
// never advertises the terminal capability (no cross-process broker channel
// to the real editor exists yet — see setup's doc comment), and an engine
// that calls terminal/create anyway (ignoring the advertised false, or
// probing) gets a SPECIFIC, actionable decline naming exactly why — never a
// locally-implemented fake terminal (ctxloom brokers, it never implements
// one of its own), and never the generic method-not-found a truly
// unrecognized method gets.
// TestChat_TerminalDeclined_Honestly: without ForwardTerminal (no upstream
// editor advertised the capability — the ordinary case for e.g. a delegated
// child agent with no ACP editor at all), ctxloom's client role advertises
// Terminal: false and declines a probing engine's terminal/* call with a
// specific, actionable reason — never a locally-implemented fake terminal.
func TestChat_TerminalDeclined_Honestly(t *testing.T) {
	h := startChat(t, agent.ChatRequest{})

	initReq := <-h.fa.requests
	require.Equal(t, "initialize", initReq.Method)
	var init api.InitializeRequest
	require.NoError(t, json.Unmarshal(initReq.Params, &init))
	assert.False(t, init.ClientCapabilities.Terminal, "ForwardTerminal is false (no upstream editor advertised terminal) — advertising true would be an advertise-then-drop lie")
	require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))

	newReq := <-h.fa.requests
	require.NoError(t, h.fa.respond(newReq.ID, map[string]any{"sessionId": "sess-term"}))

	resp := l0CallClient(h.fa, "terminal/create", map[string]any{"sessionId": "sess-term", "command": "bash"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, jsonrpc.CodeMethodNotFound, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "does not advertise the terminal capability")
	assert.Contains(t, resp.Error.Message, "brokers terminal/* to editors, it never implements one itself")

	close(h.in)
	err := <-h.chatErr
	require.NoError(t, err)
	for range h.out {
	}
}

// readUntilTerminal consumes out until a forwarded ChatEvent.Terminal arrives.
func readUntilTerminal(t *testing.T, out <-chan agent.ChatEvent) *agent.TerminalRequest {
	t.Helper()
	for ev := range out {
		if ev.Terminal != nil {
			return ev.Terminal
		}
	}
	t.Fatal("chat closed before a terminal request arrived")
	return nil
}

// TestChat_ForwardedTerminal_Create: under ForwardTerminal, ctxloom advertises
// Terminal: true, and an engine's terminal/create call surfaces as a
// ChatEvent.Terminal with the session id STRIPPED (the engine's opaque
// session id must never reach whatever answers on the caller's behalf), and
// the caller's TerminalResponse.Result rides back as the JSON-RPC result
// VERBATIM — proving both the honest capability advertisement and the full
// round trip.
func TestChat_ForwardedTerminal_Create(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardTerminal: true})

	initReq := <-h.fa.requests
	var init api.InitializeRequest
	require.NoError(t, json.Unmarshal(initReq.Params, &init))
	assert.True(t, init.ClientCapabilities.Terminal, "ForwardTerminal true must advertise Terminal: true — brokering actually works")
	require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))

	newReq := <-h.fa.requests
	require.NoError(t, h.fa.respond(newReq.ID, map[string]any{"sessionId": "sess-term"}))

	gotResp := make(chan rpcMessage, 1)
	go func() {
		gotResp <- l0CallClient(h.fa, "terminal/create", map[string]any{
			"sessionId": "sess-term", "command": "bash", "args": []string{"-lc", "echo hi"},
		})
	}()

	term := readUntilTerminal(t, h.out)
	assert.Equal(t, agent.TerminalOpCreate, term.Op)
	var params map[string]any
	require.NoError(t, json.Unmarshal(term.Params, &params))
	_, hasSessionID := params["sessionId"]
	assert.False(t, hasSessionID, "the engine's own opaque session id must be stripped before crossing the boundary")
	assert.Equal(t, "bash", params["command"])

	h.in <- agent.ChatMessage{Terminal: &agent.TerminalResponse{ID: term.ID, Result: json.RawMessage(`{"terminalId":"editor-term-1"}`)}}

	resp := <-gotResp
	require.Nil(t, resp.Error)
	assert.JSONEq(t, `{"terminalId":"editor-term-1"}`, string(resp.Result), "the caller's answer must reach the engine's JSON-RPC result verbatim")

	close(h.in)
	err := <-h.chatErr
	require.NoError(t, err)
	for range h.out {
	}
}

// TestChat_ForwardedTerminal_CallerError: when the caller answers with an
// Error (the upstream editor declined/errored), the engine's terminal/*
// call fails with that SPECIFIC reason — never a silent drop or a
// fabricated success.
func TestChat_ForwardedTerminal_CallerError(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardTerminal: true})
	initReq := <-h.fa.requests
	require.NoError(t, h.fa.respond(initReq.ID, map[string]any{"protocolVersion": 1}))
	newReq := <-h.fa.requests
	require.NoError(t, h.fa.respond(newReq.ID, map[string]any{"sessionId": "sess-term"}))

	gotResp := make(chan rpcMessage, 1)
	go func() {
		gotResp <- l0CallClient(h.fa, "terminal/kill", map[string]any{"sessionId": "sess-term", "terminalId": "t1"})
	}()

	term := readUntilTerminal(t, h.out)
	assert.Equal(t, agent.TerminalOpKill, term.Op)
	h.in <- agent.ChatMessage{Terminal: &agent.TerminalResponse{ID: term.ID, Error: "editor: no such terminal"}}

	resp := <-gotResp
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "editor: no such terminal")

	close(h.in)
	err := <-h.chatErr
	require.NoError(t, err)
	for range h.out {
	}
}

// TestChat_ForwardedTerminal_InputClosed: closing input while a terminal
// request is pending resolves it with a loud, specific error — never a hang.
// A turn is kept in flight (mirroring TestChat_ForwardedPermission_
// InputClosed) so the driver's own teardown — which fires once input is
// closed AND no turn is in flight — cannot race the reply this test asserts
// on: the fake engine only completes the turn AFTER the terminal answer.
func TestChat_ForwardedTerminal_InputClosed(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardTerminal: true})

	gotResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		gotResp <- l0CallClient(h.fa, "terminal/create", map[string]any{"sessionId": sid, "command": "bash"})
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()

	h.in <- agent.ChatMessage{Text: "do it"}
	readUntilTerminal(t, h.out)
	close(h.in)

	resp := <-gotResp
	require.NotNil(t, resp.Error, "a pending terminal request must resolve, not hang, when input closes")
	assert.Contains(t, resp.Error.Message, "input closed")

	require.NoError(t, <-h.chatErr)
	for range h.out {
	}
}

// Input close must resolve EVERY parked forwarded request, of every
// kind, in one pass. The permission and terminal brokers are the same algorithm
// twice over, so the close half has to remember to walk both maps — and every
// other input-closed test parks exactly ONE kind, which means a version that
// walked only one of them would still be green across the whole suite while
// hanging a real engine on the other.
//
// This test parks both at once. It sits at the seam ABOVE the two brokers (the
// engine's own JSON-RPC callbacks in, the replies out), so it is unchanged by
// any collapse of the duplication underneath it and red only if the shared
// behaviour actually diverges.
func TestChat_InputClosedResolvesBothBrokersAtOnce(t *testing.T) {
	h := startChat(t, agent.ChatRequest{ForwardPermissions: true, ForwardTerminal: true})

	permResp := make(chan rpcMessage, 1)
	termResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		p := make(chan rpcMessage, 1)
		tr := make(chan rpcMessage, 1)
		go func() { p <- h.fa.requestPermission(sid, permTestOptions) }()
		go func() {
			tr <- l0CallClient(h.fa, "terminal/create", map[string]any{"sessionId": sid, "command": "bash"})
		}()
		// The turn stays in flight until both callbacks are answered, so
		// teardown cannot close the transport out from under either reply.
		pv, tv := <-p, <-tr
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
		permResp <- pv
		termResp <- tv
	}()

	h.in <- agent.ChatMessage{Text: "do both"}

	// Drain until BOTH are parked upstream, so the close below has two
	// different kinds of pending request to resolve.
	var sawPerm, sawTerm bool
	for ev := range h.out {
		if ev.Permission != nil {
			sawPerm = true
		}
		if ev.Terminal != nil {
			sawTerm = true
		}
		if sawPerm && sawTerm {
			break
		}
	}
	require.True(t, sawPerm && sawTerm, "out closed before both requests were forwarded")

	close(h.in)

	// Each kind resolves in its own shape — that asymmetry is deliberate and is
	// the part a collapse must NOT flatten: a permission resolves as a
	// cancelled OUTCOME (neither approving nor remembering a rejection), a
	// terminal resolves as a JSON-RPC ERROR (an engine parked on terminal/create
	// must be told, not quietly told "ok").
	presp := <-permResp
	require.Nil(t, presp.Error, "a permission must not resolve as a protocol error")
	var body permissionResult
	require.NoError(t, json.Unmarshal(presp.Result, &body))
	assert.Equal(t, outcomeCancelled, body.Outcome.Outcome)

	tresp := <-termResp
	require.NotNil(t, tresp.Error, "a terminal request must not resolve as a silent success")
	assert.Contains(t, tresp.Error.Message, "input closed")

	require.NoError(t, <-h.chatErr)
	for range h.out {
	}
}

// Both brokers hand out sequential ids under their own kind's
// prefix, and an answer naming an id neither of them knows is dropped with a
// warning that says which kind it was. Pinned at the seam so it survives the
// two brokers being collapsed into one.
func TestChat_UnknownAnswerIdsAreReportedPerKind(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{ForwardPermissions: true, ForwardTerminal: true})

	gotPerm := make(chan *agent.PermissionRequest, 1)
	gotTerm := make(chan *agent.TerminalRequest, 1)
	permResp := make(chan rpcMessage, 1)
	termResp := make(chan rpcMessage, 1)
	go func() {
		sid := h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		p := make(chan rpcMessage, 1)
		tr := make(chan rpcMessage, 1)
		go func() { p <- h.fa.requestPermission(sid, permTestOptions) }()
		go func() {
			tr <- l0CallClient(h.fa, "terminal/create", map[string]any{"sessionId": sid, "command": "bash"})
		}()
		pv, tv := <-p, <-tr
		_ = h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
		permResp <- pv
		termResp <- tv
	}()

	h.in <- agent.ChatMessage{Text: "do both"}
	go func() {
		var p *agent.PermissionRequest
		var tr *agent.TerminalRequest
		for ev := range h.out {
			if ev.Permission != nil {
				p = ev.Permission
			}
			if ev.Terminal != nil {
				tr = ev.Terminal
			}
			if p != nil && tr != nil {
				break
			}
		}
		gotPerm <- p
		gotTerm <- tr
	}()

	perm := <-gotPerm
	term := <-gotTerm
	require.NotNil(t, perm)
	require.NotNil(t, term)
	assert.Equal(t, "perm-1", perm.ID, "permission ids are sequential under their own kind's prefix")
	assert.Equal(t, "term-1", term.ID, "terminal ids are sequential under their own kind's prefix")

	// Answers naming ids neither broker issued.
	h.in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: "perm-999", OptionID: "allow"}}
	h.in <- agent.ChatMessage{Terminal: &agent.TerminalResponse{ID: "term-999"}}
	close(h.in)

	<-permResp
	<-termResp
	require.NoError(t, <-h.chatErr)
	for range h.out {
	}

	got := warnings.String()
	assert.Contains(t, got, `permission answer for unknown request "perm-999"`)
	assert.Contains(t, got, `terminal answer for unknown request "term-999"`)
}

// TestChat_TransportError surfaces a spawn failure as a Chat error (and still
// closes out per the contract).
func TestChat_TransportError(t *testing.T) {
	b := NewACP()
	b.openTransport = func(context.Context, transportRequest) (*transport, error) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make(chan agent.ChatEvent, 1)
	in := make(chan agent.ChatMessage)
	err := b.Chat(context.Background(), agent.ChatRequest{}, in, out)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	_, ok := <-out
	assert.False(t, ok, "out must be closed even on transport failure")
}

// testModelQuirk is a fake ModelDeliveryQuirk standing in for
// claudeModelSelectionQuirk (internal/claude/chat.go), scoped to a fake
// agent identity/version rather than the real claude-code-acp one so these
// tests exercise applyModelQuirk's matching logic without depending on that
// backend's exact strings.
var testModelQuirk = &agent.ModelDeliveryQuirk{
	Method:          "session/set_model",
	AgentName:       "test-agent",
	AdapterVersions: []string{"1.0.0"},
}

// TestChat_ModelQuirk_VersionMismatch_WarnsAndSkips pins wasting-crinkle's
// fix: the connected agent IS the one the quirk targets (name matches) but
// at a version nobody verified — this is the silent-no-op-turned-loud case.
// Before the fix this path (like the other three below) returned nil with
// no signal at all; a user who upgraded claude-code-acp past 0.16.2 would
// silently lose model selection. It must now warn AND must not call the
// quirk's method (session/prompt comes right after session/new — no
// session/set_model in between).
func TestChat_ModelQuirk_VersionMismatch_WarnsAndSkips(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{Model: "haiku", ModelQuirk: testModelQuirk})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshakeAs(t, "test-agent", "1.2.3")
		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method, "an unverified version must not see the quirk's session/set_model call")
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	warned := warnings.String()
	assert.Contains(t, warned, "1.2.3", "names the connected version")
	assert.Contains(t, warned, "1.0.0", "names the version(s) the quirk covers")
	assert.Contains(t, warned, "test-agent", "names the agent")
	assert.Contains(t, warned, "haiku", "names the model that will not be applied")
	assert.Contains(t, warned, "NOT", "states the consequence plainly")
}

// TestChat_ModelQuirk_VersionMatch_CallsMethodSilently is the control: an
// EXACT AgentName+version match still fires the quirk's call, unchanged, and
// stays silent — the fix must not touch the verified-version path at all.
func TestChat_ModelQuirk_VersionMatch_CallsMethodSilently(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{Model: "haiku", ModelQuirk: testModelQuirk})
	events := collect(h.out)

	go func() {
		sid := h.fa.serveHandshakeAs(t, "test-agent", "1.0.0")

		quirkReq := <-h.fa.requests
		require.Equal(t, "session/set_model", quirkReq.Method, "a verified exact match must still fire the quirk's call")
		var params struct {
			SessionId string `json:"sessionId"`
			ModelId   string `json:"modelId"`
		}
		require.NoError(t, json.Unmarshal(quirkReq.Params, &params))
		assert.Equal(t, sid, params.SessionId)
		assert.Equal(t, "haiku", params.ModelId)
		require.NoError(t, h.fa.respond(quirkReq.ID, map[string]any{}))

		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method)
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Empty(t, warnings.String(), "a verified version match is the expected, unremarkable path and must not warn")
}

// TestChat_ModelQuirk_Nil_SilentNoOp: a nil ModelQuirk (every backend but
// claude today) is the ordinary case and must stay silent — only the
// name-match/version-miss combination is evidence of a broken expectation.
func TestChat_ModelQuirk_Nil_SilentNoOp(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{Model: "haiku"})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshake(t)
		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method)
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Empty(t, warnings.String(), "nil ModelQuirk is the normal case for every non-claude backend and must stay silent")
}

// TestChat_ModelQuirk_NameMismatch_SilentNoOp: a connected agent whose
// self-reported name does not match the quirk's AgentName is a DIFFERENT
// agent entirely, not a version-upgrade case — it must stay silent even
// though req.ModelQuirk is set and req.Model is non-empty.
func TestChat_ModelQuirk_NameMismatch_SilentNoOp(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	h := startChat(t, agent.ChatRequest{Model: "haiku", ModelQuirk: testModelQuirk})
	events := collect(h.out)

	go func() {
		_ = h.fa.serveHandshakeAs(t, "some-other-agent", "1.0.0")
		promptReq := <-h.fa.requests
		require.Equal(t, "session/prompt", promptReq.Method, "a different agent entirely must never see the quirk's call")
		require.NoError(t, h.fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"}))
	}()

	h.in <- agent.ChatMessage{Text: "hi"}
	close(h.in)
	require.NoError(t, <-h.chatErr)
	for range events() {
	}

	assert.Empty(t, warnings.String(), "a name mismatch means a different agent — must stay silent")
}
