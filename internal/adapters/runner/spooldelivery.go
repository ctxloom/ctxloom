package runner

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"google.golang.org/grpc/codes"
)

// SpoolDeliveryStats reports this runner's cumulative file-plane outcomes.
func (h *Home) SpoolDeliveryStats() coord.SpoolDeliveryStats { return h.spoolDeliveryCount.Stats() }

// startSpoolReactor brings up the runner's own in/ reader. Its reconciliation
// set is a single role — a runner has exactly one spool — but it is the same
// machinery as the coordinator's on purpose: the startup pass, the collapse of
// concurrent wakes, and the serialisation that keeps two triggers from
// delivering one file twice are all properties this side needs identically.
func (h *Home) startSpoolReactor() {
	h.spoolIn = coord.NewSpoolReactor(
		func(string) { h.sweepSpoolIn() },
		func() []string {
			// No role until the identity is bound: an unbound run has no
			// spool to reconcile.
			if harp := h.Harp(); harp != "" {
				return []string{harp}
			}
			return nil
		},
		h.cfg.SpoolSweepInterval,
	)
	// Same one-seam rule as the coordinator's, and it lands on SweepSpoolIn so
	// the doorbell joins the other triggers at the single funnel that call
	// already documents.
	h.SetSpoolDoorbellHandler(func(string, spool.Ref) { h.SweepSpoolIn() })
	h.goTracked(func() { h.spoolIn.Run(h.ctx) })
}

// SweepSpoolIn asks for a reconciliation sweep of this run's in/ spool. It is
// the one call every trigger funnels through — turn boundary, run-channel
// reattach, doorbell — so a new trigger cannot accidentally introduce a second
// way of reading the same directory.
func (h *Home) SweepSpoolIn() {
	if harp := h.Harp(); harp != "" {
		h.spoolIn.Mark(harp)
	}
}

// sweepSpoolIn delivers everything currently in this run's in/ spool, oldest
// first, through the SAME delivery-by-state seam a pushed mailbox notice uses
// (deliverNotice): a live engine gets a new turn, and anything arriving before
// the engine exists waits in the buffer.
//
// Delivery is where the file's journey through this process starts, not where
// it ends: the delivery ack (spool.Deliver: record the identity, then delete
// the file) happens later, at the moment the delivery is proven (the engine
// accepted the turn). deliverNotice's own dedupe on message id is what
// makes a doorbell and a sweep that race resolve to one delivery.
func (h *Home) sweepSpoolIn() {
	if h.exited.Load() {
		// The engine has exited (Home.exited): a file swept now belongs to
		// the run the coordinator launches next, and delivering it here
		// would consume it into a sink nothing reads.
		return
	}
	if h.isOwner() {
		// The session owner's in/ is its turn-start hook's to read, claim
		// and acknowledge: a second reader here would deliver the same mail
		// twice. All a sweep owes the owner is the wake.
		h.wakeOwner()
		return
	}
	mapper := h.cfg.Mapper
	path, err := spool.DirPath(mapper, h.Harp(), spool.DirIn)
	if err != nil {
		h.rep.Warnf("runner: cannot resolve this run's in/ spool: %v", err)
		h.spoolDeliveryCount.Failed.Add(1)
		return
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return // nothing has ever been written for this run
	}
	res, err := spool.Sweep(mapper, h.Harp(), spool.DirIn)
	if err != nil {
		h.rep.Warnf("runner: sweeping this run's in/ spool: %v", err)
		h.spoolDeliveryCount.Failed.Add(1)
		return
	}
	for _, p := range res.Problems {
		h.rep.Warnf("runner: a file in this run's in/ spool is not a message and will not be delivered: %v", p.Error())
		h.spoolDeliveryCount.Failed.Add(1)
	}
	for _, e := range res.Entries {
		h.deliverSpoolEntry(e)
	}
}

// deliverSpoolEntry projects one swept in/ entry onto the delivery seam and
// delivers it, or moves it to in/failed/ naming why it could not be.
func (h *Home) deliverSpoolEntry(e spool.Entry) {
	delivered, err := spool.Delivered(h.cfg.Mapper, h.Harp(), e.Identity())
	if err != nil {
		h.rep.Warnf("runner: cannot tell whether %s was already delivered, delivering it: %v", e.Ref, err)
	}
	if delivered {
		// Recorded but not deleted: a delivery interrupted between its
		// record and its delete. Finish it; never deliver it twice.
		if err := spool.Deliver(h.cfg.Mapper, e.Ref, e.Identity(), time.Now()); err != nil && !errors.Is(err, spool.ErrAlreadyGone) {
			h.rep.Warnf("runner: could not finish the delivery of %s: %v", e.Ref, err)
			h.spoolDeliveryCount.Failed.Add(1)
		}
		return
	}
	msg, err := coord.MailFromSpool(e, e.Message.FromHarp)
	if err != nil {
		h.failSpoolEntry(e, "refusing an undeliverable spool message", err)
		return
	}
	wire, err := coord.DeliverableStructured(msg.Structured)
	if err != nil {
		h.failSpoolEntry(e, fmt.Sprintf("cannot project spool message %s's payload onto the delivery seam", e.Ref), err)
		return
	}
	msg.Structured = wire
	pm, err := coordgrpc.PeerMessageToWire(msg)
	if err != nil {
		h.failSpoolEntry(e, fmt.Sprintf("cannot project spool message %s onto the delivery seam", e.Ref), err)
		return
	}
	h.rememberSpoolRef(msg.ID, e.Ref)
	h.spoolDeliveryCount.Delivered.Add(1)
	h.deliverNotice(pm)
}

// failSpoolEntry is the terminal outcome for an in/ entry this reader parsed
// as a message but could not classify or project onto the delivery seam: an
// unknown or future mailbox kind, or a structured payload that will not
// decode above the parse layer. A bare continue here — the defect this
// function replaces — left the file in in/ to be re-read, re-warned about,
// and re-skipped on every sweep forever, while later entries in the same
// directory kept delivering around it: exactly the silent-skip this project
// treats as its characteristic defect.
//
// Instead the file is moved OUT of in/ into the local in/failed/ terminal
// directory (spool.Fail): present on disk, unreadable, and never swept
// again — a state an operator can tell apart from "never arrived" (nothing
// in any directory) and from "delivered" (its identity in in/delivered/),
// which is the three-way distinction a bare warning-and-retry cannot make.
func (h *Home) failSpoolEntry(e spool.Entry, why string, cause error) {
	h.spoolDeliveryCount.Failed.Add(1)
	coord.FailSpool(h.rep, h.cfg.Mapper, "runner", e.Ref, why, cause)
}

// rememberSpoolRef records which file a delivered id came from, so the
// delivery ack can find it at the acknowledgement moment.
func (h *Home) rememberSpoolRef(id string, ref spool.Ref) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.spoolRefs == nil {
		h.spoolRefs = map[string]spool.Ref{}
	}
	h.spoolRefs[id] = ref
}

// takeSpoolRef claims the file behind id exactly once.
func (h *Home) takeSpoolRef(id string) (spool.Ref, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, ok := h.spoolRefs[id]
	if ok {
		delete(h.spoolRefs, id)
	}
	return ref, ok
}

// ackMailConsumed is the ONE acknowledgement point for delivered mail: the
// delivered record and delete (spool.Deliver) plus the doorbell that
// announces it. The ack timing (after
// the engine accepted the turn) is a property of the CALLER, and it is the
// property that keeps at-least-once true.
func (h *Home) ackMailConsumed(ids []string) {
	if h.exited.Load() {
		// The run is over (Crash / Close / the engine's exit): the file is
		// the next incarnation's to consume, and a rename now would create
		// a directory under a root this run is done with.
		return
	}
	for _, id := range ids {
		ref, ok := h.takeSpoolRef(id)
		if !ok {
			// Every delivery is a file, so an id with no file behind it is a
			// delivery this reader never made. Said loudly rather than
			// swallowed: an ack that matches nothing is a bookkeeping fault,
			// not a no-op.
			h.rep.Warnf("runner: asked to acknowledge message %s, which no spool file delivered", id)
			h.spoolDeliveryCount.Failed.Add(1)
			continue
		}
		if err := spool.Deliver(h.cfg.Mapper, ref, id, time.Now()); err != nil {
			if errors.Is(err, spool.ErrAlreadyGone) {
				continue
			}
			h.rep.Warnf("runner: delivered %s but could not record it as delivered: %v (it will be delivered again)", ref, err)
			h.spoolDeliveryCount.Failed.Add(1)
			continue
		}
		h.spoolDeliveryCount.Consumed.Add(1)
		// The record-and-delete IS the delivery ack, and this ring is how the
		// coordinator learns of it without polling. It names the file that
		// was delivered and is now gone: a doorbell is only a wake.
		h.outboundCourier().Announce("", ref, "consumed")
	}
}

// sendPeerViaSpool is agent_send: a LOCAL, durable file write into this
// run's out/ plus a doorbell, with no coordinator round trip at all.
// handled=false leaves a non-PeerSend request to the ordinary plane-2 path.
//
// Validation is coord.SendRequest.Validate, the same check the coordinator's
// Send verb runs, so the refusals an agent can be told about synchronously
// (no recipient, no body, a kind outside the sender vocabulary) are given
// here, before anything is written. A reply (InReplyTo set) is exempt from
// the kind check because this path has no coordinator round trip and cannot
// ask whether the reply correlates to a pending approval or ask — that state
// lives coordinator-side. A reply that turns out to correlate to nothing is
// still refused, one hop later: the coordinator's sweep (routeSpoolOut's
// peerSend call) refuses it and mails the refusal back (replySpoolRefusal).
func (h *Home) sendPeerViaSpool(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, bool) {
	send := req.GetPeerSend()
	if send == nil {
		return nil, false
	}
	if h.Harp() == "" {
		return spoolSendErr(codes.FailedPrecondition, "agent_send: "+ErrIdentityUnbound.Error()), true
	}
	sr, err := coordgrpc.SendRequestFromWire(send)
	if err != nil {
		return spoolSendErr(codes.InvalidArgument, err.Error()), true
	}
	// THE validation site — the same Validate the coordinator's Send verb
	// runs, so a send refused here is refused for the reason the wire would
	// have given, and a handler never re-checks a field.
	if err := sr.Validate(); err != nil {
		return spoolSendErr(codes.InvalidArgument, err.Error()), true
	}
	ref, err := h.writeOutbound(coord.Message{
		From: h.Harp(), To: sr.To, Kind: sr.Kind,
		Body: sr.Body, Structured: sr.Structured, InReplyTo: sr.InReplyTo,
	})
	if err != nil {
		h.spoolDeliveryCount.Failed.Add(1)
		return spoolSendErr(codes.Internal, fmt.Sprintf("agent_send: %v", err)), true
	}
	h.spoolDeliveryCount.Delivered.Add(1)
	// NO DOUBLE DELIVERY (spoolturnresult.go): this run has now reported to its
	// parent in its own words, so the automatic turn report must not repeat the
	// same turn. Marked HERE, at the one place an accepted send exists, which
	// is the runner-side twin of the coordinator's noteChildReported.
	h.noteSelfReported()
	return &agentcoordpb.CoordinatorResponse{
		RequestId: req.GetRequestId(),
		Status:    coordgrpc.OKStatus("written to this session's outbound spool"),
		Kind: &agentcoordpb.CoordinatorResponse_PeerSend{PeerSend: &agentcoordpb.PeerSendResult{
			// The FILENAME STEM is the message id, because the file is the
			// message: there is no coordinator-minted id to quote, and an id
			// the recipient could not resolve back to a file would make every
			// in_reply_to written against it dangling.
			MessageId: strings.TrimSuffix(ref.Name, spool.MessageFileExt),
			Delivery:  agentcoordpb.PeerSendResult_DELIVERY_QUEUED,
		}},
	}, true
}

// spoolSendErr is the runner-local agent_send's refusal, shaped as the
// plane-2 answer the tool reads.
func spoolSendErr(code codes.Code, msg string) *agentcoordpb.CoordinatorResponse {
	return &agentcoordpb.CoordinatorResponse{Status: coordgrpc.StatusErr(code, msg)}
}
