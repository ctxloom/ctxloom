package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestControlCapabilities_IsTheFiveVerbs pins the advertised control vocabulary
// against the plane-2 request kinds it names. A cap that no verb corresponds to
// (or a verb with no cap) is how a send-side guard silently stops guarding.
func TestControlCapabilities_IsTheFiveVerbs(t *testing.T) {
	assert.Equal(t, []string{CapSteer, CapQuestion, CapSummarize, CapPause, CapResume}, ControlCapabilities())
	assert.NotContains(t, ControlCapabilities(), CapPeerMessaging,
		"peer_messaging is not a control capability — every runner has it, engine or not")
}

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
	assert.Equal(t, []string{CapPeerMessaging, CapTerminalDelivery}, RunnerCapabilities(false))
	assert.Equal(t, append([]string{CapPeerMessaging}, ControlCapabilities()...), RunnerCapabilities(true))
	assert.NotContains(t, RunnerCapabilities(true), CapTerminalDelivery,
		"a runner that hosts an engine is driven structurally; its turn boundary owns delivery")
}

// TestHomeHelloCapabilities_DefaultsToPeerMessagingOnly: HomeConfig.Capabilities
// left unset keeps the advertisement it has always had, so the Hello of a runner
// nobody has taught about capabilities does not silently become empty.
func TestHomeHelloCapabilities_DefaultsToPeerMessagingOnly(t *testing.T) {
	assert.Equal(t, []string{CapPeerMessaging}, (&Home{}).helloCapabilities())
	h := &Home{cfg: HomeConfig{Capabilities: RunnerCapabilities(true)}}
	assert.Equal(t, RunnerCapabilities(true), h.helloCapabilities())
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
	h, err := NewHome(ctx, HomeConfig{
		URL: url, Token: token, Harness: "test", Version: "test",
		Capabilities: RunnerCapabilities(true),
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
	for _, want := range RunnerCapabilities(true) {
		assert.True(t, caps[want], "capability %q must be captured from the Hello", want)
	}
	assert.Len(t, caps, len(RunnerCapabilities(true)), "nothing beyond the advertisement is recorded")

	// And the guard reads it: an advertised capability passes, one the runner
	// never claimed is refused naming both the gap and the advertisement.
	assert.NoError(t, c.runCapability(ownerHarp, CapSteer))
	err = c.runCapability(ownerHarp, "teleport")
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "teleport", "the refusal names the missing capability")
	assert.Contains(t, err.Error(), CapSteer, "the refusal names what WAS advertised")
}

// TestRunCapability_EnginelessRunnerIsRefusedWithItsAdvertisement: the legacy /
// engineless case — the runner is attached and healthy, it simply cannot do the
// thing. The refusal must say so at command time, naming peer_messaging as all
// it offered, rather than letting the request travel and come back UNIMPLEMENTED.
func TestRunCapability_EnginelessRunnerIsRefusedWithItsAdvertisement(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	c.mu.Lock()
	c.chans["legacy-harp"] = &runChan{role: "legacy-harp", caps: map[string]bool{CapPeerMessaging: true}}
	c.mu.Unlock()

	err := c.runCapability("legacy-harp", CapPause)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), CapPause)
	assert.Contains(t, err.Error(), CapPeerMessaging)
}

// TestRunCapability_NoAttachedChannelIsRefusedNotAssumed: with no channel there
// is no advertisement, and "no advertisement" must never read as "everything is
// available". Fail closed, and say which state the caller is actually in.
func TestRunCapability_NoAttachedChannelIsRefusedNotAssumed(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	err := c.runCapability("nobody-harp", CapSteer)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "nobody-harp")
	assert.Contains(t, err.Error(), CapSteer)
}
