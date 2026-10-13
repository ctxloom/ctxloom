package coord

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// heldRequest parks the first HandleRequest for reqID, once armed, at the
// handleRequestHook: an old reader holding a request frame it has not handled.
type heldRequest struct {
	reqID    string
	armed    atomic.Bool
	at, let  chan struct{}
	reached  sync.Once
	released sync.Once
}

func newHeldRequest(t *testing.T, reqID string) *heldRequest {
	h := &heldRequest{reqID: reqID, at: make(chan struct{}), let: make(chan struct{})}
	t.Cleanup(h.release)
	return h
}

func (h *heldRequest) hook(o *Options) {
	o.handleRequestHook = func(id string) {
		if id == h.reqID && h.armed.CompareAndSwap(true, false) {
			h.reached.Do(func() { close(h.at) })
			<-h.let
		}
	}
}

func (h *heldRequest) release() { h.released.Do(func() { close(h.let) }) }

// handleHeld runs HandleRequest(ch, req) on its own goroutine — the old
// reader — and returns once it is parked at the seam; done closes when it
// finishes.
func (h *heldRequest) handleHeld(t *testing.T, c *Coordinator, ch *RunChannel, req AgentRequest) (done <-chan struct{}) {
	t.Helper()
	h.armed.Store(true)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		c.HandleRequest(ch, req)
	}()
	testsupport.Within(t, 5*time.Second, func() struct{} { return <-h.at }, "the old reader never reached HandleRequest")
	return finished
}

func approvalAsk(reqID string) AgentRequest {
	return AgentRequest{RequestID: reqID, Kind: ApprovalRequest{
		Ask:         engine.PermissionAsk{Tool: "Bash"},
		Transitions: []engine.PostureTransition{{Posture: "default", Label: "default", Default: true}},
	}}
}

// queuedReplies drains what ch's send pump holds and counts the replies to
// reqID among it. No pump runs for a hand-built channel, so what was queued is
// still there.
func queuedReplies(ch *RunChannel, reqID string) int {
	n := 0
	for {
		select {
		case f := <-ch.send:
			if f.Reply != nil && f.Reply.RequestID == reqID {
				n++
			}
		default:
			return n
		}
	}
}

func (r *supersededRun) tracked(reqID string) bool {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	_, ok := r.c.reqTrack[reqKey{role: r.harp, reqID: reqID}]
	return ok
}

// TestHandleRequest_ARequestReSentOnTheSuccessorIsAnsweredOnce FORCES the
// reconnect shape for plane 2: the old reader holds a request frame it has not
// handled when a reconnect supersedes its channel; the runner re-sends the
// request, still outstanding, on the successor, which dispatches it and the
// human answers; only then does the old reader handle its copy.
//
// The request is dispatched once ((role, request_id) is deduplicated across
// channels) and ANSWERED once: the stale copy must not re-deliver the cached
// answer as a second reply frame on the successor.
func TestHandleRequest_ARequestReSentOnTheSuccessorIsAnsweredOnce(t *testing.T) {
	const reqID = "req-ask"
	held := newHeldRequest(t, reqID)
	r := newSupersededRun(t, held.hook)
	c := r.c
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)
	c.HandleEvent(r.old, r.boundary(CustomTurnStarted, r.base+1))

	oldDone := held.handleHeld(t, c, r.old, approvalAsk(reqID))
	successor := r.reattach(t)
	c.HandleRequest(successor, approvalAsk(reqID)) // the runner's reissue
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, c.Approvals().Answer(id, ApprovalDecision{Allow: false, Message: "no"}))
	testsupport.Within(t, 5*time.Second, func() struct{} {
		for {
			if f := <-successor.send; f.Reply != nil && f.Reply.RequestID == reqID {
				return struct{}{}
			}
		}
	}, "the successor never carried the answer")

	held.release() // the old reader handles its copy after the answer is cached
	testsupport.Within(t, 5*time.Second, func() struct{} { <-oldDone; return struct{}{} }, "the old reader never finished")

	assert.Zero(t, queuedReplies(successor, reqID), "the superseded reader's stale copy answered the request a second time")
	assert.Zero(t, queuedReplies(r.old, reqID))
	assert.Empty(t, c.Approvals().Pending(), "nothing parked a second time")
	for drained := false; !drained; {
		select {
		case ev := <-events:
			assert.NotEqual(t, QueueAdded, ev.Kind, "the request was dispatched twice")
		default:
			drained = true
		}
	}
}

// TestHandleRequest_AnAskASupersededReaderStillHoldsNeverParksIntoALaterTurn
// FORCES the shape deduplication cannot see: the old reader holds an approval
// request when a reconnect supersedes its channel, and the asking turn ends
// meanwhile — its engine process took the ask with it, so the runner no longer
// holds the request and re-sends only the turn's end. There is no successor
// copy for (role, request_id) to deduplicate against.
//
// A request is stamped with its run's turn as it is handled, in order with the
// run's turn ends, and one whose turn has ended never parks. The stale copy,
// handled after the successor folded the turn's end, would be stamped with
// the NEXT turn and park for the human — an ask nothing waits on, whose allow
// grants for a turn that is over. It must change nothing.
func TestHandleRequest_AnAskASupersededReaderStillHoldsNeverParksIntoALaterTurn(t *testing.T) {
	const reqID = "req-stale"
	held := newHeldRequest(t, reqID)
	r := newSupersededRun(t, held.hook)
	c := r.c
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	events := c.Approvals().Subscribe(ctx)
	c.HandleEvent(r.old, r.boundary(CustomTurnStarted, r.base+1))

	oldDone := held.handleHeld(t, c, r.old, approvalAsk(reqID))
	successor := r.reattach(t)
	c.HandleEvent(successor, r.boundary(CustomTurnIdle, r.base+2)) // the runner's re-send
	require.Equal(t, StateIdle, rosterState(c, r.harp), "premise: the successor folded the asking turn's end")

	held.release()
	testsupport.Within(t, 5*time.Second, func() struct{} { <-oldDone; return struct{}{} }, "the old reader never finished")

	if r.tracked(reqID) {
		ev := awaitEvent(t, events, QueueAdded)
		t.Fatalf("a superseded reader's stale ask was dispatched after its turn ended and parked (%s) for a turn that is over", ev.ID)
	}
	assert.Empty(t, c.Approvals().Pending())
	assert.Zero(t, queuedReplies(successor, reqID), "nothing answers a request the runner no longer holds")
}

// TestRefuseSpoolChanged_ASupersededChannelsMalformedDoorbellIsCountedOnce
// is the same shape for a doorbell the wire could not decode: the old reader
// holds it when a reconnect supersedes its channel and refuses it after.
// Nothing about a refusal is run state — it is a warning and a counter, read
// from no channel — and a doorbell is never reissued (the runner's reconnect
// re-sweeps instead), so the stale refusal is the frame's only one: it counts
// once and changes nothing else. Where in the refusal the reader is parked is
// immaterial; handling it after the takeover is the latest it can land.
func TestRefuseSpoolChanged_ASupersededChannelsMalformedDoorbellIsCountedOnce(t *testing.T) {
	r := newSupersededRun(t, func(*Options) {})
	c := r.c
	successor := r.reattach(t)
	before := c.SpoolDoorbellStats()
	watermark := c.eventWatermark(r.runID)

	c.RefuseSpoolChanged(r.old, errors.New("a ref that does not parse"))

	after := c.SpoolDoorbellStats()
	assert.Equal(t, before.Rejected+1, after.Rejected, "the malformed doorbell is refused once")
	assert.Equal(t, before.Dropped, after.Dropped)
	assert.Equal(t, StateIdle, rosterState(c, r.harp))
	assert.False(t, r.slotHeld())
	assert.Equal(t, watermark, c.eventWatermark(r.runID))
	c.mu.Lock()
	live := c.chans[r.harp]
	c.mu.Unlock()
	assert.Same(t, successor, live, "the refusal left the successor the run's channel")
}
