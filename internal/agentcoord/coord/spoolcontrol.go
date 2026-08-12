package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// THE INTERACTION-PLANE CUTOVER (config delegation.spool_delivery, same flag
// and same predicate as the mail plane): steer and the correlated asks stop
// being wire requests and become FILES.
//
// What changes, and why each is a consequence of the file being the message:
//
//   - A STEER IS DURABLE AND WITHDRAWABLE. Today the instruction body rides a
//     CoordinatorRequest, is parked in the runner's recv buffer and pulled by
//     the agent, and dies with the runner process; the re-announcer exists
//     because nothing else can see that it was never read. As a file it is
//     ordinary mail with a reserved kind: it survives a relaunch, it is
//     delivered as the next turn like any other message, an unread one is
//     VISIBLE (a file still sitting in in/), and it can be RETRACTED before it
//     is taken — the rename into in/withdrawn/ either wins or loses to the
//     reader, and the filesystem is the arbiter. Stale-but-consumed is the
//     accepted cost; withdrawal is the remedy.
//
//   - AN ASK IS COOPERATIVE. A question or a summarize request is a file the
//     child answers with a file, correlated by in_reply_to. The answer is what
//     the child CHOSE to send: nothing here captures a turn's output and calls
//     it the answer, so an ask cannot be resolved by something the child never
//     addressed to it. An IDLE child answers within one delivery — its runner
//     prompts it with the ask as a new turn — and a BUSY one at its next
//     boundary.
//
//   - CORRELATION IS REGISTERED BEFORE THE ASK IS PUBLISHED. The waiter goes
//     into the table while the file does not yet exist, so a reply that lands
//     the instant the file becomes observable still resolves it. Registering
//     afterwards is the pulpy-whiff defect exactly: the answer arrives, finds
//     no waiter, degrades to ordinary mail, and the asker sits out its whole
//     budget before reporting a timeout that never happened.
//
// FLAG OFF: nothing below runs. Steer takes the plane-2 request path (or the
// mailbox fallback) exactly as before, and the asks — which were never built
// on plane 2 (HandleControl has only a steer arm) — refuse rather than
// pretending a path exists.

// ErrSteerAlreadyDelivered answers a withdrawal that lost its race: the target
// already took the instruction, so there is nothing left to retract.
//
// It is typed because the two outcomes demand opposite reactions from whoever
// asked to withdraw. "Retracted" means the instruction never happened;
// "already delivered" means it did, and the caller's next move is a follow-up
// steer, not a retry. Reporting the second as a failure — or, worse, as a
// success — is how a human comes to believe an instruction was pulled back
// while the agent is acting on it.
var ErrSteerAlreadyDelivered = errors.New("steer: the target already took this instruction, so it cannot be withdrawn")

// ErrNoSuchSteer answers a withdrawal naming an instruction this target's
// spool has never held. Distinct from ErrSteerAlreadyDelivered on purpose: one
// says "too late", the other says "never existed", and collapsing them would
// let a typo read as a delivery.
var ErrNoSuchSteer = errors.New("steer: no instruction with that id is queued for this target")

// ErrAskUnavailable refuses a question/summarize ask against a target that
// cannot answer one: not a child of this coordinator's cutover, or a run with
// no spool. Typed so a caller can tell "this target cannot be asked" from "the
// ask was asked and went unanswered", which are different facts about the
// world.
var ErrAskUnavailable = errors.New("ask: this target does not take correlated asks")

// ErrAskTimeout answers an ask whose budget elapsed with no reply.
//
// A timed-out ask is NOT a failed one: the request file is still in the
// target's spool (or already consumed, and being worked on). The distinction
// matters because the honest report to a human is "it has not answered yet",
// never "the question failed to send".
var ErrAskTimeout = errors.New("ask: the target did not answer within the budget")

// askBudget bounds one correlated ask when the caller's context carries no
// deadline. It matches controlRequestBudget rather than inventing a second
// number: an ask is a foreground control action with a caller waiting on it,
// and the substrate underneath it changed, not the human's patience.
const askBudget = controlRequestBudget

// ---- steer -------------------------------------------------------------

// steerViaSpool is the cutover's steer route: the instruction becomes ONE
// durable file in the target's in/ spool, with the reserved `steer` kind.
//
// It goes through queueMailPayload rather than writing the file itself, and
// that is the point: the empty-message and no-recipient refusals, the audit,
// the cutover branch and the spool write are all one chokepoint's, so a steer
// cannot arrive by a discipline ordinary mail does not have. What it adds is
// the kind (which renders into the delivered turn's provenance header, so the
// agent sees an instruction rather than an anonymous message) and the returned
// id, which is the withdraw handle.
// It shares steerAsMail with §5.6's fallback because the delivery question is
// the same one — the child still has to be WOKEN, an ended session resumed and
// an idle one driven — and only what the caller does with the answer differs:
// this route hands the id back as the withdraw handle, because here the
// message is a file that can still be retracted.
func (c *Coordinator) steerViaSpool(sender, harp, text string) (SteerOutcome, error) {
	msgID, outcome, err := c.steerAsMail(sender, harp, KindSteer, text)
	if err != nil {
		return SteerOutcome{}, err
	}
	outcome.MessageID = msgID
	return outcome, nil
}

// WithdrawSteer retracts an instruction the target has not yet taken.
//
// The race is resolved by the filesystem and reported HONESTLY, which is the
// whole reason this exists: rename-won means the child never saw it;
// ErrSteerAlreadyDelivered means it did, and no amount of retrying changes
// that. There is no TTL and no expiry sweep — an unread steer is a file
// sitting in in/ where anyone can see it, and this is the operation that
// removes it.
//
// by runs the same ownership guards a steer does: the ability to retract an
// instruction is the ability to control the run, not a lesser privilege.
func (c *Coordinator) WithdrawSteer(by ControlInitiator, harp, messageID string) error {
	if _, err := c.controlTarget(by, harp); err != nil {
		return err
	}
	if messageID == "" {
		return errors.New("steer withdraw: a message id is required (it is what ControlSteer returned)")
	}
	if !c.spoolDeliverTo(harp) {
		// Nothing to withdraw FROM: on the plane-2 route the body rides the
		// request and is parked in a runner's memory, which is precisely the
		// state the durable steer replaced. Refusing is honest; pretending to
		// retract is not.
		return fmt.Errorf("steer withdraw: %q does not take durable steers, so there is nothing to retract "+
			"(withdrawal exists because the instruction is a file; on the request route the body is not one): %w", harp, ErrNoSuchSteer)
	}

	mapper := spool.NewHomeMapper()
	ref, found := c.findSpoolMessage(harp, spool.DirIn, messageID)
	if !found {
		// Not in in/. Either it was consumed (the child took it) or it never
		// existed — two different answers, and the consumed/ directory is the
		// audit trail that tells them apart.
		if _, taken := c.findSpoolMessage(harp, spool.DirInConsumed, messageID); taken {
			c.audit("agent_steer_withdraw", by.auditName(), map[string]string{
				"harp": harp, "message_id": messageID, "outcome": "already_delivered",
			})
			return fmt.Errorf("%w (%s)", ErrSteerAlreadyDelivered, messageID)
		}
		return fmt.Errorf("%w: %s", ErrNoSuchSteer, messageID)
	}
	withdrawn, err := spool.Withdraw(mapper, ref)
	if err != nil {
		if errors.Is(err, spool.ErrAlreadyGone) {
			// The reader won between the scan and the rename. Same answer as
			// finding it in consumed/, because it is the same fact.
			c.audit("agent_steer_withdraw", by.auditName(), map[string]string{
				"harp": harp, "message_id": messageID, "outcome": "already_delivered",
			})
			return fmt.Errorf("%w (%s)", ErrSteerAlreadyDelivered, messageID)
		}
		return fmt.Errorf("steer withdraw: retracting %s: %w", ref, err)
	}
	c.audit("agent_steer_withdraw", by.auditName(), map[string]string{
		"harp": harp, "message_id": messageID, "outcome": "withdrawn", "ref": withdrawn.String(),
	})
	// Ring the transition. The runner needs no action — the file is already
	// out of the directory it sweeps — but the doorbell is what makes the
	// state change observable on the wire rather than only on disk, and it
	// costs nothing to be dropped.
	if err := c.RingSpool(harp, withdrawn); err != nil {
		clidiag.Warn("ctxloom", "coordinator: withdrew %s but could not announce it: %v", withdrawn, err)
	}
	return nil
}

// findSpoolMessage locates the file in harp's dir carrying messageID — its
// producer-minted origin_id, or its own filename stem when it has none.
//
// It scans rather than deriving a path because the id a caller holds is the
// message's identity, not the file's name: the coordinator mints the id before
// the file exists (that ordering is what makes correlation safe), so only the
// contents can answer which file it became.
func (c *Coordinator) findSpoolMessage(harp string, dir spool.Dir, messageID string) (spool.Ref, bool) {
	res, ok := c.sweepSpoolDir(harp, dir, "finding a message by id")
	if !ok {
		return spool.Ref{}, false
	}
	for _, e := range res.Entries {
		if spoolMessageID(e) == messageID {
			return e.Ref, true
		}
	}
	return spool.Ref{}, false
}

// spoolMessageID is the identity one swept entry answers to — the same rule
// mailFromSpool applies, kept in one place so a lookup and a delivery can
// never disagree about what a message is called.
func spoolMessageID(e spool.Entry) string {
	if e.Message != nil && e.Message.OriginID != "" {
		return e.Message.OriginID
	}
	return e.Name.Stem()
}

// ---- correlated asks (question / summarize) -----------------------------

// AskAnswer is one cooperative reply to a question or summarize ask: what the
// child chose to send back, and the id that correlated it.
type AskAnswer struct {
	// AskID is the correlation the reply quoted (in_reply_to).
	AskID string
	// From is the harp that answered — resolved from the spool the reply was
	// found in, never from anything the reply claimed.
	From string
	// Text is the reply body.
	Text string
	// Structured is the reply's JSON companion, when it sent one.
	Structured json.RawMessage
}

// pendingAsk is one outstanding correlated ask, parked on the id the request
// file carries as its origin_id.
type pendingAsk struct {
	// targetHarp is the ONLY identity allowed to answer — defense in depth
	// beyond "the id exists", exactly as pendingApproval's is: a sibling that
	// guessed an in_reply_to must not be able to answer someone else's ask.
	targetHarp string
	kind       string
	ch         chan AskAnswer // buffered(1)
}

// ControlQuestion asks a running target a question and waits for its answer.
//
// The ask is a file; the answer is the child's own agent_send quoting it. That
// makes the answer COOPERATIVE by construction — there is no path here that
// captures the child's turn output and reports it as a reply — and it makes
// the request durable: a child that is relaunched before it answers finds the
// question still in its spool.
func (c *Coordinator) ControlQuestion(ctx context.Context, by ControlInitiator, harp, text string) (AskAnswer, error) {
	return c.controlAsk(ctx, by, harp, KindQuestion, text)
}

// ControlSummarize asks a running target for an on-demand summary. Same
// mechanism as ControlQuestion, different kind: what separates them is what
// the child is being asked for, and the kind is what tells it.
func (c *Coordinator) ControlSummarize(ctx context.Context, by ControlInitiator, harp, focus string) (AskAnswer, error) {
	return c.controlAsk(ctx, by, harp, KindSummarize, focus)
}

// controlAsk runs one correlated ask to completion.
//
// ORDER IS THE CONTRACT: mint the id, register the waiter, THEN publish the
// file. A reply can only arrive after the file is observable, which is
// strictly after registration — so there is no window in which an answer
// arrives to a table that does not know about it (pulpy-whiff).
func (c *Coordinator) controlAsk(ctx context.Context, by ControlInitiator, harp, kind, text string) (AskAnswer, error) {
	if _, err := c.controlTarget(by, harp); err != nil {
		return AskAnswer{}, err
	}
	if text == "" {
		// Empty input must fail rather than deliver a message with nothing in
		// it and wait out a budget for an answer to a question nobody asked.
		return AskAnswer{}, fmt.Errorf("%s: text is required", kind)
	}
	if !c.spoolDeliverTo(harp) {
		return AskAnswer{}, fmt.Errorf("%w: %q has no correlated-ask path "+
			"(asks ride the spool, and this run is not delivered by it)", ErrAskUnavailable, harp)
	}
	c.audit("agent_"+kind, by.auditName(), map[string]string{"harp": harp})

	// REGISTER BEFORE PUBLISHING. The id is minted here and the waiter is in
	// the table while the file still does not exist.
	askID := newMessageID()
	pa := &pendingAsk{targetHarp: harp, kind: kind, ch: make(chan AskAnswer, 1)}
	c.mu.Lock()
	if c.asks == nil {
		c.asks = make(map[string]*pendingAsk)
	}
	c.asks[askID] = pa
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.asks[askID] == pa {
			delete(c.asks, askID)
		}
		c.mu.Unlock()
	}()

	if hook := c.onAskPublished; hook != nil {
		// THE ORDERING SEAM, fired between the registration above and the
		// publish below — the one instant that distinguishes this ordering
		// from every wrong one.
		//
		// It is deliberately NOT fired after the publish, which is where the
		// analogous approval seam sits: a hook on that side cannot tell a
		// correct implementation from one that registers in the gap between
		// the write and the hook, because both have registered by the time it
		// runs. Fired HERE, "the waiter is already in the table" is exactly
		// what a test can assert, and any implementation that registers later
		// fails it.
		hook(askID)
	}
	if _, _, err := c.queueMailPayloadID(askID, by.auditName(), harp, kind, text, nil, ""); err != nil {
		return AskAnswer{}, fmt.Errorf("%s %s: %w", kind, harp, err)
	}
	// The child must be woken for an idle or ended run, or the ask sits in a
	// spool nothing is reading — the same delivery-by-state wake ordinary mail
	// gets. THIS is what bounds an idle child's answer to one delivery rather
	// than to whenever it next happens to run.
	c.driveQueued(harp)

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, askBudget)
		defer cancel()
	}
	select {
	case ans := <-pa.ch:
		return ans, nil
	case <-ctx.Done():
		return AskAnswer{}, fmt.Errorf("%w: %s %s (%s) — the request is still in its spool, so an answer may still arrive and will be dropped",
			ErrAskTimeout, kind, harp, askID)
	case <-c.baseCtx.Done():
		return AskAnswer{}, errors.New("coordinator is shutting down")
	}
}

// resolveAskReply checks whether inReplyTo names an outstanding ask this
// caller may answer, and hands the reply to the waiter if so.
//
// matched=false falls through to ordinary mail delivery, which is what makes a
// stale, duplicate or foreign in_reply_to degrade gracefully instead of
// failing the send — the same discipline resolveApprovalReply follows, for the
// same reason: the correlation is a courtesy the sender extends, not a claim
// the coordinator has to honour.
//
// A reply arriving after its waiter is gone (the budget elapsed) is consumed
// and dropped rather than mailed onward: the asker asked, the asker left, and
// turning a late answer into unsolicited mail to the parent would make every
// timed-out ask produce a message nobody can place.
func (c *Coordinator) resolveAskReply(caller Identity, inReplyTo, body string, structured json.RawMessage) (disposition string, matched bool) {
	c.mu.Lock()
	pa := c.asks[inReplyTo]
	if pa == nil || pa.targetHarp != caller.Harp {
		c.mu.Unlock()
		return "", false
	}
	delete(c.asks, inReplyTo)
	c.mu.Unlock()

	c.audit("ask_reply", caller.Harp, map[string]string{"in_reply_to": inReplyTo, "kind": pa.kind})
	select {
	case pa.ch <- AskAnswer{AskID: inReplyTo, From: caller.Harp, Text: body, Structured: structured}:
	default: // the asker already gave up; the answer has nowhere to land
	}
	return fmt.Sprintf("answered the coordinator's %s (%s)", pa.kind, inReplyTo), true
}

// ---- pause / resume ------------------------------------------------------

// ControlPause holds a running target's turn delivery: nothing new is handed
// to its engine until ControlResume, and mail that arrives meanwhile stays in
// its spool where a resumed run will find it.
//
// Pause is NOT a delivery, which is why it does not ride the spool at all: an
// instruction that takes effect "when you next look at your mailbox" is not a
// pause. It is a RunnerRequest — beside StartRun, StopRun, KillRun and Drain —
// answered synchronously by the runner process that owns the engine.
func (c *Coordinator) ControlPause(ctx context.Context, by ControlInitiator, harp, reason string) error {
	return c.runnerControl(ctx, by, harp, "pause", reason)
}

// ControlResume releases a paused target: turns held at the gate are handed to
// the engine in arrival order.
func (c *Coordinator) ControlResume(ctx context.Context, by ControlInitiator, harp string) error {
	return c.runnerControl(ctx, by, harp, "resume", "")
}

// runnerControl issues one pause/resume against harp's runner and reports what
// the runner said.
//
// It runs the SAME ownership guards every control verb runs, and the same
// cutover predicate the delivery planes use — not because pause needs a spool,
// but because a run split across the two worlds is the one state nothing
// reconciles: the predicate is what says "this run is on the new plane", and
// every control surface has to agree about that or the answer depends on which
// one you asked.
func (c *Coordinator) runnerControl(ctx context.Context, by ControlInitiator, harp, verb, reason string) error {
	rec, err := c.controlTarget(by, harp)
	if err != nil {
		return err
	}
	if !c.spoolDeliverTo(harp) {
		return fmt.Errorf("%s: %q is not on the runner-request control plane "+
			"(pause and resume are RunnerChannel requests under delegation.spool_delivery; this run predates it): %w",
			verb, harp, ErrCapabilityUnavailable)
	}
	if rec.CredHash == "" {
		return fmt.Errorf("%s: %q has no runner credential, so no runner request can reach it: %w", verb, harp, ErrCapabilityUnavailable)
	}
	c.audit("agent_"+verb, by.auditName(), map[string]string{"harp": harp})

	// The run id rides the request and the RUNNER re-checks it (the same A9
	// correlation StartRun enforces): a runner hosts exactly one run, so a
	// request naming another is refused there rather than trusted here.
	req := &agentcoordpb.RunnerRequest{}
	switch verb {
	case "pause":
		req.Kind = &agentcoordpb.RunnerRequest_PauseRun{PauseRun: &agentcoordpb.PauseRun{RunId: rec.RunID, Reason: reason}}
	case "resume":
		req.Kind = &agentcoordpb.RunnerRequest_ResumeRun{ResumeRun: &agentcoordpb.ResumeRun{RunId: rec.RunID}}
	default:
		return fmt.Errorf("%q is not a runner control verb", verb)
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	resp, err := c.requestRunner(ctx, rec.CredHash, req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", verb, harp, err)
	}
	if code := resp.GetStatus().GetCode(); code != 0 {
		return fmt.Errorf("%s %s refused: %s", verb, harp, resp.GetStatus().GetMessage())
	}
	return nil
}
