package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunnerCapabilities_EnginePresenceDecidesTheAdvertisement: the control caps
// appear only when the runner actually hosts an engine that could execute them.
// An engineless runner advertising them would make the send-side guard pass and
// the request die at the far end instead — the exact experience the guard exists
// to replace.
//
// The engineless arm carries CapTerminalDelivery for the mirrored reason: having
// no engine is exactly what leaves it with no turn boundary to receive mail
// behind, so it must say so or its mail is never pushed at all. The two arms are
// complements, not a list — every runner advertises how it can be reached.
func TestRunnerCapabilities_EnginePresenceDecidesTheAdvertisement(t *testing.T) {
	assert.Equal(t, []string{CapTerminalDelivery}, RunnerCapabilities(false))
	assert.Empty(t, RunnerCapabilities(true))
	assert.NotContains(t, RunnerCapabilities(true), CapTerminalDelivery,
		"a runner that hosts an engine is driven structurally; its turn boundary owns delivery")
}

// TestRunChannel_CapturesHelloCapabilities is the round trip: what a runner
// advertises on its Hello is what the coordinator holds for that run. Written as
// an end-to-end dial because the two ends are the point — the field has been in
// the contract all along and neither side read it.
func TestRunChannel_CapturesHelloCapabilities(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	url, err := c.ReachURL("host")
	require.NoError(t, err)

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      url, Token: token, Harness: "test", Version: "test",
		Capabilities: RunnerCapabilities(false), // the one advertisement that carries a string today
		Harp:         "child-harp-1",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.chans[ownerHarp] != nil && len(c.chans[ownerHarp].caps) > 0
	}, 10*time.Second, 20*time.Millisecond, "the run channel must attach and record its advertisement")

	c.mu.Lock()
	caps := c.chans[ownerHarp].caps
	c.mu.Unlock()
	for _, want := range RunnerCapabilities(false) {
		assert.True(t, caps[want], "capability %q must be captured from the Hello", want)
	}
	assert.Len(t, caps, len(RunnerCapabilities(false)), "nothing beyond the advertisement is recorded")

}
