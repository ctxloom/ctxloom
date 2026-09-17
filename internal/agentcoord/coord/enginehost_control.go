package coord

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// The engine host's TURN QUEUE (what asked for each locally-originated turn,
// so the turn boundary can correlate the report to the delivery that started
// it) and the pause gate every turn waits on (RunnerRequest pause/resume).

// turnTag attributes one locally-originated turn to what asked for it. The zero
// value means "ordinary": the briefing, or coordinator mail.
type turnTag struct {
	// "" (briefing/mail) | "steer" | "question" | "summarize" | "reannounce"
	kind string
	// The CoordinatorRequest's correlating id, when kind != "". "reannounce"
	// is the exception: nothing correlates a re-announcement to an open
	// request (the ack went back turns ago), so it carries the PARKED BODY's
	// message id — which is what the re-announcer's budget is keyed on and
	// what its give-up event names.
	reqID string
	// mail is the id of the DELIVERED MESSAGE that started this turn, when one
	// did. It rides the attribution FIFO rather than a field of its own
	// because that FIFO already answers exactly this question — which turn
	// belongs to which enqueue — and a second mechanism would have to be kept
	// in step with it.
	//
	// It becomes the automatic turn report's in_reply_to
	// (spoolturnresult.go): the correlation a parent needs to tell which of
	// its outstanding asks an answer answers. Empty for a turn nothing
	// delivered started — the briefing, or an engine continuing on its own.
	mail string
}

// enqueueTurn is the ONE funnel onto eh.in for every locally-originated turn —
// the briefing's successor sends, coordinator mail, and each control verb.
//
// It does three things that must happen together: it waits for the briefing to
// have gone first (a turn that overtakes the briefing makes the
// child's first turn something other than its task), it pushes tag onto the
// turn-attribution FIFO in the same order the sends land, and it records the
// user turn in the canonical transcript once the engine has actually taken it.
//
// The lock is held ACROSS the send. That serializes turn enqueues, which is the
// point: the FIFO's order is only meaningful if it is the order the engine
// receives. A send that can never complete is bounded by ctx and by the run's
// own ctx, so the lock is released when the run dies.
func (eh *EngineHost) enqueueTurn(ctx context.Context, tag turnTag, text string) error {
	eh.mu.Lock()
	in := eh.in
	rec := eh.rec
	runCtx := eh.runCtx
	briefed := eh.briefed
	eh.mu.Unlock()
	if in == nil || runCtx == nil {
		return errors.New("engine host: no run has started, so there is no turn stream to enqueue onto")
	}
	select {
	case <-briefed:
	case <-ctx.Done():
		return ctx.Err()
	case <-runCtx.Done():
		return runCtx.Err()
	}
	// THE PAUSE GATE (spoolcontrol.go's ControlPause). A paused run takes no
	// new turn: the caller waits here, holding NOTHING — not the enqueue lock,
	// not a slot on the FIFO — so a pause cannot deadlock a resume, and the
	// mail behind this turn stays unconsumed in the spool where a relaunch
	// would find it. Bounded by the same two contexts everything else here is.
	if gate := eh.pauseGate(); gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}

	eh.enqueueMu.Lock()
	defer eh.enqueueMu.Unlock()
	eh.mu.Lock()
	eh.pendingTags = append(eh.pendingTags, tag)
	eh.mu.Unlock()

	select {
	case in <- agent.ChatMessage{Text: text}:
		transcript.RecordUserText(rec, text)
		return nil
	case <-ctx.Done():
		eh.dropQueuedTag()
		return ctx.Err()
	case <-runCtx.Done():
		eh.dropQueuedTag()
		return runCtx.Err()
	}
}

// dropQueuedTag removes the tag this call pushed when its send never landed.
// Safe because enqueueMu makes this goroutine the only pusher, so the tail is
// ours; adapt only ever pops the head, and it pops only after a send completed.
func (eh *EngineHost) dropQueuedTag() {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	if n := len(eh.pendingTags); n > 0 {
		eh.pendingTags = eh.pendingTags[:n-1]
	}
}

// beginTurn pops the FIFO at a turn start and marks the engine busy.
func (eh *EngineHost) beginTurn() {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	eh.inTurn = true
	if len(eh.pendingTags) > 0 {
		eh.currentTag = eh.pendingTags[0]
		eh.pendingTags = eh.pendingTags[1:]
		return
	}
	// A turn nothing local enqueued (an engine that continues on its own).
	// Untagged, never mis-attributed to a waiting control verb.
	eh.currentTag = turnTag{}
}

// endTurn marks the engine idle at a turn boundary and RETURNS the tag it
// cleared — which is the only moment the just-ended turn's attribution is
// still available. The automatic turn report reads its correlation from it, so
// a caller that cleared the tag first would have to re-derive what it had just
// thrown away.
func (eh *EngineHost) endTurn() turnTag {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	eh.inTurn = false
	tag := eh.currentTag
	eh.currentTag = turnTag{}
	return tag
}

// pauseGate returns the channel a turn must wait on, or nil when the run is
// not paused. Nil rather than a closed channel: an unpaused run must not even
// select, so the gate costs one nil check on the path every turn takes.
func (eh *EngineHost) pauseGate() chan struct{} {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	return eh.paused
}

// pauseRun holds this run's turn hand-off. It is IDEMPOTENT, and says which
// it did: a second pause is not an error (the caller wanted the run paused,
// and it is), but reporting "newly paused" for it would let a supervisor
// believe it caught a running agent when it caught a stopped one.
func (eh *EngineHost) pauseRun(req *agentcoordpb.PauseRun) *agentcoordpb.RunnerResponse {
	if resp := eh.checkRunID(req.GetRunId(), "PauseRun"); resp != nil {
		return resp
	}
	eh.mu.Lock()
	newly := eh.paused == nil
	if newly {
		eh.paused = make(chan struct{})
	}
	eh.mu.Unlock()
	return &agentcoordpb.RunnerResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.RunnerResponse_PauseRun{PauseRun: &agentcoordpb.PauseRunResult{NewlyPaused: newly}},
	}
}

// resumeRun releases a paused run. Idempotent on the same terms as pauseRun.
//
// The gate is CLOSED, never sent to, so every waiter is released by the one
// operation — a resume that had to wake waiters one at a time would deliver
// its turns in an order decided by scheduling.
func (eh *EngineHost) resumeRun(req *agentcoordpb.ResumeRun) *agentcoordpb.RunnerResponse {
	if resp := eh.checkRunID(req.GetRunId(), "ResumeRun"); resp != nil {
		return resp
	}
	eh.mu.Lock()
	gate := eh.paused
	eh.paused = nil
	eh.mu.Unlock()
	if gate != nil {
		close(gate)
	}
	return &agentcoordpb.RunnerResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.RunnerResponse_ResumeRun{ResumeRun: &agentcoordpb.ResumeRunResult{NewlyResumed: gate != nil}},
	}
}

// checkRunID enforces the A9 correlation every runner request carries: this
// process hosts exactly ONE run, and a request naming another is refused
// rather than applied to the run that happens to be here. Returns nil when the
// request may proceed.
func (eh *EngineHost) checkRunID(runID, what string) *agentcoordpb.RunnerResponse {
	if runID == eh.runID {
		return nil
	}
	return &agentcoordpb.RunnerResponse{Status: statusErr(codes.PermissionDenied, fmt.Sprintf(
		"%s named run %s, but this runner hosts run %s (A9 correlation)", what, runID, eh.runID))}
}
