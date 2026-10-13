package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// The *_connection_refused.json fixtures are CAPTURED: claude 2.1.292 on
// haiku, in a throwaway HOME/CLAUDE_CONFIG_DIR/repo, with ANTHROPIC_BASE_URL
// at a refused local port and a dummy key (nothing was reachable, so no
// credential was involved). claude retried ten times (api_retry frames,
// error_status null), then ended the turn on an assistant frame with error
// "server_error" and a result frame with api_error_status null.

// turnOf maps frames in order through one turn's stream.
func turnOf(t *testing.T, names ...string) []agent.ChatEvent {
	t.Helper()
	s := new(turnStream)
	var evs []agent.ChatEvent
	for _, n := range names {
		evs = append(evs, s.Map(fixture(t, n))...)
	}
	return evs
}

// withStatus is the captured result frame with api_error_status set.
func withStatus(t *testing.T, status int) []byte {
	t.Helper()
	var frame map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "result_connection_refused.json"), &frame))
	frame["api_error_status"] = status
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

// TestTurnStream_ConnectionRefused_EndpointUnreachable: claude's own
// connection-failure signal — a server_error on the turn's message and no
// HTTP status on its result — fails the turn endpoint_unreachable, once.
func TestTurnStream_ConnectionRefused_EndpointUnreachable(t *testing.T) {
	evs := turnOf(t, "api_retry_connection_refused.json", "assistant_connection_refused.json", "result_connection_refused.json")
	assert.Equal(t, []agent.TurnFailure{{Kind: agent.FailureEndpointUnreachable}}, failuresIn(evs))
}

// TestTurnStream_ServerErrorWithStatus_NotUnreachable: the server answered
// (a 500), so the endpoint was reached; a server_error with a status fails
// nothing, as before.
func TestTurnStream_ServerErrorWithStatus_NotUnreachable(t *testing.T) {
	s := new(turnStream)
	evs := s.Map(fixture(t, "assistant_connection_refused.json"))
	evs = append(evs, s.Map(withStatus(t, 500))...)
	assert.Empty(t, failuresIn(evs))
}

// TestTurnStream_StatuslessResultAlone_NotUnreachable: a result with no
// HTTP status is every successful turn's; without the turn's own
// server_error it says nothing about the endpoint.
func TestTurnStream_StatuslessResultAlone_NotUnreachable(t *testing.T) {
	assert.Empty(t, failuresIn(turnOf(t, "result_success.json")))
	assert.Empty(t, failuresIn(turnOf(t, "result_connection_refused.json")))
}

// TestTurnStream_SubagentServerError_NotTheTurns: a sub-agent's unreachable
// API is reported to the turn as a tool result, not as the turn's endpoint.
func TestTurnStream_SubagentServerError_NotTheTurns(t *testing.T) {
	var frame map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "assistant_connection_refused.json"), &frame))
	frame["parent_tool_use_id"] = "toolu_01VuNv2eXbKS3shVDAQ1zMBX"
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	s := new(turnStream)
	evs := s.Map(raw)
	evs = append(evs, s.Map(fixture(t, "result_connection_refused.json"))...)
	assert.Empty(t, failuresIn(evs))
}
