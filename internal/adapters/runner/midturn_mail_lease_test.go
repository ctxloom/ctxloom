package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMidTurnMail_APeekThatHandsOutNothingLeasesNothing: when not even the
// notice that mail waits fits the cap, the peek hands out nothing — no lease,
// no ids — and the message is free for the next peek.
func TestMidTurnMail_APeekThatHandsOutNothingLeasesNothing(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "WAITING "+strings.Repeat("w", 400))

	tiny := home.PeekMail(10)
	assert.Empty(t, tiny.Lease)
	assert.Empty(t, tiny.IDs)

	next := home.PeekMail(7500)
	require.Len(t, next.IDs, 1, "nothing was held back by the empty peek")
}

// TestMidTurnMail_NoLeaseConfirmsNothing: the empty lease is no lease — a
// confirm under it delivers nothing, not even a message no peek holds.
func TestMidTurnMail_NoLeaseConfirmsNothing(t *testing.T) {
	_, home, _ := gatedRunOverHome(t)
	mailArrives(t, home, "UNLEASED")
	home.mu.Lock()
	ids := append([]string(nil), home.pendingOrder...)
	home.mu.Unlock()
	require.NotEmpty(t, ids)

	assert.Zero(t, home.ConfirmMail("", ids))
	assert.True(t, home.holdsMail("UNLEASED"), "still waiting for its turn")
}
