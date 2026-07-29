package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every failure inside DialRunner must unwind the SAME way: no link handed
// back, the link context cancelled, the connection closed. The paths are
// indistinguishable from outside except by their message, so these tests pin
// the messages — that is what tells a redial loop which failure it hit — and
// pin that nothing is returned alongside an error.

func TestDialRunner_UnparseableURLIsRefusedBeforeAnyDial(t *testing.T) {
	link, err := DialRunner(context.Background(), "://not-a-url", "tok", "run-1", "mock", "test", nil)
	assert.Nil(t, link, "a failed dial hands back no link")
	require.Error(t, err)
	assert.Contains(t, err.Error(), EnvCoordURL, "the message must name the variable that carried the bad URL")
}

func TestDialRunner_URLWithoutAHostIsRefused(t *testing.T) {
	link, err := DialRunner(context.Background(), "http:///mcp", "tok", "run-1", "mock", "test", nil)
	assert.Nil(t, link)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no host")
}

// TestDialRunner_HelloAgainstNoListenerFailsAndReturnsNoLink drives the
// handshake unwind: grpc.NewClient is lazy, so the failure surfaces at the
// RunnerHello/HelloAck round trip against a port nothing is serving.
func TestDialRunner_HelloAgainstNoListenerFailsAndReturnsNoLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Port 1 on loopback: reserved, never bound by a test coordinator.
	link, err := DialRunner(ctx, "http://127.0.0.1:1/mcp", "tok", "run-1", "mock", "test", nil)
	assert.Nil(t, link, "a failed handshake hands back no link, so nothing can Shutdown a torn-down conn")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coord: ", "every dial failure is attributed to the coordinator link")
}

// TestDialRunner_RejectedHelloIsReportedAsARejection: a coordinator that
// answers the Hello with accepted=false must not read as a transport error —
// the redial loop treats the two differently.
func TestDialRunner_HelloClaimingAnUnownedRunIsRejected(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner("owner-harp")
	require.NoError(t, err)

	// A run id this credential does not own: the ownership check rejects the
	// Hello, which unwinds through the ack-rejected path rather than a
	// transport error — the redial loop treats the two differently.
	link, err := DialRunner(context.Background(), c.LoopbackURL(), token, "run-nobody-owns", "mock", "test", nil)
	assert.Nil(t, link, "a rejected Hello hands back no link")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coord: ", "every dial failure is attributed to the coordinator link")
}
