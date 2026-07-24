package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// engineDeathTail is the distinctive diagnostic a dying engine adapter writes
// to stderr — the shape internal/acp now captures and wraps into the Chat
// error, which enginehost.adapt turns into a FAILED RunCompleted's
// Result.Text. It stands in for the real 2026-07-24 evidence
// ("SyntaxError: Unexpected token 'with'", a JSON-RPC -32603 "Invalid API
// key") that was recoverable only via `docker logs` on a since-removed
// container.
const engineDeathTail = "acp: connection closed (engine stderr tail: SyntaxError: Unexpected token 'with')"

// TestTerminateRun_DeadEngineReasonReachesParentMailbox is the coordinator
// half of the engine-can-say-why-it-died work. A migrated child that dies
// BELOW the protocol (its adapter never answers a JSON-RPC call) emits a
// FAILED RunCompleted whose Result.Text carries the stderr tail, then reports
// RunExited. Before this change the parent learned only
// "agent ... exited (runner-exit)" — no reason anywhere, the 49-minute dead
// end. Asserting merely that the parent got SOME terminal notice is the exact
// failure this test exists to prevent: the assertion is on the REASON TEXT.
func TestTerminateRun_DeadEngineReasonReachesParentMailbox(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// Reach a live, journaled RunChannel so the synthetic FAILED terminal is
	// indistinguishable from a genuine one on the same (run_id, channel).
	require.Eventually(t, func() bool {
		var seq uint64
		c.mu.Lock()
		if ch := c.chans[out.Harp]; ch != nil {
			seq = ch.ackSeq
		}
		c.mu.Unlock()
		return seq > 0
	}, conformanceWait, 5*time.Millisecond, "the RunChannel must be live before the terminal is driven")

	var credHash string
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			credHash = r.CredHash
		}
	})
	require.NotEmpty(t, credHash)

	// The engine dies below the protocol: a FAILED RunCompleted whose reason
	// IS the stderr tail (exactly what enginehost.adapt emits when
	// backend.Chat returns the acp-wrapped error), with NO final-channel
	// output — so bridgeTurnResult has nothing to deliver and the terminal
	// notice is the child's only voice.
	c.mu.Lock()
	ch := c.chans[out.Harp]
	seq := ch.ackSeq + 1
	c.mu.Unlock()
	require.NotNil(t, ch)
	c.handleAgentEvent(ch, &agentcoordpb.AgentEvent{
		RunId: out.RunID,
		Seq:   seq,
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{
				Status: agentcoordpb.Result_RUN_STATUS_FAILED,
				Text:   engineDeathTail,
			},
		}},
	})

	// The runner then reports the process-level exit (CauseRunnerExit), which
	// terminates the run and mails the parent.
	c.handleRunExited(credHash, &agentcoordpb.RunExited{RunId: out.RunID, TerminalEventSeen: true})

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 2*time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, msgs, "the parent must learn the child died")

	var body string
	for _, m := range msgs {
		if m.From == out.Harp {
			body = m.Body
		}
	}
	require.NotEmpty(t, body, "a terminal notice from the dead child must be in the parent's mailbox")
	assert.True(t, strings.Contains(body, "SyntaxError: Unexpected token 'with'"),
		"the dead engine's own reason (the stderr tail) must reach the parent — a bare 'exited (runner-exit)' is the silent dead end this whole change exists to fix; got: %q", body)
}
