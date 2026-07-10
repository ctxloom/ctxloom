package coord

import (
	"encoding/json"
	"fmt"
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

// sessionAcceptKey is the ACCEPT_FOR_SESSION cache key: (run, kind).
type sessionAcceptKey struct {
	runID string
	kind  agentcoordpb.ApprovalRequest_ApprovalKind
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
	// later like-kind ask on the SAME run.
	if d, ok := c.sessionAccepted(rec.RunID, kind); ok {
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
			decision, timedOut := c.relayApproval(rec, req, rung)
			if !timedOut {
				resolution := "granted"
				switch decision.GetDecision() {
				case agentcoordpb.ApprovalDecision_DECISION_DECLINE:
					resolution = "denied"
				case agentcoordpb.ApprovalDecision_DECISION_CANCEL:
					resolution = "cancelled"
				}
				c.audit("approval", caller.Harp, map[string]string{
					"run_id": rec.RunID, "kind": approvalKindName(kind), "rung": rungLabel,
					"action": string(rung.Action), "role": rung.Role,
					"resolution": resolution, "decision": decision.GetDecision().String(),
				})
				if decision.GetDecision() == agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION {
					c.cacheSessionAccept(rec.RunID, kind)
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
	msgID, _, err := c.queueMailPayload(rec.Harp, targetHarp, "approval_request", body, structured, "")
	if err != nil {
		clidiag.Warn("ctxloom", "coord: relay approval for %s: queue mail: %v", rec.Harp, err)
		return nil, true
	}

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
	delete(c.approvals, inReplyTo)
	c.mu.Unlock()

	decision, derr := decisionFromStructured(structured)
	if derr != nil {
		return "", fmt.Errorf("agent_send in_reply_to %s: decode ApprovalDecision from structured: %w", inReplyTo, derr), true
	}
	select {
	case pa.ch <- decision:
	default: // relayApproval already gave up (timed out) — the ladder moved on
	}
	return fmt.Sprintf("approval decision recorded (%s)", decision.GetDecision()), nil, true
}

// sessionAccepted reads the ACCEPT_FOR_SESSION cache.
func (c *Coordinator) sessionAccepted(runID string, kind agentcoordpb.ApprovalRequest_ApprovalKind) (*agentcoordpb.ApprovalDecision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.sessionAccepts[sessionAcceptKey{runID, kind}]
	return d, ok
}

// cacheSessionAccept records a DECISION_ACCEPT_FOR_SESSION grant. Later
// like-kind requests on the SAME run get a plain ACCEPT from cache (the
// "_FOR_SESSION" qualifier is spent at the moment it is granted, not
// re-asserted on every cache hit).
func (c *Coordinator) cacheSessionAccept(runID string, kind agentcoordpb.ApprovalRequest_ApprovalKind) {
	c.mu.Lock()
	if c.sessionAccepts == nil {
		c.sessionAccepts = make(map[sessionAcceptKey]*agentcoordpb.ApprovalDecision)
	}
	c.sessionAccepts[sessionAcceptKey{runID, kind}] = &agentcoordpb.ApprovalDecision{
		Decision: agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
		Note:     "accept-for-session cache",
	}
	c.mu.Unlock()
}

// clearSessionAccepts drops a run's ACCEPT_FOR_SESSION cache entries at
// terminal (children.go's terminateRun) — the cache is run-scoped and must
// not outlive the run it was granted for.
func (c *Coordinator) clearSessionAccepts(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.sessionAccepts {
		if k.runID == runID {
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

// decisionFromStructured decodes the parent's agent_send structured payload
// into an ApprovalDecision (protojson accepts both proto and camelCase
// field names).
func decisionFromStructured(structured json.RawMessage) (*agentcoordpb.ApprovalDecision, error) {
	if len(structured) == 0 {
		return nil, fmt.Errorf(`structured is required (an ApprovalDecision projection, e.g. {"decision": "DECISION_ACCEPT"})`)
	}
	d := &agentcoordpb.ApprovalDecision{}
	if err := protojson.Unmarshal(structured, d); err != nil {
		return nil, err
	}
	if d.GetDecision() == agentcoordpb.ApprovalDecision_DECISION_UNSPECIFIED {
		return nil, fmt.Errorf("decision is required (ACCEPT | ACCEPT_FOR_SESSION | DECLINE | CANCEL)")
	}
	return d, nil
}
