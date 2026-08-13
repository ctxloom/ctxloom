package coord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// The EXECUTOR half of plane 2's down direction: the engine host runs one
// coordinator-initiated control request against the hosted engine.
//
// DELIVERY IS REMINDER + PULL. The injected turn carries no untrusted bytes: it
// is a reminder frame emitted by a generated .XmlLike() encoder
// (SteerPendingReminder, for a steer) and nothing else. That encoder is the ONE
// place a frame is constructed — see internal/agentcoord/xmllike_gen.go, and
// the gate in tests/arch that keeps it the only place; this file must not name
// the frame's literal tag, let alone assemble one. The instruction
// body is parked in the runner's own recv buffer and the agent PULLS it with
// agent_recv, in the same turn (tool results arrive mid-turn, so this is one
// turn, not two). That keeps the frame unforgeable by construction — a body
// containing something that looks like a header is never interpolated into a
// turn — and it keeps ONE copy of the body, in one place, with one lifecycle.
//
// AND IF THE AGENT DOES NOT PULL? Then nothing here notices: the announcement
// below is emitted once, at enqueue, and this file's whole job is done the
// moment it lands. What makes the un-pulled case survivable lives in
// enginehost_reannounce.go — the turn-boundary re-announcer that re-announces a
// body still sitting unread, and reports it when it gives up. Without it, an
// engine that ignored this one frame lost the instruction silently.

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

// HandleControl executes ONE coordinator-initiated control request against the
// hosted engine. Registered on Home by BindHome.
//
// It refuses everything before startRun: there is no turn stream to enqueue
// onto, and answering FAILED_PRECONDITION is honest where silently succeeding
// would not be.
func (eh *EngineHost) HandleControl(ctx context.Context, req *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
	eh.mu.Lock()
	started := eh.started
	home := eh.home
	eh.mu.Unlock()
	if !started || home == nil {
		return &agentcoordpb.AgentResponse{Status: statusErr(codes.FailedPrecondition,
			"no run has started on this runner yet, so there is nothing to control")}
	}
	switch kind := req.GetKind().(type) {
	case *agentcoordpb.CoordinatorRequest_Steer:
		return eh.handleSteer(ctx, home, req.GetRequestId(), kind.Steer)
	default:
		// Home's backstop already refused every kind this runner did not
		// advertise, so reaching here means an advertised kind with no arm —
		// a wiring bug, reported as one.
		return &agentcoordpb.AgentResponse{Status: statusErr(codes.Unimplemented,
			"this engine host has no executor for that control request kind")}
	}
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

// steerBodyID derives the parked body's message id from the request id, so a
// reissued request cannot park a second copy: ParkControlPayload dedupes on
// message id exactly as the mailbox path does.
func steerBodyID(reqID string) string { return "steer-" + reqID }

// handleSteer executes a steer: park the body, then inject the reminder.
//
// The ORDER is load-bearing. The body is parked FIRST so that an agent which
// calls agent_recv immediately on seeing the reminder finds it there; injecting
// the reminder first opens a window where the pull returns nothing and the
// instruction reads as a phantom.
//
// `applied` is decided at ENQUEUE time by whether the engine was mid-turn:
// idle means the reminder starts a new turn NOW (APPLIED_IMMEDIATE — latency,
// never interruption: no engine surface ctxloom drives supports genuine
// mid-turn injection), busy means the driver queues it to the next boundary
// (APPLIED_NEXT_TURN).
func (eh *EngineHost) handleSteer(ctx context.Context, home engineHome, reqID string, steer *agentcoordpb.SteerRequest) *agentcoordpb.AgentResponse {
	text := steer.GetText()
	if text == "" {
		// Empty input must fail, not succeed having written nothing.
		return &agentcoordpb.AgentResponse{Status: statusErr(codes.InvalidArgument, "steer: text is empty")}
	}
	from := steerSender(steer.GetMetadata())

	structured, err := structpb.NewStruct(map[string]any{
		"request_id": reqID,
		"control":    "steer",
	})
	if err != nil {
		return &agentcoordpb.AgentResponse{Status: statusErr(codes.Internal, fmt.Sprintf("steer: build payload: %v", err))}
	}
	home.ParkControlPayload(&agentcoordpb.PeerMessage{
		MessageId:   steerBodyID(reqID),
		FromAgentId: from,
		Text:        text,
		Structured:  structured,
		Kind:        agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	})

	eh.mu.Lock()
	busy := eh.inTurn
	runCtx := eh.runCtx
	eh.mu.Unlock()

	// The hand-off runs on its own goroutine, bounded by the RUN's context
	// rather than this request's: the caller's 60-second budget must not be
	// able to abandon a reminder that is already committed and queued. A
	// mid-turn hand-off blocks until the boundary, which can be minutes away —
	// answering only then would burn the whole budget on an outcome already
	// known.
	handed := make(chan error, 1)
	eh.goTracked(func() {
		handed <- eh.enqueueTurn(runCtx, turnTag{kind: "steer", reqID: reqID}, (&agentcoordpb.SteerPendingReminder{}).XmlLike())
	})

	if busy {
		// The engine is mid-turn. No surface ctxloom drives takes a concurrent
		// prompt, so the driver queues the reminder to the next boundary — and
		// the acknowledgement SAYS SO rather than implying it landed.
		return steerAck(agentcoordpb.SteerResult_APPLIED_NEXT_TURN)
	}
	// The engine is idle, so the hand-off is a send onto a channel the driver
	// is already waiting on: it completes now or the engine was not as idle as
	// it looked. `applied` therefore reports what was OBSERVED, never a guess.
	select {
	case err := <-handed:
		if err != nil {
			// The reminder never landed — the run is ending or has stopped
			// taking turns. REJECTED, not a queue that does not exist.
			return &agentcoordpb.AgentResponse{
				Status: statusErr(codes.FailedPrecondition, fmt.Sprintf("steer: the engine took no turn: %v", err)),
				Kind: &agentcoordpb.AgentResponse_Steer{Steer: &agentcoordpb.SteerResult{
					Applied: agentcoordpb.SteerResult_APPLIED_REJECTED,
				}},
			}
		}
		return steerAck(agentcoordpb.SteerResult_APPLIED_IMMEDIATE)
	case <-time.After(steerHandoffWindow):
		// It became busy under us. The reminder is still queued and still
		// lands; the honest report is the boundary, not the immediacy.
		return steerAck(agentcoordpb.SteerResult_APPLIED_NEXT_TURN)
	case <-ctx.Done():
		return steerAck(agentcoordpb.SteerResult_APPLIED_NEXT_TURN)
	}
}

// steerHandoffWindow bounds how long an IDLE target's hand-off is watched
// before the answer degrades to "queued for the next boundary". It is short on
// purpose: handing a turn to a driver that is already blocked on its input
// channel is a channel send, so anything slower means the engine started a turn
// between the check and the send — which is exactly what NEXT_TURN describes.
const steerHandoffWindow = 2 * time.Second

func steerAck(applied agentcoordpb.SteerResult_Applied) *agentcoordpb.AgentResponse {
	return &agentcoordpb.AgentResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.AgentResponse_Steer{Steer: &agentcoordpb.SteerResult{Applied: applied}},
	}
}

// steerSender reads the initiator identity the coordinator attached to the
// request. It is metadata the COORDINATOR wrote, never something the steering
// party self-asserted — the initiator is derived from the authenticated call,
// not carried in a payload.
func steerSender(meta *structpb.Struct) string {
	if meta == nil {
		return UserSender
	}
	if v, ok := meta.GetFields()["from"]; ok && v.GetStringValue() != "" {
		return v.GetStringValue()
	}
	return UserSender
}
