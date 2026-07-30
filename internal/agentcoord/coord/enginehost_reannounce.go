package coord

import (
	"context"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// THE TURN-BOUNDARY RE-ANNOUNCER (plane-2 F10).
//
// Plane 2 delivers a control instruction as an ANNOUNCEMENT plus a PULL: the
// body is parked in the runner's own recv buffer and the agent is expected to
// call agent_recv for it. §6.1 assumed a "pending-until-recv re-delivery"
// mechanism — but pending-until-recv names a STATE, not a TRIGGER, and no
// component existed to fire on it. The announcement was emitted exactly once,
// at enqueue, by whichever control verb parked the body.
//
// So an engine that took the announcement turn and never pulled left the
// human's instruction sitting in the buffer until the process died: exit 0, an
// acknowledged control request, and nothing anywhere that a caller could
// observe. That is the silent loss this file closes.
//
// This is the trigger. It fires at every ENGINE TURN BOUNDARY (adapt's
// ev.Complete arm, immediately after endTurn) and asks one question: is there
// still a parked body nobody pulled? If there is, it re-announces — and because
// a notice repeated verbatim is a notice a model learns to skip, each
// re-announcement carries the OLDEST body's age and one rung more urgency than
// the last (UnpulledReminder).
//
// It is BOUNDED, because an unbounded re-announcer is its own silent loss: it
// would occupy every turn of the run forever, which costs the caller their
// agent just as thoroughly as dropping the instruction did. After
// reannounceLimit frames go unanswered it stops — and stops LOUDLY, on the
// event plane and on stderr at both ends. A quiet give-up would be the very
// defect this file exists to close, wearing a different hat.
//
// Frames themselves are built ONLY by the generated .XmlLike() encoders; see
// internal/agentcoord/xmllike_gen.go and the gate in tests/arch.

// reannounceLimit is how many times ONE parked body may be re-announced after
// its original announcement went unanswered.
//
// Three, because of what each rung buys. The first re-announcement covers the
// ordinary miss: an engine that spent the announcement turn on something else
// and would act if told again. The second covers a turn genuinely busy with a
// long tool call. The third is labelled FINAL and says so in the frame, which
// is the last lever a text channel has. A fourth would be a fourth identical
// disappointment: an engine that has ignored an escalating notice through three
// consecutive turns of its own is not going to pull on the fourth, and every
// further frame is a turn the caller paid for and did not ask for.
const reannounceLimit = 3

// reannounceState is the re-announcer's per-run bookkeeping, guarded by eh.mu.
type reannounceState struct {
	// sent counts frames emitted per parked body id. Per-BODY rather than
	// per-run: two steers in flight each deserve their own budget, and a
	// second instruction must not inherit the first one's exhaustion.
	sent map[string]uint32
	// gaveUp remembers the bodies the limit already retired, so the
	// re-announcer moves on to the next-oldest instead of re-litigating one it
	// has already reported as lost.
	gaveUp map[string]bool
	// inFlight is true from the moment a frame is planned until its enqueue
	// returns. The enqueue blocks until the engine takes the turn, so without
	// this a second boundary arriving in that window would plan a second frame
	// for a body already being re-announced.
	inFlight bool
}

// reannouncePlan is one boundary's decision.
type reannouncePlan struct {
	messageID string
	waited    time.Duration
	urgency   agentcoordpb.UnpulledReminder_Urgency
	// emit and giveUp are mutually exclusive; both false means "nothing to do
	// at this boundary", which is the overwhelmingly common answer.
	emit   bool
	giveUp bool
}

// reannounceAtBoundary is the TRIGGER ITSELF: adapt calls it once per completed
// turn, after endTurn has marked the engine idle.
//
// It must not block — adapt is the only reader of the engine's event stream,
// and a boundary that stalls stalls the whole run — so the enqueue (which waits
// for the engine to take the turn, possibly minutes) runs on a tracked
// goroutine bounded by the RUN's context rather than any request's.
func (eh *EngineHost) reannounceAtBoundary(home engineHome) {
	plan, runCtx := eh.planReannounce(home.PendingControlPayloads())
	switch {
	case plan.giveUp:
		eh.reportUnpulled(home, plan)
	case plan.emit:
		frame := (&agentcoordpb.UnpulledReminder{
			AgeSeconds: wholeSeconds(plan.waited),
			Urgency:    plan.urgency,
		}).XmlLike()
		eh.goTracked(func() {
			defer eh.finishReannounce()
			// The error is deliberately dropped: every way this fails is the
			// run ending, and a run that is ending has no boundary left to
			// report to. The give-up path is for the case that matters —
			// a LIVE engine that will not pull.
			_ = eh.enqueueTurn(runCtx, turnTag{kind: "reannounce", reqID: plan.messageID}, frame)
		})
	}
}

// planReannounce decides one boundary under eh.mu, returning the plan and the
// run context its enqueue must ride.
//
// The HOLD-OFF (step 2) is the non-obvious rule. A non-empty tag FIFO means a
// locally-originated turn is enqueued and the engine has not started it yet —
// most often the body's own original announcement, queued while the engine was
// mid-turn. Re-announcing on top of an announcement the agent has not been
// shown would be nagging about something it was never told, and it would stack
// turns the caller never asked for.
func (eh *EngineHost) planReannounce(pending []PendingControlPayload) (reannouncePlan, context.Context) {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	if eh.reannounce.inFlight || len(eh.pendingTags) > 0 || eh.runCtx == nil {
		return reannouncePlan{}, nil
	}
	oldest, ok := eh.oldestLiveLocked(pending)
	if !ok {
		return reannouncePlan{}, nil
	}
	waited := time.Since(oldest.ParkedAt)
	sent := eh.reannounce.sent[oldest.MessageID]
	if sent >= reannounceLimit {
		if eh.reannounce.gaveUp == nil {
			eh.reannounce.gaveUp = map[string]bool{}
		}
		eh.reannounce.gaveUp[oldest.MessageID] = true
		return reannouncePlan{messageID: oldest.MessageID, waited: waited, giveUp: true}, eh.runCtx
	}
	if eh.reannounce.sent == nil {
		eh.reannounce.sent = map[string]uint32{}
	}
	sent++
	eh.reannounce.sent[oldest.MessageID] = sent
	eh.reannounce.inFlight = true
	return reannouncePlan{
		messageID: oldest.MessageID,
		waited:    waited,
		urgency:   reannounceUrgency(sent),
		emit:      true,
	}, eh.runCtx
}

// oldestLiveLocked picks the oldest pending body the limit has not already
// retired. pending arrives oldest-first from the Home ledger. Caller holds
// eh.mu.
func (eh *EngineHost) oldestLiveLocked(pending []PendingControlPayload) (PendingControlPayload, bool) {
	for _, p := range pending {
		if !eh.reannounce.gaveUp[p.MessageID] {
			return p, true
		}
	}
	return PendingControlPayload{}, false
}

// finishReannounce clears the in-flight guard once an enqueue has landed (or
// died with the run).
func (eh *EngineHost) finishReannounce() {
	eh.mu.Lock()
	eh.reannounce.inFlight = false
	eh.mu.Unlock()
}

// reportUnpulled is the terminal give-up, and the whole reason the bound is
// safe to have. It says so on the event plane (where the coordinator and
// anything watching the run's events can see it) AND on stderr at both ends.
//
// It does NOT inject anything: the point of the bound is to stop taking the
// engine's turns, and one more frame explaining that we are out of frames would
// be the same hazard with better manners.
func (eh *EngineHost) reportUnpulled(home engineHome, plan reannouncePlan) {
	secs := wholeSeconds(plan.waited)
	home.emitCustomEvent(CustomControlUnpulled, map[string]any{
		"message_id":    plan.messageID,
		"announcements": int(reannounceLimit) + 1, // the original plus every re-announcement
		"age_seconds":   int(secs),
	})
	clidiag.Warn("ctxloom", "runner: control payload %s was announced %d times over %ds and the agent never called agent_recv; "+
		"no further reminders will be injected and the instruction has NOT been acted on",
		plan.messageID, reannounceLimit+1, secs)
}

// reannounceUrgency maps the count of frames emitted for a body onto the
// escalation rung.
//
// It escalates on the COUNT rather than the elapsed time, deliberately. Age is
// already the frame's other attribute, and an engine that burns three turn
// boundaries inside one second would otherwise see three frames reading
// `age_seconds="0"` — identical strings, which is the habituation the
// escalation exists to defeat. Count guarantees every frame differs from the
// one before it; age carries the information a model actually weighs.
func reannounceUrgency(sent uint32) agentcoordpb.UnpulledReminder_Urgency {
	switch {
	case sent >= reannounceLimit:
		return agentcoordpb.UnpulledReminder_URGENCY_FINAL
	case sent <= 1:
		return agentcoordpb.UnpulledReminder_URGENCY_REPEAT
	default:
		return agentcoordpb.UnpulledReminder_URGENCY_URGENT
	}
}

// wholeSeconds renders a wait for the frame: truncated, never negative. A
// clock that stepped backwards must read as "just now", not as an enormous
// unsigned number.
func wholeSeconds(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	return uint32(d / time.Second)
}
