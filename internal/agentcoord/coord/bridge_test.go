package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blunt-whiff: the automatic child→parent RESULT BRIDGE. Before this, the
// bridge fired only for a backend that does NOT implement
// agent.StructuredChat — and every registered backend does, so in production
// it never fired at all and a delegated child's ONLY way to report was for
// its model to decide to call agent_send. These tests pin the two properties
// a coordinator actually needs: the parent ALWAYS receives the child's
// result, and NEVER receives it twice.

// TestBridge_LegacyChatChildResultReachesParent proves the bridge no longer
// keys off the StructuredChat type assertion: a LEGACY (non-StartRun) chat
// child — the branch whose own comment used to concede "today, no production
// backend; only test doubles" — now reports automatically too.
func TestBridge_LegacyChatChildResultReachesParent(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// "ok" is the scripted engine's own assistant output for the turn — the
	// content, not a pin on "a call returned nil".
	msgs := recvBody(t, c, "ok", 5*time.Second)
	require.Len(t, msgs, 1, "the legacy chat child's turn result must reach the parent's mailbox")
	assert.Equal(t, "result", msgs[0].Kind)
}

// TestBridge_NoDoubleDeliveryWhenChildAlsoReports is the other half: a child
// that calls agent_send ITSELF during a turn is not re-reported by the
// bridge. The parent sees the child's OWN words exactly once, and never the
// bridged copy of the same turn.
//
// The child's send is issued while its turn is GATED (mid-turn, before the
// engine has emitted a single entry) — deliberately the hardest ordering: a
// naive "reset the accumulator at turn start" would race that report away
// and double-deliver.
func TestBridge_NoDoubleDeliveryWhenChildAlsoReports(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{turnGate: gate} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// The engine has taken the briefing and is parked on the gate: the turn
	// is open and nothing has been emitted yet.
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}
	_, err = c.AgentSend(child, ParentAddress, "result", "CHILD-OWN-REPORT", nil, "")
	require.NoError(t, err)

	close(gate) // let the turn finish: it emits "echo: ..." and completes

	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle },
		conformanceWait, 10*time.Millisecond)

	// Drain everything the parent can see and assert on the CONTENT.
	var all []Message
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		msgs, rerr := c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond)
		if rerr == nil {
			all = append(all, msgs...)
		}
	}
	var own, bridged int
	for _, m := range all {
		switch {
		case m.Body == "CHILD-OWN-REPORT":
			own++
		case m.From == out.Harp:
			bridged++
		}
	}
	assert.Equal(t, 1, own, "the child's own report must arrive exactly once")
	assert.Zero(t, bridged, "the bridge must not re-report a turn the child already reported (got %d extra messages from %s)", bridged, out.Harp)
}
