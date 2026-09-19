package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Inject answers the TUI in typed delivery modes; peerSend answers the sending
// agent in prose. Both describe the SAME observed state, so both must come from
// one classification — this table is the parity check that keeps them from
// drifting into two vocabularies that disagree about what a state means.
func TestDeliveryDisposition_OneClassificationForBothVocabularies(t *testing.T) {
	cases := []struct {
		state string
		mode  string
		prose string
	}{
		{StateEnded, DeliveryResumed, "child session had ended — resuming it with the message as its next turn"},
		{StateIdle, DeliveryNewTurn, "delivering as a new turn"},
		{StateQueued, DeliveryQueued, "queued: the child has not started yet; it will drain its mailbox after its first turn"},
		{StateExecuting, DeliveryQueued, "queued mid-turn: delivered at the child's next turn boundary"},
		{StateParked, DeliveryQueued, "queued mid-turn: delivered at the child's next turn boundary"},
		{"", DeliveryQueued, "queued mid-turn: delivered at the child's next turn boundary"},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			mode, prose := deliveryDisposition(c.state)
			assert.Equal(t, c.mode, mode)
			assert.Equal(t, c.prose, prose)
		})
	}
}

// TestDeliveryDisposition_ModeIsAlwaysAKnownDeliveryConstant: the TUI renders
// the mode verbatim, so an unclassified state must still resolve to one of the
// four documented modes rather than a bare state name leaking through.
func TestDeliveryDisposition_ModeIsAlwaysAKnownDeliveryConstant(t *testing.T) {
	known := map[string]bool{
		DeliveryCompletedRecv: true,
		DeliveryNewTurn:       true,
		DeliveryQueued:        true,
		DeliveryResumed:       true,
	}
	for _, state := range []string{StateQueued, StateExecuting, StateParked, StateIdle, StateEnded, "some-future-state"} {
		mode, prose := deliveryDisposition(state)
		assert.True(t, known[mode], "state %q resolved to unknown mode %q", state, mode)
		assert.NotEmpty(t, prose, "state %q must produce prose for the sender", state)
	}
}
