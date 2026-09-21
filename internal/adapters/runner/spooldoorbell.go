package runner

import (
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// RingSpool sends the coordinator a doorbell for ref. FIRE-AND-FORGET on the
// same terms as the coordinator's: a down or absent stream drops it and
// returns nil; only an unusable ref is an error.
func (h *Home) RingSpool(ref spool.Ref) error {
	msg, err := coord.SpoolChangedProto(ref)
	if err != nil {
		return err
	}
	frame := &agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_SpoolChanged{SpoolChanged: msg}}
	if !h.trySend(frame) {
		// Report, then count — see Coordinator.noteSpoolDrop.
		h.rep.WarnOncef("runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", ref)
		h.spoolDoorbell.Dropped.Add(1)
	}
	return nil
}

// SetSpoolDoorbellHandler registers THE runner-side consumer for validated
// inbound doorbells; nil deregisters. startSpoolReactor registers SweepSpoolIn
// here, so the doorbell reaches the same single funnel as every other trigger.
//
// The handler takes the coordinator's role name (always the empty string
// today: the coordinator is the only peer on this channel) so that both sides
// register the same signature.
func (h *Home) SetSpoolDoorbellHandler(fn coord.SpoolDoorbellHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spoolHandler = fn
}

// handleSpoolChanged is the runner's receive chokepoint. Unlike the
// coordinator's it does not substitute an identity: this channel has exactly
// one peer, and the harp a coordinator names is the harp whose spool it wrote
// into. Validation is identical, and just as unconditional.
func (h *Home) handleSpoolChanged(msg *agentcoordpb.SpoolChanged) {
	ref, err := coord.SpoolRefFromProto(msg)
	if err != nil {
		h.rep.Warnf("runner: refusing an invalid spool doorbell from the coordinator: %v", err)
		h.spoolDoorbell.Rejected.Add(1)
		return
	}
	// INTERIOR-CLAIM DISCIPLINE, runner side. A runner has exactly one spool,
	// and a doorbell naming any other harp is refused rather than followed: the
	// ref arrives from a peer, and a runner that swept whatever spool it was
	// pointed at would read a sibling session's mail across the one boundary
	// the per-session mount exists to draw.
	switch {
	case ref.Harp != h.Harp():
		h.rep.Warnf("runner: refusing a spool doorbell for %q; this run's spool is %q", ref.Harp, h.Harp())
		h.spoolDoorbell.Rejected.Add(1)
		return
	case ref.Dir == spool.DirInWithdrawn:
		// ACCEPTED, and consumed below by the one seam.
		// A RETRACTION (spoolcontrol.go's WithdrawSteer): the coordinator
		// renamed an unread instruction out of in/ and is announcing the
		// transition. There is nothing to deliver — the file has already
		// left the directory this runner sweeps — and nothing to refuse
		// either: a sweep re-derives the picture and finds it gone, which
		// is exactly the outcome. Counting it as a rejection would make
		// every successful withdrawal read as a doorbell fault.
	case ref.Dir != spool.DirIn:
		// out/ and the remaining terminal directories are this runner's
		// own writes coming back at it; nothing to read there.
		h.rep.Warnf("runner: ignoring a spool doorbell for %s: only inbound mail is delivered to this run", ref.Dir)
		h.spoolDoorbell.Rejected.Add(1)
		return
	}
	h.mu.Lock()
	fn := h.spoolHandler
	h.mu.Unlock()
	if fn == nil {
		h.spoolDoorbell.Dropped.Add(1)
		return
	}
	fn("", ref)
}

// SpoolDoorbellStats reports this runner's cumulative doorbell drops and
// rejections.
func (h *Home) SpoolDoorbellStats() coord.SpoolDoorbellStats { return h.spoolDoorbell.Stats() }
