package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The engine host's TURN QUEUE (what asked for each locally-originated turn,
// so the turn boundary can correlate the report to the delivery that started
// it) and the pause gate every turn waits on (RunnerRequest pause/resume).

// turnOutcome is what a Turn frame waiting on a turn receives at its
// boundary: the result, or the error the turn ended with.
type turnOutcome struct {
	res engine.TurnResult
	err error
}

// turnTag attributes one locally-originated turn to what asked for it. The zero
// value means "ordinary": the briefing, or an engine continuing on its own.
type turnTag struct {
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
	// done, when non-nil, receives this turn's result at its boundary — a
	// Turn frame's caller is waiting on it. Buffered by its maker so the
	// adapt loop never blocks on a caller that went away.
	done chan turnOutcome
}

// enqueueTurn is the ONE funnel for every locally-originated turn — the
// briefing and delivered mail.
//
// It does three things that must happen together: it waits for the turn in
// flight to reach its boundary (the engine takes one turn at a time; an
// arrival mid-turn becomes the next one), it pushes tag onto the
// turn-attribution FIFO in the same order the turns start, and it records
// the user turn in the canonical transcript as the engine takes it. It
// returns once the turn's process is STARTED, not ended — the caller that
// wants the boundary waits on its tag.
//
// The enqueue lock is held ACROSS the hand-off. That serializes turns, which
// is the point: the FIFO's order is only meaningful if it is the order the
// engine receives. A wait that can never end is bounded by ctx and by the
// run's own ctx, so the lock is released when the run dies.
func (eh *EngineHost) enqueueTurn(ctx context.Context, tag turnTag, text string) error {
	eh.mu.Lock()
	started := eh.driver != nil && eh.runCtx != nil
	ended := eh.ended
	runCtx := eh.runCtx
	eh.mu.Unlock()
	if !started {
		return errors.New("engine host: no run has started, so there is no engine to hand a turn to")
	}
	if ended {
		return errors.New("engine host: the run has ended; no engine takes a turn")
	}
	// THE PAUSE GATE (spoolcontrol.go's ControlPause). A paused run takes no
	// new turn: the caller waits here, holding NOTHING — not the enqueue lock,
	// not a slot on the FIFO — so a pause cannot deadlock a resume, and the
	// mail behind this turn stays unconsumed in the spool where a relaunch
	// would find it. Bounded by the same two contexts everything else here is.
	if err := waitGate(ctx, runCtx, eh.pauseGate()); err != nil {
		return err
	}

	eh.enqueueMu.Lock()
	defer eh.enqueueMu.Unlock()
	// The turn in flight ends first: one engine process at a time.
	eh.mu.Lock()
	busy := eh.turnBusy
	eh.mu.Unlock()
	if err := waitGate(ctx, runCtx, busy); err != nil {
		return err
	}
	// THE OWNER GATE: LET IT FINISH, THEN WAIT. The turn in flight has just
	// reached its boundary; the next one starts only with the owner present,
	// so a runner whose coordinator is away runs nothing new — however the
	// turn was offered (the boundary's own spool sweep, the periodic one, a
	// queued delivery). The owner-loss clock runs while it waits here, and
	// its expiry ends runCtx (Main tears the run down).
	eh.mu.Lock()
	home := eh.home
	eh.mu.Unlock()
	if home != nil {
		if err := waitGate(ctx, runCtx, home.ownerPresent()); err != nil {
			return err
		}
	}
	return eh.startTurn(tag, text)
}

// waitGate waits for gate to close, bounded by ctx and the run's ctx; a nil
// gate is open.
func waitGate(ctx, runCtx context.Context, gate <-chan struct{}) error {
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-runCtx.Done():
		return runCtx.Err()
	}
}

// startTurn hands text to the engine as the next turn — unless the run ended
// while the caller waited — pushing tag on the attribution FIFO, marking the
// engine busy, and recording the user turn. Called under the enqueue lock.
func (eh *EngineHost) startTurn(tag turnTag, text string) error {
	eh.mu.Lock()
	if eh.ended || eh.stopping {
		eh.mu.Unlock()
		return errors.New("engine host: the run has ended; no engine takes a turn")
	}
	eh.pendingTags = append(eh.pendingTags, tag)
	next := make(chan struct{})
	eh.turnBusy = next
	// The turn's context exists before the turn does, under the same lock
	// that publishes it busy: an interrupt that arrives the instant the turn
	// is handed off still finds it.
	turnCtx, turnCancel := context.WithCancel(eh.runCtx)
	eh.turnCancel = turnCancel
	rec := eh.rec
	key := eh.nativeKey
	eh.mu.Unlock()
	transcript.RecordUserText(rec, text)
	eh.goTracked(func() {
		defer turnCancel()
		eh.runTurn(turnCtx, next, text, key)
	})
	return nil
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
		Status: coordgrpc.OKStatus(""),
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
		Status: coordgrpc.OKStatus(""),
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
	return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.PermissionDenied, fmt.Sprintf(
		"%s named run %s, but this runner hosts run %s (A9 correlation)", what, runID, eh.runID))}
}

// turnFrame answers RunnerRequest.Turn — the coordinator's turn injection to
// THIS live runner: one engine turn (a fresh engine process, resumed by the
// frame's key or the one the host learned), answered with the turn's final
// text and the key the next turn resumes by. It blocks until the turn's
// boundary.
func (eh *EngineHost) turnFrame(t *agentcoordpb.Turn) *agentcoordpb.RunnerResponse {
	eh.mu.Lock()
	started := eh.started
	if t.GetResume() != "" && eh.turnBusy == nil {
		// A parked host: the frame names the key its process resumes by.
		eh.nativeKey = t.GetResume()
	}
	eh.mu.Unlock()
	if !started {
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.FailedPrecondition, "turn: no run is driven on this runner yet")}
	}
	if t.GetPrompt() == "" {
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.InvalidArgument, "turn: a turn needs a prompt")}
	}
	done := make(chan turnOutcome, 1)
	if err := eh.enqueueTurn(eh.baseCtx, turnTag{done: done}, t.GetPrompt()); err != nil {
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Unavailable, "turn: "+err.Error())}
	}
	select {
	case out := <-done:
		if out.err != nil {
			// The engine's own account of the failed turn answers the frame,
			// so the coordinator's caller renders WHY — the run ends after.
			return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Aborted, "turn: "+out.err.Error())}
		}
		res := out.res
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.RunnerResponse_Turn{Turn: &agentcoordpb.TurnResult{NativeKey: res.NativeKey, Answer: res.Answer}}}
	case <-eh.baseCtx.Done():
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Canceled, "turn: the runner is shutting down")}
	}
}

// interruptedTurnNote heads an interrupted turn's automatic report, so the
// parent reads "cut short" — never the no-output error an empty report is.
const interruptedTurnNote = "[this turn was interrupted before it finished]"

// interruptTurn interrupts the turn in flight, if any, and returns its
// boundary (nil before the first turn; already closed when parked).
func (eh *EngineHost) interruptTurn() <-chan struct{} {
	eh.mu.Lock()
	cancel := eh.turnCancel
	busy := eh.turnBusy
	eh.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return busy
}

// interruptRun answers InterruptRun: the turn in flight is cut short and
// parks at its boundary; the run lives. With no turn in flight it does
// nothing. Answered at once — the turn's end is reported by its own idle.
func (eh *EngineHost) interruptRun(req *agentcoordpb.InterruptRun) *agentcoordpb.RunnerResponse {
	if resp := eh.checkRunID(req.GetRunId(), "InterruptRun"); resp != nil {
		return resp
	}
	eh.interruptTurn()
	return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus("")}
}

// errNoApprovalRoute refuses grants for a run whose approver is not the
// human: nothing on this run could have granted them.
var errNoApprovalRoute = errors.New("set grants: this run routes no approvals, so it holds no session grants")

// setGrants answers SetGrants: the run's session grants become the
// coordinator's set, from the next turn on. Answered by status alone.
func (eh *EngineHost) setGrants(req *agentcoordpb.SetGrants) *agentcoordpb.RunnerResponse {
	if resp := eh.checkRunID(req.GetRunId(), "SetGrants"); resp != nil {
		return resp
	}
	eh.mu.Lock()
	appr := eh.approvals
	eh.mu.Unlock()
	switch {
	case appr != nil:
		appr.setGrants(req.GetRules())
	case len(req.GetRules()) > 0:
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.FailedPrecondition, errNoApprovalRoute.Error())}
	}
	return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus("")}
}

// stopRun answers StopRun: INTERRUPT-THEN-CLOSE. Nothing new starts, the turn
// in flight is interrupted and given the request's grace to reach its
// boundary (its report and idle written), and the run is then closed — its
// terminal flows from the run's cancellation. Answered once closed.
func (eh *EngineHost) stopRun(req *agentcoordpb.StopRun) *agentcoordpb.RunnerResponse {
	if resp := eh.checkRunID(req.GetRunId(), "StopRun"); resp != nil {
		return resp
	}
	eh.mu.Lock()
	eh.stopping = true
	eh.mu.Unlock()
	if busy := eh.interruptTurn(); busy != nil {
		grace := time.NewTimer(req.GetGrace().AsDuration())
		defer grace.Stop()
		select {
		case <-busy:
		case <-grace.C:
		case <-eh.baseCtx.Done():
		}
	}
	eh.closeRun()
	return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.RunnerResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{}}}
}

// closeRun cancels the run's context: the turn in flight ends with it and the
// run's terminal is reported (Drive's watcher, or the turn's own end).
func (eh *EngineHost) closeRun() {
	eh.mu.Lock()
	cancel := eh.cancel
	eh.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
