package coord

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// C2 — the coordinator-side half of the escalation ladder: applying a run's
// Ladder (spawner.go/ladder.go) to one child ApprovalRequest, relaying to
// the parent role as durable mail when a rung says so, and resolving the
// parent's agent_send in_reply_to answer. EVERY hop is journaled: the
// per-rung walk through the informal-but-durable audit journal (c.audit —
// facts.go's "interaction" fact predates and anticipates this exact use,
// see its doc comment), and the request's own final resolution through the
// plane-1 InteractionRecorded AgentEvent the RUNNER emits (enginehost.go) —
// the two together make "which rung answered" queryable from the journals
// without a live consumer.

// pendingApproval is one outstanding relay-to-role/surface-to-human rung,
// parked on a message id — the correlation the answering agent_send's
// in_reply_to answers. For ActionRelayToRole that id is a real mailbox
// message (relayApproval). For ActionSurfaceToHuman there is no mail; the id
// is minted the same way but nothing is queued (surfaceApprovalToHuman).
type pendingApproval struct {
	// targetHarp is the ONLY identity allowed to resolve this approval (the
	// role the mail was addressed to, or — for a human-surfaced rung — the
	// run's parent harp, the identity a human answers as) — defense in depth
	// beyond "the id exists": a foreign session's in_reply_to guess cannot
	// answer someone else's approval.
	targetHarp string
	ch         chan *agentcoordpb.ApprovalDecision // buffered(1)
	// human, when non-nil, is this entry's PendingApprovals()-visible
	// projection — set only for an ActionSurfaceToHuman rung (a
	// relay_to_role entry has no human-observable shape; PendingApprovals
	// skips any entry with human == nil).
	human *PendingApproval
}

// PendingApproval is one approval currently parked on an ActionSurfaceToHuman
// rung, awaiting a human decision — see Coordinator.PendingApprovals /
// OnPendingApproval. A relay_to_role rung (parked on a PARENT ROLE, not a
// human) never appears here.
type PendingApproval struct {
	MessageID string
	Harp      string // the run asking
	Kind      agentcoordpb.ApprovalRequest_ApprovalKind
	Title     string // the tool name
	Payload   json.RawMessage
	Since     time.Time
	// Deadline is when this rung's park times out (Since + the rung's
	// resolved timeout — rung.Timeout, falling back to defaultRelayTimeout
	// exactly like the select below). Slice 3's "expires in Xm" reads this;
	// nothing outside coord can otherwise compute it.
	Deadline time.Time
}

// PendingApprovals lists approvals currently parked for a human, oldest
// first. Snapshot semantics: the slice is a copy, safe to range over after
// the lock is released, and may be stale the instant it returns (an approval
// can resolve or time out concurrently) — callers that need to react to a
// change should use OnPendingApproval instead of polling this in a loop.
func (c *Coordinator) PendingApprovals() []PendingApproval {
	c.mu.Lock()
	out := make([]PendingApproval, 0, len(c.approvals))
	for _, pa := range c.approvals {
		if pa != nil && pa.human != nil {
			out = append(out, *pa.human)
		}
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// OnPendingApproval registers fn to be called every time a human-surfaced
// approval parks (parked=true) or leaves the parked set — answered, timed
// out, or the coordinator shutting down (parked=false). Nil clears it.
//
// The callback runs OUTSIDE c.mu — surfaceApprovalToHuman captures the
// current fn under the lock, releases it, THEN calls fn, so fn may safely
// call back into the coordinator (e.g. PendingApprovals) without
// deadlocking. It must not block: it runs synchronously on the approval's
// own goroutine (serveApproval's, one per in-flight approval — see
// serveApproval's doc comment), so a slow callback delays that approval's
// own park/resolve accounting, not just the observer.
func (c *Coordinator) OnPendingApproval(fn func(PendingApproval, bool)) {
	c.mu.Lock()
	c.onPendingApproval = fn
	c.mu.Unlock()
}

// notifyPendingApproval invokes the registered OnPendingApproval callback,
// if any, OUTSIDE c.mu (capture under the lock, unlock, then call) — never
// invoked while c.mu is held, so a callback that reads back into the
// coordinator (PendingApprovals, another approval) cannot deadlock against
// the goroutine that is notifying it.
func (c *Coordinator) notifyPendingApproval(p PendingApproval, parked bool) {
	c.mu.Lock()
	fn := c.onPendingApproval
	c.mu.Unlock()
	if fn != nil {
		fn(p, parked)
	}
}

// sessionAcceptKey is the ACCEPT_FOR_SESSION cache key: (harp, kind).
//
// Was (runID, kind) — re-scoped in fix/accept-session-scope (one-shot-resume
// plan Slice 1 / Fork 2.1). "For-session" means the child's WHOLE delegated
// session (the harp), not the turn's runID: a run only lasts one turn
// on the persistent model's warm-engine lifetime today, but under the coming
// one-shot model (deferred, Slice 4) every turn mints a fresh runID for the
// same harp, so a runID-keyed grant is wiped at every turn boundary and the
// human is silently re-prompted for something they already said "don't ask
// again" to. The harp is the correct, model-independent scope.
type sessionAcceptKey struct {
	harp string
	kind agentcoordpb.ApprovalRequest_ApprovalKind
}

// serveApproval is the AgentRequest_Approval plane-2 handler
// (runchannel.go's serveAgentRequest dispatches here): the escalation
// ladder itself, run on its own goroutine (handleAgentRequest already
// isolates it — a relay can wait minutes for a human).
func (c *Coordinator) serveApproval(caller Identity, req *agentcoordpb.ApprovalRequest) *agentcoordpb.CoordinatorResponse {
	if !caller.IsChild() {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.PermissionDenied, "approval requests: only a delegated child's run asks the coordinator for a decision")}
	}
	var rec RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(caller.RunID); r != nil {
			rec = *r
		}
	})
	if rec.RunID == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.NotFound, fmt.Sprintf("approval: unknown run %q", caller.RunID))}
	}
	kind := req.GetKind()

	// ACCEPT_FOR_SESSION cache: suppresses the whole ladder walk for a
	// later like-kind ask on the SAME harp (session) — survives a runID
	// change (resume), not just a later turn on the SAME run.
	if d, ok := c.sessionAccepted(rec.Harp, kind); ok {
		c.audit("approval", caller.Harp, map[string]string{
			"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": "cache", "resolution": "granted",
		})
		return approvalResponse(d)
	}

	rungs := rec.Ladder.matchingRungs(kind)
	for i, rung := range rungs {
		rungLabel := fmt.Sprintf("%d/%d", i+1, len(rungs))
		switch rung.Action {
		case ActionAutoAccept:
			c.audit("approval", caller.Harp, map[string]string{
				"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel, "action": string(rung.Action), "resolution": "granted",
			})
			return approvalResponse(&agentcoordpb.ApprovalDecision{
				Decision: agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
				Note:     fmt.Sprintf("rung %s: auto_accept", rungLabel),
			})
		case ActionAutoDecline:
			c.audit("approval", caller.Harp, map[string]string{
				"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel, "action": string(rung.Action), "resolution": "denied",
			})
			return approvalResponse(&agentcoordpb.ApprovalDecision{
				Decision: agentcoordpb.ApprovalDecision_DECISION_DECLINE,
				Note:     fmt.Sprintf("rung %s: auto_decline", rungLabel),
			})
		case ActionRelayToRole:
			// Yield the child's ceiling slot while the relay parks on the
			// parent's decision (one-shot-resume plan, Slice 4 / Fork 1's
			// companion — now load-bearing since the turn cap is a finite
			// resource ceiling, not 1). A child blocked on an approval consumes
			// no compute, so it must not hold an executing slot and starve
			// peers up to the ceiling. Mechanically identical to the recv-park
			// yield (onRolePark) — release the slot + mark the run parked —
			// paired with the reacquire below. Approvals are STRICTLY intra-turn
			// (the tool cannot complete until the decision arrives), so this can
			// never race the turn boundary: the child returns to StateExecuting
			// and finishes the SAME turn once the decision lands. A no-op when
			// the child holds no slot (nothing to yield).
			c.onRolePark(rec.Harp)
			decision, timedOut := c.relayApproval(rec, req, rung)
			c.onRoleUnpark(rec.Harp)
			if resp, done := c.finishParkedRung(caller, rec, kind, rungLabel, rung, decision, timedOut); done {
				return resp
			}
			// Fall through to the next matching rung.
		case ActionSurfaceToHuman:
			// Same slot-yield discipline as ActionRelayToRole above — a child
			// parked waiting for a HUMAN (rather than a parent role) is exactly
			// as idle, and must yield its slot the same way.
			c.onRolePark(rec.Harp)
			decision, timedOut := c.surfaceApprovalToHuman(rec, req, rung)
			c.onRoleUnpark(rec.Harp)
			if resp, done := c.finishParkedRung(caller, rec, kind, rungLabel, rung, decision, timedOut); done {
				return resp
			}
			// Fall through to the next matching rung.
		}
	}
	// THE BOTTOM IS A CANCEL, NOT A DECLINE. Reaching here means NO rung
	// resolved: every matching rung timed out or failed (and a ladder with no
	// matching rung at all resolved nothing by definition). Nobody decided, so
	// nobody refused — and a DECLINE here does not stay an abstraction: it
	// picks a reject_once option at the runner (enginehost.go's
	// resolveApproval), which claude-code-acp reports to the model as "User
	// refused permission to run tool". That is ctxloom's own refusal wearing
	// the operator's face in the engine's durable transcript. A rung that
	// genuinely declines — auto_decline, or a human/parent answering
	// DECISION_DECLINE — returns from the loop above and is unaffected.
	decision := &agentcoordpb.ApprovalDecision{
		Decision: agentcoordpb.ApprovalDecision_DECISION_CANCEL,
		Note:     "ladder exhausted with no rung resolving the request; bottoming at CANCEL",
	}
	c.audit("approval", caller.Harp, map[string]string{
		"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": "bottom",
		"resolution": approvalResolution(decision.GetDecision()),
	})
	return approvalResponse(decision)
}

// finishParkedRung is the audit-and-fall-through handling shared by the two
// PARKING rung actions (ActionRelayToRole/relayApproval and
// ActionSurfaceToHuman/surfaceApprovalToHuman) — same (decision, timedOut)
// contract, same audit keys, same ACCEPT_FOR_SESSION caching and
// fall-through-on-timeout, regardless of which park mechanism produced the
// result. done=false tells serveApproval's loop to fall through to the next
// matching rung; done=true carries the CoordinatorResponse to return
// immediately.
func (c *Coordinator) finishParkedRung(caller Identity, rec RunRecord, kind agentcoordpb.ApprovalRequest_ApprovalKind, rungLabel string, rung LadderRung, decision *agentcoordpb.ApprovalDecision, timedOut bool) (resp *agentcoordpb.CoordinatorResponse, done bool) {
	if !timedOut {
		resolution := approvalResolution(decision.GetDecision())
		c.audit("approval", caller.Harp, map[string]string{
			"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel,
			"action": string(rung.Action), "role": rung.Role,
			"resolution": resolution, "decision": decision.GetDecision().String(),
		})
		if decision.GetDecision() == agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION {
			c.cacheSessionAccept(rec.Harp, kind)
		}
		return approvalResponse(decision), true
	}
	c.audit("approval", caller.Harp, map[string]string{
		"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel,
		"action": string(rung.Action), "role": rung.Role, "resolution": "timed_out",
	})
	return nil, false
}

// approvalResolution maps a decision onto the audit journal's resolution
// vocabulary — DEFINED BY the enforcement allow-list (interactionResolution,
// enginehost.go) rather than restating it, so the coordinator's audit trail
// and the child's own InteractionRecorded can never disagree about the same
// event.
//
// This used to initialise to "granted" and downgrade only on an explicit
// DECLINE/CANCEL: fail-OPEN. Enforcement is fail-CLOSED, so a decision the
// child recorded as RESOLUTION_DENIED was journaled here as "granted" — two
// audit trails contradicting each other, which for a product whose thesis is
// signed, trustworthy context matters well beyond its blast radius. It is an
// INTEGRITY defect, not a privilege-escalation one: no grant ever leaked
// (oily-morse).
func approvalResolution(d agentcoordpb.ApprovalDecision_Decision) string {
	switch interactionResolution(d) {
	case agentcoordpb.InteractionRecorded_RESOLUTION_GRANTED:
		return "granted"
	case agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED:
		return "cancelled"
	default:
		return "denied"
	}
}

func approvalResponse(d *agentcoordpb.ApprovalDecision) *agentcoordpb.CoordinatorResponse {
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(d.GetNote()),
		Kind:   &agentcoordpb.CoordinatorResponse_Approval{Approval: d},
	}
}

// relayApproval queues req as mail to rung's target role (always the
// child's own parent — buildLadder refuses any other role in this window),
// with the request's proto projection as the structured payload, then parks
// on the queued mail's id as the correlation a reply answers via
// in_reply_to. timedOut covers both an elapsed rung.Timeout and a queuing
// failure (the caller falls through to the next rung either way — a relay
// hiccup must not hang the ladder).
func (c *Coordinator) relayApproval(rec RunRecord, req *agentcoordpb.ApprovalRequest, rung LadderRung) (decision *agentcoordpb.ApprovalDecision, timedOut bool) {
	targetHarp := rec.ParentHarp
	if targetHarp == "" {
		clidiag.Warn("ctxloom", "coord: relay approval for %s: no parent harp on the run record", rec.Harp)
		return nil, true
	}
	structured, err := approvalRequestStructured(req)
	if err != nil {
		clidiag.Warn("ctxloom", "coord: relay approval for %s: encode payload: %v", rec.Harp, err)
		return nil, true
	}
	body := fmt.Sprintf("approval requested by %s: %s (%s)", rec.Harp, req.GetTitle(), approvalKindName(req.GetKind()))

	// REGISTER BEFORE PUBLISHING. The mailbox message id IS the correlation a
	// reply carries, and the mail is observable to the parent the instant it
	// is queued — a parked recv is even completed synchronously from inside
	// queueMailPayload. Registering afterwards left a window in which a
	// parent could drain, decide and reply before c.approvals held the id;
	// resolveApprovalReply then missed, the reply degraded to ordinary mail,
	// and this rung sat until its timeout and fell through to DECLINE. A
	// human's approval silently became a denial (pulpy-whiff).
	//
	// So the id is minted up front and the pendingApproval is registered
	// while the mail does not yet exist: a reply can only arrive after the
	// mail is observable, which is strictly after registration.
	msgID := newMessageID()
	pa := &pendingApproval{targetHarp: targetHarp, ch: make(chan *agentcoordpb.ApprovalDecision, 1)}
	c.mu.Lock()
	if c.approvals == nil {
		c.approvals = make(map[string]*pendingApproval)
	}
	c.approvals[msgID] = pa
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.approvals[msgID] == pa {
			delete(c.approvals, msgID)
		}
		c.mu.Unlock()
	}()

	if _, _, err := c.queueMailPayloadID(msgID, rec.Harp, targetHarp, "approval_request", body, structured, ""); err != nil {
		clidiag.Warn("ctxloom", "coord: relay approval for %s: queue mail: %v", rec.Harp, err)
		return nil, true
	}
	if hook := c.onApprovalMailQueued; hook != nil {
		hook(msgID)
	}

	timeout := rung.Timeout
	if timeout <= 0 {
		timeout = defaultRelayTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case d := <-pa.ch:
		return d, false
	case <-timer.C:
		return nil, true
	case <-c.baseCtx.Done():
		return nil, true
	}
}

// surfaceApprovalToHuman is ActionSurfaceToHuman's sibling to relayApproval:
// SAME register-before-publish discipline (pulpy-whiff), SAME (decision,
// timedOut) contract, SAME audit keys (finishParkedRung handles those,
// identically for both). The difference is what it parks ON and how it
// becomes observable: relayApproval addresses a parent ROLE and its
// publish moment is the mail becoming visible in that role's mailbox
// (queueMailPayloadID); this addresses a HUMAN and its publish moment is
// notifyPendingApproval's callback — there is no mailbox message, so
// PendingApprovals()/OnPendingApproval are the only way anything outside
// this coordinator learns the approval exists.
//
// Resolution still rides resolveApprovalReply/peerSend unchanged: the human
// answers with AgentSend(caller, to, "", body, structured, inReplyTo=msgID),
// exactly as a parent answers a relayed approval, and the SAME targetHarp
// check (pa.targetHarp == caller.Harp) gates who may answer — the run's
// parent harp today (the identity the interactive owner session — the
// eventual CLI surface, slice 3 — answers as; there is no separate "human"
// identity in this window). Timeout still comes from rung.Timeout (the SAME
// defaultRelayTimeout fallback as a relay rung with no explicit timeout).
func (c *Coordinator) surfaceApprovalToHuman(rec RunRecord, req *agentcoordpb.ApprovalRequest, rung LadderRung) (decision *agentcoordpb.ApprovalDecision, timedOut bool) {
	targetHarp := rec.ParentHarp
	if targetHarp == "" {
		clidiag.Warn("ctxloom", "coord: surface approval for %s: no parent harp on the run record", rec.Harp)
		return nil, true
	}

	var payload json.RawMessage
	if p := req.GetPayload(); p != nil {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(p)
		if err != nil {
			clidiag.Warn("ctxloom", "coord: surface approval for %s: encode payload: %v", rec.Harp, err)
			return nil, true
		}
		payload = raw
	}

	// Resolved up front (not just before the select, as relayApproval does)
	// because Deadline needs it too: Since+timeout must describe the SAME
	// timeout the select below actually waits on, not a value recomputed
	// later that could in principle drift from it.
	timeout := rung.Timeout
	if timeout <= 0 {
		timeout = defaultRelayTimeout
	}

	// REGISTER BEFORE PUBLISHING — the same discipline relayApproval documents
	// (pulpy-whiff), applied to this rung's own publish moment: a human
	// answering the INSTANT they observe the OnPendingApproval callback must
	// find the pendingApproval already registered, or the reply falls through
	// to ordinary mail and this rung sits until its timeout. So both the
	// pendingApproval AND the PendingApproval it carries are built and
	// inserted into c.approvals BEFORE notifyPendingApproval (this rung's
	// "publish") is ever called.
	msgID := newMessageID()
	since := c.now()
	pending := PendingApproval{
		MessageID: msgID,
		Harp:      rec.Harp,
		Kind:      req.GetKind(),
		Title:     req.GetTitle(),
		Payload:   payload,
		Since:     since,
		Deadline:  since.Add(timeout),
	}
	pa := &pendingApproval{targetHarp: targetHarp, ch: make(chan *agentcoordpb.ApprovalDecision, 1), human: &pending}
	c.mu.Lock()
	if c.approvals == nil {
		c.approvals = make(map[string]*pendingApproval)
	}
	c.approvals[msgID] = pa
	c.mu.Unlock()
	// Cleanup ALWAYS deletes the entry and THEN notifies parked=false, on
	// every exit path (answered, timed out, or shutting down) — deletion
	// first so a callback that immediately calls PendingApprovals() never
	// observes an entry this call is already leaving.
	defer func() {
		c.mu.Lock()
		if c.approvals[msgID] == pa {
			delete(c.approvals, msgID)
		}
		c.mu.Unlock()
		c.notifyPendingApproval(pending, false)
	}()

	clidiag.Warn("ctxloom", "coord: approval %q for %s (%s) is waiting for a human decision — no relay, no engine progress until it is answered or times out", req.GetTitle(), rec.Harp, approvalKindName(req.GetKind()))
	c.notifyPendingApproval(pending, true)
	if hook := c.onApprovalMailQueued; hook != nil {
		// Reuses relayApproval's test seam (its own doc explains why only a
		// hook fired at THIS instant can assert register-before-publish
		// deterministically) — the seam's contract is "called once the
		// approval is observable to whatever answers it", which is equally
		// true here even though nothing was mailed.
		hook(msgID)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case d := <-pa.ch:
		return d, false
	case <-timer.C:
		clidiag.Warn("ctxloom", "coord: approval %q for %s (%s) timed out after %s waiting for a human — falling through", req.GetTitle(), rec.Harp, approvalKindName(req.GetKind()), timeout)
		return nil, true
	case <-c.baseCtx.Done():
		return nil, true
	}
}

// resolveApprovalReply checks whether inReplyTo names an outstanding
// relayed approval this caller may answer (only the exact role the mail was
// addressed to — defense in depth). matched=false lets peerSend fall
// through to ordinary mail delivery, so a stale/duplicate/foreign
// in_reply_to degrades gracefully instead of failing the whole agent_send.
func (c *Coordinator) resolveApprovalReply(caller Identity, inReplyTo string, structured json.RawMessage) (disposition string, err error, matched bool) {
	c.mu.Lock()
	pa := c.approvals[inReplyTo]
	if pa == nil || pa.targetHarp != caller.Harp {
		c.mu.Unlock()
		return "", nil, false
	}
	c.mu.Unlock()

	// DECODE BEFORE CONSUME (legal-jelly). This used to delete the pending
	// approval FIRST and decode second, with no restore on the error path —
	// so ONE unusable reply permanently burned the correlation. The retry
	// then found nothing registered, fell through to ordinary chat mail and
	// reported DELIVERY_QUEUED (success!), and the child sat out its whole
	// relay timeout before the ladder bottomed at DECLINE. No approval INTENT
	// was even required: any agent_send carrying a live approval's in_reply_to
	// is decoded as an ApprovalDecision, so a bare courtesy ack burned it too.
	//
	// The rule is now the obvious one: consume nothing this call cannot
	// honour. A rejected reply leaves the approval outstanding and answerable.
	decision, derr := decisionFromStructured(structured)
	if derr != nil {
		// AND leave a trace. The failed-decode path used to queue no mail and
		// write no fact, so a burned approval left ZERO disk evidence — which
		// is precisely why the live incident could not identify which send
		// burned it. One audit fact makes this whole class self-diagnosing.
		c.audit("approval_reply", caller.Harp, map[string]string{
			"in_reply_to": inReplyTo, "resolution": "rejected", "error": derr.Error(),
		})
		return "", fmt.Errorf("agent_send in_reply_to %s: decode ApprovalDecision from structured: %w", inReplyTo, derr), true
	}

	// Consume exactly once, now that the payload is honourable. A concurrent
	// reply (or the rung's own timeout teardown) may have taken it in the
	// meantime — say so rather than reporting a decision that reached nobody.
	c.mu.Lock()
	held := c.approvals[inReplyTo] == pa
	if held {
		delete(c.approvals, inReplyTo)
	}
	c.mu.Unlock()
	if !held {
		c.audit("approval_reply", caller.Harp, map[string]string{
			"in_reply_to": inReplyTo, "resolution": "already_resolved",
		})
		return "", fmt.Errorf("agent_send in_reply_to %s: that approval is no longer outstanding — it was already answered, or its rung timed out and the ladder moved on", inReplyTo), true
	}

	select {
	case pa.ch <- decision:
	default: // relayApproval already gave up (timed out) — the ladder moved on
	}
	return fmt.Sprintf("approval decision recorded (%s)", decision.GetDecision()), nil, true
}

// sessionAccepted reads the ACCEPT_FOR_SESSION cache, keyed by harp — the
// child's whole delegated session, not the turn's runID (see
// sessionAcceptKey's doc).
func (c *Coordinator) sessionAccepted(harp string, kind agentcoordpb.ApprovalRequest_ApprovalKind) (*agentcoordpb.ApprovalDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.sessionAccepts[sessionAcceptKey{harp, kind}]
	return d, ok
}

// cacheSessionAccept records a DECISION_ACCEPT_FOR_SESSION grant. Later
// like-kind requests on the SAME harp — including a LATER run under the
// same harp (a resume, one-shot's per-turn shape) — get a plain ACCEPT from
// cache (the "_FOR_SESSION" qualifier is spent at the moment it is granted,
// not re-asserted on every cache hit).
func (c *Coordinator) cacheSessionAccept(harp string, kind agentcoordpb.ApprovalRequest_ApprovalKind) {
	c.mu.Lock()
	if c.sessionAccepts == nil {
		c.sessionAccepts = make(map[sessionAcceptKey]*agentcoordpb.ApprovalDecision)
	}
	c.sessionAccepts[sessionAcceptKey{harp, kind}] = &agentcoordpb.ApprovalDecision{
		Decision: agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
		Note:     "accept-for-session cache",
	}
	c.mu.Unlock()
}

// clearSessionAccepts drops a harp's ACCEPT_FOR_SESSION cache entries.
//
// This is now scoped to the HARP's session lifetime, not the run's: it must
// NOT be called on every ordinary terminateRun (chat-close / runner-loss /
// runner-exit / orphaned-by-restart) — factRunEnded's own doc is explicit
// that "the harp stays resumable" after any of those, and under one-shot
// (deferred, one-shot-resume.plan.md Slice 4) EVERY turn boundary funnels
// through terminateRun, so clearing there would reintroduce exactly the
// regression this re-scoping fixes (TestApproval_AcceptForSessionSurvivesRunIDChange).
// terminateRun calls this only for CauseStopped (an explicit agent_stop) —
// the one terminal cause that reflects a deliberate, final end of the harp's
// session rather than an ordinary/lossy turn boundary the harp is expected
// to resume from.
func (c *Coordinator) clearSessionAccepts(harp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.sessionAccepts {
		if k.harp == harp {
			delete(c.sessionAccepts, k)
		}
	}
}

// approvalRequestStructured projects an ApprovalRequest onto the mailbox's
// JSON structured payload — the "proto-projection structured payload" the
// relay mail carries, nested under "approval_request" so it composes with
// Message.Kind's own "kind" field (peerMessageProto merges both).
func approvalRequestStructured(req *agentcoordpb.ApprovalRequest) (json.RawMessage, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{"approval_request": raw})
}

// envelopeKeys are the mailbox ENVELOPE's own keys, which ride the SAME
// structured payload as the message body (peerMessageProto merges both, and
// servePeerSend reads the message kind straight off structured.kind). Both
// agent_send surfaces document `kind` as a thing you put in structured, so a
// reply that carries it is following the documentation — it must not be a
// decode failure. Stripped here rather than tolerated with protojson's
// DiscardUnknown, which would ALSO swallow a misspelled `decision` and turn a
// loud failure into a silent UNSPECIFIED-shaped one.
var envelopeKeys = []string{"kind"}

// stripEnvelopeKeys removes the envelope's own keys from an otherwise
// ApprovalDecision-shaped payload. A payload that is not a JSON object is
// returned untouched so protojson produces the authoritative error.
func stripEnvelopeKeys(structured json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(structured, &obj); err != nil {
		return structured
	}
	stripped := false
	for _, k := range envelopeKeys {
		if _, ok := obj[k]; ok {
			delete(obj, k)
			stripped = true
		}
	}
	if !stripped {
		return structured
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return structured
	}
	return out
}

// decisionVocabulary renders ApprovalDecision.Decision's declared, non-zero
// vocabulary, derived from the generated enum table so the diagnostic can
// never drift from the wire contract it is describing.
func decisionVocabulary() string {
	names := make([]string, 0, len(agentcoordpb.ApprovalDecision_Decision_name))
	for v, n := range agentcoordpb.ApprovalDecision_Decision_name {
		if v == int32(agentcoordpb.ApprovalDecision_DECISION_UNSPECIFIED) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, " | ")
}

// decisionShape is the self-documenting half of every decode error: the
// accepted SHAPE and the accepted VOCABULARY, in the reply's own terms. Five
// lines that would have made the original incident self-correcting — the
// coordinator was guessing at both, and the error it got back named neither.
func decisionShape() string {
	return fmt.Sprintf(`answering a relayed approval_request, "structured" IS the ApprovalDecision: {"decision": <%s>, "note": "..."} — correlated by in_reply_to = the approval_request's message_id. The envelope key "kind" may ride alongside and is ignored; any OTHER key is rejected`, decisionVocabulary())
}

// decisionFromStructured decodes the parent's agent_send structured payload
// into an ApprovalDecision (protojson accepts both proto and camelCase field
// names). Deliberately strict: no protojson DiscardUnknown, because a
// misspelled `decision` would then decode to DECISION_UNSPECIFIED and this
// whole cluster is about failures that look like successes. Every rejection
// names the shape AND the vocabulary.
func decisionFromStructured(structured json.RawMessage) (*agentcoordpb.ApprovalDecision, error) {
	if len(structured) == 0 {
		return nil, fmt.Errorf("structured is required — %s", decisionShape())
	}
	d := &agentcoordpb.ApprovalDecision{}
	if err := protojson.Unmarshal(stripEnvelopeKeys(structured), d); err != nil {
		return nil, fmt.Errorf("%w — %s", err, decisionShape())
	}
	// proto3 enums are OPEN: protojson accepts {"decision": 99} without
	// error, which used to sail through, journal itself as "granted" at the
	// coordinator and be enforced as a denial at the child. A value outside
	// the declared vocabulary is rejected at the boundary (oily-morse).
	if _, ok := agentcoordpb.ApprovalDecision_Decision_name[int32(d.GetDecision())]; !ok {
		return nil, fmt.Errorf("decision %d is outside the declared vocabulary — %s", int32(d.GetDecision()), decisionShape())
	}
	if d.GetDecision() == agentcoordpb.ApprovalDecision_DECISION_UNSPECIFIED {
		return nil, fmt.Errorf("decision is required — %s", decisionShape())
	}
	return d, nil
}
