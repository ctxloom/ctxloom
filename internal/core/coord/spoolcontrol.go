package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// THE INTERACTION PLANE: steer and the correlated asks are FILES in the
// target's own in/ spool, same predicate as the mail plane (spoolDeliverTo).
//
// What follows from the file being the message:
//
//   - A STEER IS DURABLE AND WITHDRAWABLE. It is ordinary mail with a
//     reserved kind: it survives a relaunch, it is delivered as the next turn
//     like any other message, an unread one is VISIBLE (a file still sitting
//     in in/), and it can be RETRACTED before it is taken — the rename into
//     in/withdrawn/ either wins or loses to the reader, and the filesystem is
//     the arbiter. Stale-but-consumed is the accepted cost; withdrawal is the
//     remedy.
//
//   - AN ASK DOES NOT WAIT. A question or a summarize request is a file; the
//     asker gets its id at once and nothing here waits for an answer. The
//     answer is the child's own agent_send quoting that id: ordinary mail to
//     the child's parent, whose arrival triggers the asker's next turn like
//     any other mail. Nothing captures a turn's output and calls it the
//     answer — the runner's automatic turn report quotes the id too, but it
//     is marked automatic, and only a reply the child CHOSE to send closes
//     the ask.
//
//   - AN ASK IS OPEN UNTIL ANSWERED, and recorded open BEFORE it is
//     published, so a reply that lands the instant the file becomes
//     observable still finds the record to close. A child that ends with an
//     ask still open leaves its asker a notice correlated to that ask
//     (noticeUnansweredAsks) instead of silence.

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

// ---- steer -------------------------------------------------------------

// steerViaSpool is the steer route: the instruction becomes ONE durable file
// in the target's in/ spool, with the reserved `steer` kind.
//
// It goes through the mail chokepoint (steerAsMail, and through it
// queueMailPayload) rather than writing the file itself, and that is the point:
// the empty-message and no-recipient refusals, the audit, the spool write and
// the delivery-by-state wake are all one path's, so a steer cannot arrive by
// a discipline ordinary mail does not have.
//
// What this route adds is the KIND — which renders into the delivered turn's
// provenance header, so the agent sees an instruction rather than an anonymous
// message — and the returned ID: the message is a file that can still be
// retracted, so the id is a live withdraw handle.
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
// removes it. `ctxloom doctor` (doctorCheckSpoolBacklog) names it once it has
// sat unread past its bound, so it never passes for a delivered one.
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
		// Nothing to withdraw FROM: a run the spool does not deliver to
		// cannot have been steered (ControlSteer refuses it at the same
		// predicate), so no steer file for it can exist.
		return fmt.Errorf("steer withdraw: %q is not delivered by the spool, so no steer to it exists to retract: %w",
			harp, ErrNoSuchSteer)
	}

	mapper := c.mapper
	ref, found := c.findSpoolMessage(harp, spool.DirIn, messageID)
	if !found {
		// Not in in/. Either it was delivered (the child took it) or it never
		// existed — two different answers, and the spool's delivered record
		// tells them apart for as long as it keeps the identity
		// (spool.DeliveredRetention); past that, it reads as never existed.
		if spool.ValidateName(messageID) != nil {
			// Not a name any spool file or record entry can have.
			return fmt.Errorf("%w: %s", ErrNoSuchSteer, messageID)
		}
		taken, err := spool.Delivered(mapper, harp, messageID)
		if err != nil {
			return fmt.Errorf("steer withdraw: %w", err)
		}
		if taken {
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
			// finding it in the delivered record, because it is the same fact.
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
	c.mailCourier().Announce(harp, withdrawn, "withdrew")
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
		if e.Identity() == messageID {
			return e.Ref, true
		}
	}
	return spool.Ref{}, false
}

// ---- correlated asks (question / summarize) -----------------------------

// openAsk is one ask its target has not answered by choice: what the
// unanswered-ask notice needs to name the ask and its kind.
type openAsk struct {
	// target is the ONLY harp whose reply closes the ask: an id is not
	// authority, so a reply quoting it from anyone else is ordinary mail and
	// leaves the ask open.
	target string
	kind   string
}

// controlAsk publishes one correlated ask (KindQuestion or KindSummarize)
// and returns its id at once; it never waits for the answer.
//
// ORDER IS THE CONTRACT: mint the id, record the ask open, THEN publish. A
// reply can only exist after the file is observable, so it always finds the
// record to close. Recorded afterwards, a fast answer would find none, and the
// child's end would report an answered ask as unanswered.
func (c *Coordinator) controlAsk(by ControlInitiator, harp, kind, text string) (string, error) {
	if _, err := c.controlTarget(by, harp); err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("%s: text is required", kind)
	}
	if !c.spoolDeliverTo(harp) {
		return "", fmt.Errorf("%w: %q has no correlated-ask path "+
			"(asks ride the spool, and this run is not delivered by it)", ErrAskUnavailable, harp)
	}
	c.audit("agent_"+kind, by.auditName(), map[string]string{"harp": harp})

	askID := newMessageID()
	c.mu.Lock()
	if c.openAsks == nil {
		c.openAsks = make(map[string]openAsk)
	}
	c.openAsks[askID] = openAsk{target: harp, kind: kind}
	c.mu.Unlock()
	if hook := c.onAskPublished; hook != nil {
		// THE ORDERING SEAM, between recording and publishing: the one
		// instant at which "the ask is already open" distinguishes this
		// ordering from one that records after the write.
		hook(askID)
	}
	// The same delivery-by-state wake ordinary mail gets: an idle child is
	// prompted with the ask as a new turn, an ended one is resumed for it.
	if _, err := c.deliverMailID(askID, by.auditName(), harp, kind, text, nil, ""); err != nil {
		c.mu.Lock()
		delete(c.openAsks, askID)
		c.mu.Unlock()
		return "", fmt.Errorf("%s %s: %w", kind, harp, err)
	}
	return askID, nil
}

// settleAsk closes the ask a message routed from sender answers. Only the
// target's DELIBERATE reply closes it: the runner's automatic turn report
// quotes the id of the ask that started the turn, and is marked so
// (IsAutoReport) precisely because it is not the child choosing to answer.
func (c *Coordinator) settleAsk(sender, inReplyTo string, structured json.RawMessage) {
	if IsAutoReport(structured) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.openAsks[inReplyTo]; ok && a.target == sender {
		delete(c.openAsks, inReplyTo)
	}
}

// noticeUnansweredAsks tells the asker of every ask harp's ENDED run left
// unanswered, one notice per ask, correlated to it by in_reply_to. Without it
// the asker hears only the uncorrelated exit notice (or, for an idle-reaped
// child, nothing at all), and cannot tell which of its asks will never be
// answered.
//
// It runs after the spool sweep has routed what harp sent, so an answer the
// child wrote before it ended has already closed its ask. An ask whose file is
// still unread in harp's in/ is skipped: the ended-child rule resumes the
// child for it (relaunchForLeftoverMail), so it may yet be answered.
func (c *Coordinator) noticeUnansweredAsks(harp string) {
	var due []string
	c.mu.Lock()
	for id, a := range c.openAsks {
		if a.target == harp {
			due = append(due, id)
		}
	}
	c.mu.Unlock()
	if len(due) == 0 {
		return
	}
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil || !rec.Ended {
		return
	}
	for _, id := range due {
		if _, unread := c.findSpoolMessage(harp, spool.DirIn, id); unread {
			continue
		}
		c.mu.Lock()
		a, open := c.openAsks[id]
		delete(c.openAsks, id)
		c.mu.Unlock()
		if !open {
			continue
		}
		body := fmt.Sprintf("agent %q (session %s) ended (%s) without answering your %s %s",
			rec.Agent, harp, rec.Cause, a.kind, id)
		if _, err := c.queueMailPayload(harp, rec.ParentHarp, KindExited, body, nil, id); err != nil {
			c.rep.Warnf("agent %s: the unanswered-%s notice for %s could not be written to %s's spool (%v)",
				harp, a.kind, id, rec.ParentHarp, err)
		}
	}
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
//
// newlyPaused is the runner's own word on whether THIS call installed the
// gate or found it already installed. Pause is idempotent, so both are
// success; a caller who cannot tell them apart cannot tell a deliberate
// second hold from a pause that never took.
func (c *Coordinator) ControlPause(ctx context.Context, by ControlInitiator, harp, reason string) (newlyPaused bool, err error) {
	resp, err := c.runnerControl(ctx, by, harp, "pause", reason)
	if err != nil {
		return false, err
	}
	res, _ := resp.Kind.(PauseRunResult)
	return res.NewlyPaused, nil
}

// ControlResume releases a paused target: turns held at the gate are handed to
// the engine in arrival order. newlyResumed mirrors ControlPause's
// newlyPaused: whether this call released the gate or found none.
func (c *Coordinator) ControlResume(ctx context.Context, by ControlInitiator, harp string) (newlyResumed bool, err error) {
	resp, err := c.runnerControl(ctx, by, harp, "resume", "")
	if err != nil {
		return false, err
	}
	res, _ := resp.Kind.(ResumeRunResult)
	return res.NewlyResumed, nil
}

// runnerControl issues one pause/resume against harp's runner and returns
// what the runner answered (an OK response; a refusal is the error).
//
// It runs the SAME ownership guards every control verb runs, and the same
// cutover predicate the delivery planes use — not because pause needs a spool,
// but because a run split across the two worlds is the one state nothing
// reconciles: the predicate is what says "this run is on the new plane", and
// every control surface has to agree about that or the answer depends on which
// one you asked.
func (c *Coordinator) runnerControl(ctx context.Context, by ControlInitiator, harp, verb, reason string) (RunnerResponse, error) {
	rec, err := c.controlTarget(by, harp)
	if err != nil {
		return RunnerResponse{}, err
	}
	if !c.spoolDeliverTo(harp) {
		return RunnerResponse{}, fmt.Errorf("%s: %q is not a run this coordinator tracks, so no runner request can reach it: %w",
			verb, harp, ErrCapabilityUnavailable)
	}
	if rec.CredHash == "" {
		return RunnerResponse{}, fmt.Errorf("%s: %q has no runner credential, so no runner request can reach it: %w", verb, harp, ErrCapabilityUnavailable)
	}
	c.audit("agent_"+verb, by.auditName(), map[string]string{"harp": harp})

	// The run id rides the request and the RUNNER re-checks it (the same A9
	// correlation StartRun enforces): a runner hosts exactly one run, so a
	// request naming another is refused there rather than trusted here.
	req := RunnerRequest{}
	switch verb {
	case "pause":
		req.Kind = PauseRun{RunID: rec.RunID, Reason: reason}
	case "resume":
		req.Kind = ResumeRun{RunID: rec.RunID}
	default:
		return RunnerResponse{}, fmt.Errorf("%q is not a runner control verb", verb)
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultRequestTimeout)
		defer cancel()
	}
	resp, err := c.requestRunner(ctx, rec.CredHash, req)
	if err != nil {
		return RunnerResponse{}, fmt.Errorf("%s %s: %w", verb, harp, err)
	}
	if resp.Err != nil {
		return RunnerResponse{}, fmt.Errorf("%s %s refused: %s", verb, harp, resp.Err.Error())
	}
	// The runner owns the gate; this is the coordinator's record of it, which
	// is what lets a delivery say "held" instead of "woke it into a new turn".
	// Every pause and resume passes through here, so the record cannot miss
	// one this coordinator issued.
	c.setRunPaused(rec.RunID, verb == "pause")
	return resp, nil
}

// setRunPaused records (or clears) that runID is held at its runner's gate.
func (c *Coordinator) setRunPaused(runID string, paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !paused {
		delete(c.pausedRuns, runID)
		return
	}
	if c.pausedRuns == nil {
		c.pausedRuns = make(map[string]struct{})
	}
	c.pausedRuns[runID] = struct{}{}
}

// runPaused reports whether runID is held at its runner's gate by a pause
// this coordinator issued.
func (c *Coordinator) runPaused(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pausedRuns[runID]
	return ok
}
