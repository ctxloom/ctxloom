package coord

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// supersededRun is an idle run whose live channel the test owns: the real
// runner's channel is superseded by `old`, and the runner's own reattach is
// held off by a long redial backoff, so every frame either channel handles is
// one the test placed.
type supersededRun struct {
	c     *Coordinator
	harp  string
	runID string
	base  uint64
	old   *RunChannel
}

func newSupersededRun(t *testing.T, set func(*Options)) *supersededRun {
	t.Helper()
	resetStrictness(t)
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })
	sp.redialBackoff = time.Hour
	c := newTestCoordinatorWith(t, sp, set)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	awaitItemsDurable(t, c, sp, out.Harp)
	base := c.eventWatermark(out.RunID)
	require.NotZero(t, base, "premise: the briefing's turn raised the watermark")

	old, err := c.AttachRun(Identity{Harp: out.Harp, RunID: out.RunID}, RunHello{RunID: out.RunID, ResumeFromSeq: base}, func() {})
	require.NoError(t, err)
	return &supersededRun{c: c, harp: out.Harp, runID: out.RunID, base: base, old: old}
}

func (r *supersededRun) reattach(t *testing.T) *RunChannel {
	t.Helper()
	ch, err := r.c.AttachRun(Identity{Harp: r.harp, RunID: r.runID}, RunHello{RunID: r.runID, ResumeFromSeq: r.base}, func() {})
	require.NoError(t, err)
	return ch
}

func (r *supersededRun) slotHeld() bool {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	rt := r.c.attach[r.runID]
	return rt != nil && rt.slot == slotHeld
}

func (r *supersededRun) boundary(name string, seq uint64) Event {
	return Event{RunID: r.runID, Seq: seq, Payload: CustomEvent{Name: name}}
}

// TestHandleEvent_AFrameFromASupersededChannelChangesNothing FORCES the
// ordering serveTracked leaves open: a reconnect supersedes a run channel
// while the old stream's reader still holds a frame it has not handled
// (serveTracked returns on the stream's cancel without joining its reader, and
// AttachRun does not wait for it either). The runner never saw that frame
// acked, so it re-sends it — and what follows — on the new channel. When the
// old reader finally handles its copy, the successor has already folded the
// turn's start AND its end.
//
// The stale TurnStarted must change nothing: folded, it would put an idle run
// back to executing and take a slot no boundary will ever release.
func TestHandleEvent_AFrameFromASupersededChannelChangesNothing(t *testing.T) {
	r := newSupersededRun(t, func(*Options) {})
	c := r.c
	successor := r.reattach(t)

	c.HandleEvent(successor, r.boundary(CustomTurnStarted, r.base+1))
	c.HandleEvent(successor, r.boundary(CustomTurnIdle, r.base+2))
	require.Equal(t, StateIdle, rosterState(c, r.harp), "premise: the successor folded the whole turn")
	require.False(t, r.slotHeld(), "premise: the turn's end yielded its slot")

	// The old reader's in-flight frame, handled after the takeover.
	c.HandleEvent(r.old, r.boundary(CustomTurnStarted, r.base+1))

	assert.Equal(t, StateIdle, rosterState(c, r.harp), "a superseded channel's stale turn start put the idle run back to executing")
	assert.False(t, r.slotHeld(), "a superseded channel's stale turn start took a slot nothing will release")
	assert.Equal(t, r.base+2, c.eventWatermark(r.runID))
}

// TestHandleEvent_ASupersededReaderStillInsideAFrameHoldsTheSuccessorBack
// FORCES the other half of that ordering: the old reader is INSIDE
// HandleEvent — parked at its TurnIdle's fold (turnIdleHook) — when the
// reconnect takes over. The successor must not fold the runner's re-send, and
// the next turn's start, underneath it: the stale idle would land after the
// new turn's start and idle a run that is executing, yielding its slot.
func TestHandleEvent_ASupersededReaderStillInsideAFrameHoldsTheSuccessorBack(t *testing.T) {
	var armed atomic.Bool
	atIdle, letIdle := make(chan struct{}), make(chan struct{})
	var reached, released sync.Once
	t.Cleanup(func() { released.Do(func() { close(letIdle) }) })
	r := newSupersededRun(t, func(o *Options) {
		o.turnIdleHook = func(string) {
			if armed.Load() {
				reached.Do(func() { close(atIdle) })
				<-letIdle
			}
		}
	})
	c := r.c
	c.HandleEvent(r.old, r.boundary(CustomTurnStarted, r.base+1))
	require.Equal(t, StateExecuting, rosterState(c, r.harp))

	armed.Store(true)
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		c.HandleEvent(r.old, r.boundary(CustomTurnIdle, r.base+2))
	}()
	testsupport.Within(t, 5*time.Second, func() struct{} { return <-atIdle }, "the old reader never reached its TurnIdle fold")
	armed.Store(false)

	// THE TAKEOVER, with the old reader inside HandleEvent.
	successor := r.reattach(t)
	if successor.handling.TryLock() {
		successor.handling.Unlock()
		t.Fatal("the successor can handle frames while the superseded reader is still inside one")
	}

	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		c.HandleEvent(successor, r.boundary(CustomTurnIdle, r.base+2)) // the runner's re-send
		c.HandleEvent(successor, r.boundary(CustomTurnStarted, r.base+3))
	}()
	released.Do(func() { close(letIdle) })
	testsupport.Within(t, 5*time.Second, func() struct{} { <-oldDone; return struct{}{} }, "the old reader's frame never finished")
	testsupport.Within(t, 5*time.Second, func() struct{} { <-newDone; return struct{}{} }, "the successor's frames never finished")

	assert.Equal(t, StateExecuting, rosterState(c, r.harp), "the next turn is in flight")
	assert.True(t, r.slotHeld(), "the next turn holds its slot")
	assert.Equal(t, r.base+3, c.eventWatermark(r.runID))
}
