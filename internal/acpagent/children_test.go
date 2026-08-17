package acpagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitUpdate blocks until the next session/update notification arrives on
// the raw wire (independent of any request/response cycle — a child push is
// unsolicited, unlike the entries TestServe_FullTurn asserts via
// waitResponse's post-response drain).
func (c *testClient) waitUpdate() frame {
	c.t.Helper()
	select {
	case f := <-c.updates:
		return f
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for a session/update notification")
		return frame{}
	}
}

// TestServe_ChildUpdatePush is the hermetic acceptance test for Tier A push
// (manly-grant item 2): a frontend-shaped test observes a delegated child's
// event arrive by PUSH — no session/prompt in flight, no polling. The CLI's
// real WatchChildren (backed by ConsumerService.WatchRuns) is not exercised
// here; this pins the acpagent-side wiring (EngineChat.WatchChildren ->
// pushChildUpdates -> session/update) against a fake subscription, exactly
// the same boundary fakeEngine already draws for the engine substrate.
func TestServe_ChildUpdatePush(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	chat := eng.chat("")

	childUpdates := make(chan ChildUpdate, 4)
	cancelled := make(chan struct{})
	chat.WatchChildren = func(ctx context.Context) (<-chan ChildUpdate, func()) {
		return childUpdates, func() { close(cancelled) }
	}

	open := func(ctx context.Context, req OpenRequest) (*EngineChat, error) { return chat, nil }
	c := startServer(t, open)
	sid := c.handshake("/proj")

	childUpdates <- ChildUpdate{Harp: "child-1", Kind: ChildUpdateMessage, Text: "hello from child"}

	upd := c.waitUpdate()
	assert.Equal(t, "session/update", upd.Method)
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	require.NoError(t, json.Unmarshal(upd.Params, &params))
	assert.Equal(t, sid, params.SessionID, "the push lands on the PARENT session that opened it")
	assert.Equal(t, "agent_message_chunk", params.Update.SessionUpdate)
	assert.Contains(t, params.Update.Content.Text, "child-1")
	assert.Contains(t, params.Update.Content.Text, "hello from child")

	// Started/completed kinds carry a distinguishing prefix.
	childUpdates <- ChildUpdate{Harp: "child-1", Kind: ChildUpdateStarted, Text: "delegated task"}
	upd = c.waitUpdate()
	require.NoError(t, json.Unmarshal(upd.Params, &params))
	assert.Contains(t, params.Update.Content.Text, "started:")

	childUpdates <- ChildUpdate{Harp: "child-1", Kind: ChildUpdateCompleted, Text: "done"}
	upd = c.waitUpdate()
	require.NoError(t, json.Unmarshal(upd.Params, &params))
	assert.Contains(t, params.Update.Content.Text, "completed:")

	select {
	case <-cancelled:
		t.Fatal("the subscription must not be cancelled while the session is still live")
	default:
	}
}

// TestServe_ChildUpdatePush_NilIsANoop pins the degraded/no-coordinator
// posture: a session with no WatchChildren behaves
// exactly as it did before this feature existed — no push goroutine, no panic.
func TestServe_ChildUpdatePush_NilIsANoop(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	chat := eng.chat("")
	require.Nil(t, chat.WatchChildren)

	open := func(ctx context.Context, req OpenRequest) (*EngineChat, error) { return chat, nil }
	c := startServer(t, open)
	c.handshake("/proj")

	select {
	case f := <-c.updates:
		t.Fatalf("no session/update expected with WatchChildren nil, got %+v", f)
	case <-time.After(200 * time.Millisecond):
	}
}
