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
// parked on the mailbox message id it was relayed as — the correlation the
// parent's agent_send in_reply_to answers.
type pendingApproval struct {
	// targetHarp is the ONLY identity allowed to resolve this approval (the
	// role the mail was addressed to) — defense in depth beyond "the id
	// exists": a foreign session's in_reply_to guess cannot answer someone
	// else's approval.
	targetHarp string
	ch         chan *agentcoordpb.ApprovalDecision // buffered(1)
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
		case ActionRelayToRole, ActionSurfaceToHuman:
			// Yield the child's ceiling slot while the relay parks on a
			// human/parent decision (one-shot-resume plan, Slice 4 / Fork 1's
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
				return approvalResponse(decision)
			}
			c.audit("approval", caller.Harp, map[string]string{
				"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel,
				"action": string(rung.Action), "role": rung.Role, "resolution": "timed_out",
			})
			// Fall through to the next matching rung.
		}
	}
	c.audit("approval", caller.Harp, map[string]string{
		"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": "bottom", "resolution": "denied",
	})
	return approvalResponse(&agentcoordpb.ApprovalDecision{
		Decision: agentcoordpb.ApprovalDecision_DECISION_DECLINE,
		Note:     "ladder exhausted with no rung resolving the request; bottoming at DECLINE",
	})
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
