package acpagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServe_AnnouncementArrivesBeforeSessionNewReply is the unit-level proof
// (in-memory pipes, no subprocess) that the ISO3 posture announcement is
// queued as a session/update notification BEFORE the session/new response
// itself — waitResponse only returns once the response frame arrives, and
// drainUpdates (called from inside it) collects every notification already
// written by then; a server that only emitted the announcement later
// (e.g. from inside a turn) would leave found nil here.
func TestServe_AnnouncementArrivesBeforeSessionNewReply(t *testing.T) {
	eng := newFakeEngine()
	eng.initSummary = "ctxloom: session initialization summary\n  isolation : HOST process (no container); working directory /proj (no worktree) — NOT isolated on either axis"
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, updates := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	found := findAgentMessageChunkText(t, updates)
	require.NotEmpty(t, found, "expected an agent_message_chunk session/update notification carrying the posture announcement, queued ahead of the session/new response")
	assert.Equal(t, eng.initSummary, found)
}

// TestServe_AnnouncementArrivesBeforeSessionLoadReply mirrors the above for
// session/load: a resumed session's freshly re-dialed engine gets its OWN
// posture announcement, delivered before the loadSessionResult reply and
// before the recorded-history replay notifications.
func TestServe_AnnouncementArrivesBeforeSessionLoadReply(t *testing.T) {
	eng := newFakeEngine()
	eng.initSummary = "ctxloom: session initialization summary\n  agent     : no agent bound (profile flow)"
	eng.harp = "resumed-harp"
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, updates := c.waitResponse(c.send("session/load", `{"cwd":"/proj","sessionId":"resumed-harp","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	found := findAgentMessageChunkText(t, updates)
	require.NotEmpty(t, found, "expected the resumed session's own posture announcement ahead of the session/load reply")
	assert.Equal(t, eng.initSummary, found)
}

// findAgentMessageChunkText scans notification frames for an
// agent_message_chunk session/update (the SDK's SessionUpdate marshals as a
// FLAT object discriminated by a top-level "sessionUpdate" string, not a
// nested "agentMessageChunk" key — see coder/acp-go-sdk's generated
// MarshalJSON) and returns its content text, or "" if none matched.
func findAgentMessageChunkText(t *testing.T, updates []frame) string {
	t.Helper()
	for _, u := range updates {
		var params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(u.Params, &params); err != nil {
			continue
		}
		if params.Update.SessionUpdate == "agent_message_chunk" {
			return params.Update.Content.Text
		}
	}
	return ""
}

// TestServe_AnnouncementAbsentWhenUnset proves the nil-safe guard
// (sessionInitSummaryUpdateWire) never fabricates a frame: a test double
// that leaves EngineChat.InitSummary unset ("") emits no agent_message_chunk
// at all from emitSessionInitSummary — production always sets it, but this
// pins the no-op contract emitUpdate's nil convention promises.
func TestServe_AnnouncementAbsentWhenUnset(t *testing.T) {
	eng := newFakeEngine() // init summary left ""
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	_, updates := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	assert.Empty(t, findAgentMessageChunkText(t, updates), "an unset init summary must not fabricate an agent_message_chunk frame")
}
