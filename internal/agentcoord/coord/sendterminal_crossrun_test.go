package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

func terminalEvent(seq uint64, runID string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		Seq: seq, RunId: runID,
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}},
	}
}

func nonTerminalEvent(seq uint64, runID string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		Seq: seq, RunId: runID,
		Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "fill"}},
	}
}

// sendTerminal's eviction was payload-blind (`case <-ch:` with no inspection).
// Both production subscribers watch ALL runs (WatchRuns(nil)), so one ring
// carries many runs' terminal events — and a terminal is the ONE event whose
// loss is unrecoverable (no seq gap reveals it; adaptConsumerFeed ends its feed
// only on RunCompleted, so its watcher hangs forever). Making room for run A's
// terminal by evicting the oldest queued event therefore destroyed run B's
// terminal — the very invariant sendTerminal exists to protect.
//
// The eviction must sacrifice only a recoverable NON-terminal, never another
// run's terminal.
func TestSendTerminal_EvictionNeverConsumesAnotherRunsTerminal(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 2)
	// Head is run B's terminal (the event the blind eviction used to destroy),
	// followed by a recoverable non-terminal. The ring is now full.
	ch <- terminalEvent(1, "run-B")
	ch <- nonTerminalEvent(2, "run-A")

	// Run A's terminal needs room. The oldest queued event is run B's terminal;
	// it must NOT be the one evicted.
	sendTerminal(ch, terminalEvent(3, "run-A"))

	require.Len(t, ch, 2, "sendTerminal must not grow the ring — evict one, then place one")

	var terminals []string
	for len(ch) > 0 {
		ev := <-ch
		if isTerminal(ev) {
			terminals = append(terminals, ev.GetRunId())
		}
	}
	assert.Contains(t, terminals, "run-B",
		"evicting run A's terminal must not consume run B's terminal — B's watcher still needs it")
	assert.Contains(t, terminals, "run-A",
		"run A's own terminal must still be placed")
}

// The complementary guarantee: when a recoverable non-terminal IS available to
// evict, it — and only it — is sacrificed, so cross-run terminal protection
// does not come at the cost of never making room.
func TestSendTerminal_EvictsTheNonTerminalNotTheTerminal(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 2)
	ch <- terminalEvent(1, "run-B") // must survive
	ch <- nonTerminalEvent(2, "run-A")

	sendTerminal(ch, terminalEvent(3, "run-A"))

	// The surviving events are exactly B's terminal and A's new terminal; the
	// non-terminal (seq 2) is the one that was dropped.
	seqs := map[uint64]bool{}
	for len(ch) > 0 {
		seqs[(<-ch).GetSeq()] = true
	}
	assert.False(t, seqs[2], "the recoverable non-terminal (seq 2) must be the evicted one")
	assert.True(t, seqs[1], "run B's terminal (seq 1) must survive")
	assert.True(t, seqs[3], "run A's terminal (seq 3) must be delivered")
}
