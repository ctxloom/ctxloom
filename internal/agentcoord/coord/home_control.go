package coord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// The RUNNER half of plane 2's down direction: the responder for
// coordinator-initiated control requests, and the CAPABILITY BACKSTOP that
// makes the whole arrangement safe.
//
// RECEIVER-NORMATIVE (human ruling). The guarantee that a run never
// executes a request kind it did not advertise is the RECEIVER's, not the
// sender's. Capabilities ride each Hello, which is to say each RUN: a resumed
// or relaunched run can advertise differently, so only the far end, at delivery
// time, knows what it can currently do. The coordinator's send-side check is
// experience and routing — a SHOULD — and the drain seam's re-validation is an
// early-rejection optimisation that keeps an undeliverable action unconsumed.
// Neither is the boundary. This file is.
//
// The backstop keys on the ACTUAL request kind — the oneof arm that is really
// present — never on the request's self-declared required_capabilities. That is
// the whole reason the declared field is safe to have: a request that declares
// less than it carries still meets this check on the kind it really is, so
// under-declaring buys nothing.

// inflightCtrl tracks one control request's SINGLE dispatch and its eventual
// answer. resp == nil means the dispatch is still running: a reissue that finds
// it must not start a second one — the running dispatch answers on whichever
// stream is current when it finishes.
type inflightCtrl struct {
	resp *agentcoordpb.AgentResponse
}

// SetRequestHandler registers the executor for coordinator-initiated plane-2
// control requests. One per Home (EngineHost.BindHome registers itself); a
// second registration is a wiring bug and is refused so the first handler is
// not silently replaced mid-run.
func (h *Home) SetRequestHandler(fn func(context.Context, *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctrlHandler != nil {
		return
	}
	h.ctrlHandler = fn
}

// capabilityForRequest maps a control request onto the capability it requires,
// BY ITS ACTUAL KIND. This is the one kind→capability table in the system, and
// it lives here on purpose: the receiver is the only party whose table has to
// be right, because the receiver is the only party whose refusal is a
// guarantee. An arm this build does not know returns "" — refused as
// unrecognised, never defaulted into an executable one.
func capabilityForRequest(req *agentcoordpb.CoordinatorRequest) string {
	switch req.GetKind().(type) {
	case *agentcoordpb.CoordinatorRequest_Steer:
		return CapSteer
	case *agentcoordpb.CoordinatorRequest_Question:
		return CapQuestion
	case *agentcoordpb.CoordinatorRequest_Summarize:
		return CapSummarize
	case *agentcoordpb.CoordinatorRequest_Pause:
		return CapPause
	case *agentcoordpb.CoordinatorRequest_Resume:
		return CapResume
	default:
		return ""
	}
}

// advertises reports whether this runner's own Hello claimed the capability.
// It reads helloCapabilities — the exact list that went out on the wire — so
// the refusal can never disagree with the advertisement the coordinator holds.
func (h *Home) advertises(capability string) bool {
	for _, adv := range h.helloCapabilities() {
		if adv == capability {
			return true
		}
	}
	return false
}

// serveCoordinatorRequest answers one plane-2 control request.
//
// Order is deliberate: identity, then idempotency, then the CAPABILITY
// BACKSTOP, then handler presence, then dispatch. The backstop runs before the
// handler is even consulted, so a kind this runner never advertised cannot
// reach an executor at all — the refusal is structural, not a check the
// executor is trusted to remember.
func (h *Home) serveCoordinatorRequest(req *agentcoordpb.CoordinatorRequest) {
	reqID := req.GetRequestId()
	if reqID == "" {
		h.answerControl(&agentcoordpb.AgentResponse{
			Status: statusErr(codes.InvalidArgument, "request_id is required"),
		})
		return
	}

	h.mu.Lock()
	if tr := h.inflightCtrl[reqID]; tr != nil {
		resp := tr.resp
		h.mu.Unlock()
		if resp != nil {
			// Already answered: re-send the SAME response. A reconnect dropped
			// the original, or a duplicate frame arrived — idempotent either
			// way, and critically the executor is NOT run again.
			h.answerControl(resp)
		}
		// resp == nil: the original dispatch owns the answer.
		return
	}
	handler := h.ctrlHandler
	h.mu.Unlock()

	capability := capabilityForRequest(req)
	if capability == "" {
		// Unrecognised kind — a bug or a newer coordinator, NOT a capability
		// gap. Typed apart from the refusal below so §5.6's fallback selection
		// cannot mistake "I don't know this" for "I can't do this".
		h.answerControl(&agentcoordpb.AgentResponse{
			RequestId: reqID,
			Status: statusErr(codes.InvalidArgument,
				"unrecognised control request kind; this runner executes: "+strings.Join(h.controlKinds(), ", ")),
		})
		return
	}
	if !h.advertises(capability) {
		h.answerControl(&agentcoordpb.AgentResponse{
			RequestId: reqID,
			Status: statusErr(codes.Unimplemented, fmt.Sprintf(
				"this run did not advertise %q and refuses to execute it; it advertised: %s",
				capability, strings.Join(h.helloCapabilities(), ", "))),
		})
		return
	}
	if handler == nil {
		h.answerControl(&agentcoordpb.AgentResponse{
			RequestId: reqID,
			Status: statusErr(codes.Unimplemented,
				"this runner hosts no engine, so it can execute no control request"),
		})
		return
	}

	tr := &inflightCtrl{}
	h.mu.Lock()
	h.inflightCtrl[reqID] = tr
	h.mu.Unlock()

	// Own goroutine: a control request can consume a whole child turn (a
	// question takes minutes), and the recv loop must keep draining — the same
	// reasoning the coordinator's handleAgentRequest applies in the other
	// direction.
	h.goTracked(func() {
		ctx := h.ctx
		if d := req.GetTimeout().AsDuration(); d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		resp := handler(ctx, req)
		if resp == nil {
			resp = &agentcoordpb.AgentResponse{Status: statusErr(codes.Internal, "control handler returned no response")}
		}
		resp.RequestId = reqID
		h.mu.Lock()
		tr.resp = resp
		h.mu.Unlock()
		h.answerControl(resp)
	})
}

// controlKinds names what this runner will execute, for a refusal's prose.
func (h *Home) controlKinds() []string {
	adv := h.helloCapabilities()
	out := make([]string, 0, len(adv))
	for _, a := range adv {
		if a != CapPeerMessaging {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"(none)"}
	}
	return out
}

// answerControl sends one AgentResponse on the current stream. A response that
// finds no stream is DROPPED, not queued: the coordinator's own budget expires
// and its reconnect reissues, at which point inflightCtrl re-delivers the
// cached answer. Queuing it would mean answering a request nobody is waiting
// on anymore.
func (h *Home) answerControl(resp *agentcoordpb.AgentResponse) {
	h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Response{Response: resp}})
}

// ParkControlPayload puts a control request's BODY into this runner's existing
// recv buffer, where agent_recv finds it.
//
// This is the runner half of the one-object ruling: the body arrived ON the
// request, so it never became coordinator mail, and there is no second store to
// keep in step. It is parked HERE — in the buffer Recv already drains — rather
// than in a control-specific stash, so it inherits the dedupe, the cursor-ack
// and the at-least-once accounting the mailbox path already has.
//
// It deliberately does NOT go through deliverNotice. Ordinary mail with no
// parked recv is handed to the engine as a full framed TURN; a control body
// must not be, because the control verb injects its own reminder turn and the
// agent PULLS the body. Routing it through deliverNotice would inject the body
// twice — once as a turn, once as the pulled payload — which is exactly the
// double-delivery the one-object collapse exists to remove.
//
// A parked recv is still completed immediately: the agent is actively polling,
// so there is nothing to wait for.
func (h *Home) ParkControlPayload(pm *agentcoordpb.PeerMessage) {
	h.mu.Lock()
	id := pm.GetMessageId()
	if h.consumed[id] || h.turnPending[id] {
		h.mu.Unlock()
		return
	}
	for _, b := range h.buffer {
		if b.GetMessageId() == id {
			h.mu.Unlock()
			return
		}
	}
	h.buffer = append(h.buffer, pm)
	h.parkedCtl = append(h.parkedCtl, PendingControlPayload{MessageID: id, ParkedAt: time.Now()})
	p := h.park
	var msgs []*agentcoordpb.PeerMessage
	if p != nil && !p.done {
		p.done = true
		h.park = nil
		msgs = h.buffer
		h.buffer = nil
	}
	h.mu.Unlock()
	if msgs != nil {
		p.ch <- msgs
	}
}

// PendingControlPayload is one control body parked into the recv buffer that
// the agent has NOT pulled yet.
//
// ParkedAt is the runner's own clock at park time, never anything a sender
// supplied: it is the input to the re-announcement's anti-habituation, and a
// caller-influenced age would let a steer's sender decide how loudly its own
// instruction nags.
type PendingControlPayload struct {
	MessageID string
	ParkedAt  time.Time
}

// PendingControlPayloads snapshots the control bodies this runner has parked
// and the agent has not pulled, OLDEST FIRST.
//
// It exists for one caller: the engine host's turn-boundary re-announcer. A
// steer is delivered as an announcement plus a pull, and an engine that takes
// the announcement turn without pulling leaves the human's instruction sitting
// here until the process dies. Nothing else in the system can see that state,
// which is precisely why losing an instruction was silent.
func (h *Home) PendingControlPayloads() []PendingControlPayload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PendingControlPayload(nil), h.parkedCtl...)
}
