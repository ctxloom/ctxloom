package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingWrites is a journal file whose appends fail the way a full or broken
// disk does, recoverably — unlike a closed Store, which can never write again.
type failingWrites struct{ afero.File }

func (failingWrites) Write([]byte) (int, error) { return 0, errors.New("injected disk error") }

// breakAppends makes s's appends fail until the returned restore runs. Swapped
// under the store's own lock, so the writer goroutine never sees a torn file.
func breakAppends(s *Store) (restore func()) {
	s.mu.Lock()
	orig := s.f
	s.f = failingWrites{orig}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.f = orig
		s.mu.Unlock()
	}
}

// drainAcks returns the highest CommittedSeq among the acks queued on ch.
func drainAcks(ch *RunChannel) (max uint64, any bool) {
	for {
		select {
		case f := <-ch.send:
			if f.Ack != nil {
				any = true
				if f.Ack.CommittedSeq > max {
					max = f.Ack.CommittedSeq
				}
			}
		default:
			return max, any
		}
	}
}

// TestHandleEvent_AFailedRecordIsNotAcked: the run channel's Ack is the
// runner's only signal that an event is durable — it drops acked events and
// never re-sends them. An event whose record failed (a disk error, or a store
// already closed under a shutdown) used to be acked anyway: HandleEvent raised
// the channel's watermark before dispatching, the handler only warned, and the
// flush that followed acked through it, so the event was lost for good. Here
// the artifact manifest at seq 2 fails to journal; nothing at or past it may
// be acked, and the runner's in-order re-issue (seq 2, then 3) must be
// processed — not dropped as a duplicate — once the disk recovers.
func TestHandleEvent_AFailedRecordIsNotAcked(t *testing.T) {
	sp := newFakeSpawner(t, nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ch := &RunChannel{
		role:        "child-a",
		id:          Identity{Harp: "child-a", RunID: "run-a"},
		BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 64),
		completed:   make(chan struct{}),
	}
	manifest := func(id string) Event {
		return Event{RunID: "run-a", Payload: ArtifactProduced{ArtifactID: id, Name: id, SHA256: []byte(id)}}
	}
	at := func(ev Event, seq uint64) Event { ev.Seq = seq; return ev }
	item := Event{RunID: "run-a", Seq: 3, Payload: MessageStarted{}}

	c.HandleEvent(ch, at(manifest("first"), 1))
	acked, _ := drainAcks(ch)
	require.Equal(t, uint64(1), acked, "precondition: a recorded event is acked")

	restore := breakAppends(c.runs)
	c.HandleEvent(ch, at(manifest("second"), 2))
	c.HandleEvent(ch, item) // its own journal is healthy: only seq 2 failed
	acked, _ = drainAcks(ch)
	assert.Less(t, acked, uint64(2),
		"an event whose record failed must not be acked, nor anything after it — the runner drops "+
			"acked events and would never re-send it")
	restore()

	// The runner re-issues everything unacked, in order.
	c.HandleEvent(ch, at(manifest("second"), 2))
	c.HandleEvent(ch, item)
	acked, _ = drainAcks(ch)
	assert.Equal(t, uint64(3), acked, "once re-sent and recorded, the event and its successors are acked")
	ids := []string{}
	for _, a := range c.Artifacts("child-a") {
		ids = append(ids, a.ArtifactID)
	}
	assert.Equal(t, []string{"first", "second"}, ids, "the re-sent manifest must be recorded, not dropped as a duplicate")
}

// TestHandleEvent_AnIdleBoundaryThatFailsToJournalIsNotAcked: the same for a
// turn boundary — the shape that lost a run's idle state across a restart. A
// TurnIdle whose state fact fails to journal must stay unacked, and its re-send
// must fold the run idle.
func TestHandleEvent_AnIdleBoundaryThatFailsToJournalIsNotAcked(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	ch := &RunChannel{
		role:        out.Harp,
		id:          Identity{Harp: out.Harp, RunID: out.RunID},
		BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 64),
		completed:   make(chan struct{}),
	}
	started := Event{RunID: out.RunID, Seq: 1, Payload: CustomEvent{Name: CustomTurnStarted}}
	idle := Event{RunID: out.RunID, Seq: 2, Payload: CustomEvent{Name: CustomTurnIdle}}
	c.HandleEvent(ch, started)
	require.Equal(t, StateExecuting, rosterState(c, out.Harp))
	acked, _ := drainAcks(ch)
	require.Equal(t, uint64(1), acked)

	restore := breakAppends(c.runs)
	c.HandleEvent(ch, idle)
	acked, _ = drainAcks(ch)
	restore()
	assert.Less(t, acked, uint64(2), "a boundary whose state fact failed to journal must not be acked")

	c.HandleEvent(ch, idle) // the runner's re-send
	acked, _ = drainAcks(ch)
	assert.Equal(t, uint64(2), acked)
	assert.Equal(t, StateIdle, rosterState(c, out.Harp), "the re-sent boundary must fold the run idle")
}
