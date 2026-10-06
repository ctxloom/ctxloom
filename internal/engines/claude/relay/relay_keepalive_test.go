package relay_test

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/engines/claude/relay"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The forced durations: the endpoint closes a session idle for idleTimeout,
// the relay pings every keepAlive, and the tests go idle for idleGap — longer
// than the timeout, so a session nothing kept alive is gone by its end.
const (
	idleTimeout = 400 * time.Millisecond
	keepAlive   = 40 * time.Millisecond
	idleGap     = 2 * idleTimeout
)

// The relay keeps its sessions alive well inside the endpoint's idle timeout:
// a session that missed a ping or two must still be alive.
func TestRelay_KeepsAliveWellInsideTheEndpointsIdleTimeout(t *testing.T) {
	assert.LessOrEqual(t, relay.KeepAliveInterval, interaction.IdleSessionTimeout/4)
}

// The wake session subscribes once and then never asks anything, so only the
// relay's keepalive stands between it and the endpoint's idle timeout: after
// a gap longer than the timeout, a fired wake still reaches claude.
func TestRelay_TheWakeOutlivesAnIdleGapLongerThanTheSessionTimeout(t *testing.T) {
	url, sig := endpointTimingOut(t, idleTimeout)
	sock, posts := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	startRelayKeepingAlive(t, env, keepAlive)
	fireOnceSubscribed(t, sig)
	testsupport.Await(t, bound, posts, "the first wake was not posted")

	time.Sleep(idleGap)

	require.NoError(t, sig.Fire(context.Background(), nonce), "the wake subscription outlives the idle gap")
	testsupport.Await(t, bound, posts, "the wake after the idle gap was not posted")
}

// claude's own session through the relay is just as idle between tool calls
// while the human is away: the next call after a gap longer than the timeout
// still reaches the endpoint.
func TestRelay_AToolCallAfterAnIdleGapLongerThanTheSessionTimeoutSucceeds(t *testing.T) {
	url, _ := endpointTimingOut(t, idleTimeout)
	cs, _, _ := startRelayKeepingAlive(t, baseEnv(url), keepAlive)
	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	time.Sleep(idleGap)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/gamma"}}})
	require.NoError(t, err, "the call after the idle gap reaches the endpoint")
	assert.False(t, res.IsError, "%v", res.Content)
}
