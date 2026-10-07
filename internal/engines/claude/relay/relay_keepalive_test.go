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

// keepAlive is the relay's forced ping interval: short, so the tests see
// several pings, and never a deadline — the tests count pings, they do not
// time them.
const keepAlive = 20 * time.Millisecond

// The relay keeps its sessions alive well inside the endpoint's idle timeout:
// a session that missed a ping or two must still be alive.
func TestRelay_KeepsAliveWellInsideTheEndpointsIdleTimeout(t *testing.T) {
	assert.LessOrEqual(t, relay.KeepAliveInterval, interaction.IdleSessionTimeout/4)
}

// awaitPings returns the session of the next n pings on the wake's session
// (wake) or on claude's (!wake), requiring them all on one session: a
// keepalive that reconnected would be pinging a session that lost what the
// first one held.
func awaitPings(t *testing.T, c *sessionCutter, wake bool, n int) string {
	t.Helper()
	session := ""
	for n > 0 {
		p := testsupport.Await(t, bound, c.pings, "the relay stopped pinging")
		if p.wake != wake {
			continue
		}
		if session == "" {
			session = p.session
		}
		require.Equal(t, session, p.session, "the keepalive moved to another session")
		n--
	}
	return session
}

// startKeptAlive runs the relay through a sessionCutter, keeping alive every
// keepAlive, with claude's messaging socket bound, and returns the cutter,
// the endpoint's wake signal, claude's session and its posts.
func startKeptAlive(t *testing.T) (*sessionCutter, *interaction.WakeSignal, *mcp.ClientSession, <-chan []string) {
	t.Helper()
	endpointURL, sig := endpoint(t)
	cutter, url := newSessionCutter(t, endpointURL)
	sock, posts := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	cs, _, _ := startRelayKeepingAlive(t, env, keepAlive)
	testsupport.Await(t, bound, cutter.subscribed, "the relay never subscribed to the wake")
	return cutter, sig, cs, posts
}

// The wake session subscribes once and then never asks anything, so only the
// relay's keepalive resets the endpoint's idle clock for it: the session that
// holds the subscription is pinged, again and again, and a wake fired after
// those pings still reaches claude.
func TestRelay_KeepsTheWakeSessionAlive(t *testing.T) {
	cutter, sig, _, posts := startKeptAlive(t)

	awaitPings(t, cutter, true, 3)

	require.NoError(t, sig.Fire(context.Background(), nonce), "the pinged session still holds the subscription")
	testsupport.Await(t, bound, posts, "the wake after the pings was not posted")
}

// claude's own session through the relay is just as idle between tool calls
// while the human is away: it is pinged too, and a call after those pings
// still reaches the endpoint.
func TestRelay_KeepsClaudesSessionAlive(t *testing.T) {
	cutter, _, cs, _ := startKeptAlive(t)

	awaitPings(t, cutter, false, 3)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/gamma"}}})
	require.NoError(t, err, "the call after the pings reaches the endpoint")
	assert.False(t, res.IsError, "%v", res.Content)
}

// A ping the endpoint is slow to answer is not a lost session: the endpoint
// holds its idle clock while a request is in flight, so only the endpoint
// ending the session loses it. The relay waits on the wake's ping for as long
// as the endpoint takes — here, until claude's session has been pinged three
// times more, which is past any deadline a ping could carry that is shorter
// than the interval — and the wake's subscription survives on the same
// session.
func TestRelay_ASlowPingDoesNotCostTheWakeItsSubscription(t *testing.T) {
	cutter, sig, _, posts := startKeptAlive(t)
	cutter.holdWakePing()
	testsupport.Await(t, bound, cutter.held, "the wake's session was never pinged")

	for seen := 0; seen < 3; {
		select {
		case <-cutter.abandoned:
			require.FailNow(t, "the relay abandoned a ping the endpoint was still answering")
		case p := <-cutter.pings:
			if !p.wake {
				seen++
			}
		case <-time.After(bound):
			require.FailNow(t, "claude's session stopped being pinged")
		}
	}
	cutter.releaseWakePing()

	awaitPings(t, cutter, true, 1)
	require.NoError(t, sig.Fire(context.Background(), nonce), "the wake held its subscription through the slow ping")
	testsupport.Await(t, bound, posts, "the wake after the slow ping was not posted")
	assert.Empty(t, cutter.subscribed, "the wake never had to subscribe again")
}
