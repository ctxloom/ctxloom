package coord

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ackDropper is an ackHook that, once armed for a harp, drops every Ack to
// that harp's run channel: the runner keeps holding events this coordinator
// has already RECORDED, exactly as when a crash falls after the records and
// before the acks reach it.
type ackDropper struct{ harp atomic.Value }

func newAckDropper() *ackDropper {
	d := &ackDropper{}
	d.harp.Store("")
	return d
}

func (d *ackDropper) hook(role string) bool {
	h, _ := d.harp.Load().(string)
	return h != "" && role == h
}

// awaitTurnRecorded waits until harp's failed turn is RECORDED, not merely
// folded: the hold took it in and the run reads idle in the journal, so the
// crash that follows loses nothing but the acks.
func (f *holdFixture) awaitTurnRecorded(t *testing.T, harp string) {
	t.Helper()
	runID := currentRunID(f.c, harp)
	require.Eventually(t, func() bool { return f.c.runState(runID) == StateIdle }, conformanceWait, time.Millisecond,
		"%s's failed turn never read idle in the journal", harp)
}

// TestHoldRestart_AFailedTurnRecordedButNeverAckedIsNotReplayedIntoAFreshHold
// forces the crash after the worker's failed turn was wholly recorded — its
// turn start, its failure folded into the hold, its idle state — but before
// EITHER boundary's ack reached its runner. The runner re-sends both to the
// restarted coordinator. The replayed turn start is one the crashed
// coordinator saw; taken as a fresh turn it cleared the re-adopted run's
// "turn began before adoption" mark, and the replayed failure then opened a
// fresh hold over the release adoption had just made (its deadline passed
// while down), re-parking the runs that release resumed.
//
// The durable event watermark closes it: the restarted coordinator answers
// the runner's Hello with the highest seq it RECORDED, and the runner re-sends
// only what lies above it.
func TestHoldRestart_AFailedTurnRecordedButNeverAckedIsNotReplayedIntoAFreshHold(t *testing.T) {
	drop := newAckDropper()
	f, clk := newRateFixture(t, func(o *Options) { o.ackHook = drop.hook })
	drop.harp.Store(f.worker)
	f.send(t, f.worker, limitHit+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	f.awaitTurnRecorded(t, f.worker)

	reasserted := make(chan struct{}, 8)
	clk = f.restart(t, clk, limitResets.Sub(clk.Now())+time.Minute, stepSignal(holdStepReasserted, reasserted))
	_ = clk
	require.Empty(t, f.c.CredentialHolds(), "premise: the hold released itself at adoption")
	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	f.awaitReplayed(t, 0)

	assert.Empty(t, f.c.CredentialHolds(), "the recorded failure was replayed into a fresh hold")
	assert.Len(t, journaled[holdOpened](t, f.c, factHoldOpened), 1, "the recorded failure was replayed into a fresh hold")
	f.send(t, f.sibling, "after the outage")
	awaitChatText(t, f.sp, 1, "after the outage")
}

// TestHoldRestart_ARefusalRecordedButNeverAckedIsNotChargedToTheFreshCredential
// is the same crash under the replaced-credential rule: the restart
// re-authenticates (a fresh credential replaces the refused one), and the
// replayed turn start, taken as a turn on the fresh credential, cleared the
// run's "credential replaced" mark — so the replayed refusal was charged to
// the FRESH credential: a hold on it, and the human told their
// re-authentication was refused.
func TestHoldRestart_ARefusalRecordedButNeverAckedIsNotChargedToTheFreshCredential(t *testing.T) {
	drop := newAckDropper()
	f, clk := newSecretsFixture(t, func(o *Options) { o.ackHook = drop.hook })
	drop.harp.Store(f.worker)
	f.send(t, f.worker, credRefused+" do the work")
	f.awaitHold(t, f.worker, f.sibling)
	f.awaitParks(t, f.worker, f.sibling)
	f.awaitTurnRecorded(t, f.worker)

	f.opts.RefreshSecrets = (&secretsRefresher{}).refresh
	f.opts.LookupEnv = envOf(map[string]string{tokenVar: freshToken})
	reasserted := make(chan struct{}, 8)
	f.restart(t, clk, 0, stepSignal(holdStepReasserted, reasserted))
	require.Empty(t, f.c.CredentialHolds(), "premise: the re-auth released the hold at adoption")
	f.redial(t)
	within(t, reasserted, "the worker's owed resume was never delivered")
	within(t, reasserted, "the sibling's owed resume was never delivered")
	f.awaitReplayed(t, 0)

	assert.Empty(t, f.c.CredentialHolds(), "the recorded refusal was charged to the fresh credential")
	assert.Len(t, journaled[holdOpened](t, f.c, factHoldOpened), 1, "no new hold opened")
	assert.Zero(t, f.findingsWith(refusedFinding), "the human is told the fresh credential was refused: %v", f.findings.All())
}

// TestEventWatermark_ABoundaryIsNeverRecordedAboveUnflushedItems: a turn
// boundary's state fact raises the run's durable event watermark, which a
// restarted coordinator hands the runner as the seq to re-send above — so no
// event below it may be undurable. Items buffered ahead of the boundary
// (deltas group-fsync) are flushed before it is recorded; when that flush
// fails the boundary is withheld with them, the watermark does not move, and
// the runner's re-send records both.
// watermarkRun is a live, idle child and a run channel for it whose ack and
// flush cursors start at the run's durable event watermark, base; deltas
// counts the run's recorded message deltas.
type watermarkRun struct {
	c      *Coordinator
	ch     *RunChannel
	harp   string
	runID  string
	base   uint64
	deltas func() int
}

func newWatermarkRun(t *testing.T) *watermarkRun {
	t.Helper()
	resetStrictness(t)
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	base := c.eventWatermark(out.RunID)
	require.NotZero(t, base, "premise: the briefing's turn raised the watermark")
	return &watermarkRun{
		c: c, harp: out.Harp, runID: out.RunID, base: base,
		ch: &RunChannel{
			role:        out.Harp,
			id:          Identity{Harp: out.Harp, RunID: out.RunID},
			BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 64),
			completed:   make(chan struct{}),
			ackSeq:      base,
			flushedSeq:  base,
		},
		deltas: func() (n int) {
			c.items.View(func() { n = c.itemsF.countsFor(out.RunID)["message_delta"] })
			return n
		},
	}
}

func TestEventWatermark_ABoundaryIsNeverRecordedAboveUnflushedItems(t *testing.T) {
	w := newWatermarkRun(t)
	c, ch, base := w.c, w.ch, w.base
	before := w.deltas()

	delta := Event{RunID: w.runID, Seq: base + 1, Payload: MessageDelta{Text: "partial"}}
	started := Event{RunID: w.runID, Seq: base + 2, Payload: CustomEvent{Name: CustomTurnStarted}}

	restore := breakAppends(c.items)
	c.HandleEvent(ch, delta)
	c.HandleEvent(ch, started)
	restore()
	acked, _ := drainAcks(ch)
	assert.Less(t, acked, base+1, "nothing past the unflushed item may be acked")
	assert.Equal(t, base, c.eventWatermark(w.runID), "the boundary was recorded above an item that is not durable")
	assert.Equal(t, StateIdle, rosterState(c, w.harp), "the withheld boundary changed the run's state")

	// The runner's re-send, in order.
	c.HandleEvent(ch, delta)
	c.HandleEvent(ch, started)
	acked, _ = drainAcks(ch)
	assert.Equal(t, base+2, acked)
	assert.Equal(t, base+2, c.eventWatermark(w.runID))
	assert.Equal(t, StateExecuting, rosterState(c, w.harp))
	assert.Equal(t, before+1, w.deltas(), "the buffered item is recorded exactly once")
}

// The turn's END is a boundary too: items buffered ahead of a TurnIdle are
// flushed before it is recorded, and a failed flush withholds it with them.
func TestEventWatermark_ATurnsEndIsNeverRecordedAboveUnflushedItems(t *testing.T) {
	w := newWatermarkRun(t)
	c, ch, base := w.c, w.ch, w.base
	c.HandleEvent(ch, Event{RunID: w.runID, Seq: base + 1, Payload: CustomEvent{Name: CustomTurnStarted}})
	require.Equal(t, base+1, c.eventWatermark(w.runID), "premise: the turn started")
	require.Equal(t, StateExecuting, rosterState(c, w.harp))
	drainAcks(ch)
	before := w.deltas()

	delta := Event{RunID: w.runID, Seq: base + 2, Payload: MessageDelta{Text: "partial"}}
	idle := Event{RunID: w.runID, Seq: base + 3, Payload: CustomEvent{Name: CustomTurnIdle}}
	restore := breakAppends(c.items)
	c.HandleEvent(ch, delta)
	c.HandleEvent(ch, idle)
	restore()
	acked, _ := drainAcks(ch)
	assert.Less(t, acked, base+2, "nothing past the unflushed item may be acked")
	assert.Equal(t, base+1, c.eventWatermark(w.runID), "the turn's end was recorded above an item that is not durable")
	assert.Equal(t, StateExecuting, rosterState(c, w.harp), "the withheld end changed the run's state")

	c.HandleEvent(ch, delta)
	c.HandleEvent(ch, idle)
	acked, _ = drainAcks(ch)
	assert.Equal(t, base+3, acked)
	assert.Equal(t, base+3, c.eventWatermark(w.runID))
	assert.Equal(t, StateIdle, rosterState(c, w.harp))
	assert.Equal(t, before+1, w.deltas(), "the buffered item is recorded exactly once")
}
