package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunChannel_CapturesHelloCapabilities is the round trip: what a runner
// advertises on its Hello is what the coordinator holds for that run. Written as
// an end-to-end dial because the two ends are the point — the field has been in
// the contract all along and neither side read it.
func TestRunChannel_CapturesHelloCapabilities(t *testing.T) {
	// No shipped runner advertises anything; the round trip is about the
	// field, so the advertisement is synthetic.
	testAdvertisement := []string{"test_capability"}
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
		Capabilities: testAdvertisement,
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
	for _, want := range testAdvertisement {
		assert.True(t, caps[want], "capability %q must be captured from the Hello", want)
	}
	assert.Len(t, caps, len(testAdvertisement), "nothing beyond the advertisement is recorded")

}
