package coord

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// spoolCourier pairs a spool WRITE with its DOORBELL so that neither can be
// performed without the other.
//
// THE STANDARD IT ENFORCES: file is truth, wire is doorbell. A message's
// payload goes to a file; the wire carries only the notice that the file
// exists. A lost doorbell therefore costs LATENCY — one sweep interval — and
// never a message. That promise holds only because the write is durable before
// the ring is attempted, which is the ordering this type exists to make
// unbreakable.
//
// WHY A TYPE RATHER THAN A CONVENTION. Both ends of this system wrote a spool
// file and then SEPARATELY remembered to ring, at six sites. Nothing tied a
// ring to a write, so "wrote the file and notified nobody" was expressible
// everywhere — and that is exactly the shape of the delivery failures that cost
// this project a long investigation: a message could be made durable and handed
// to no one, because making-durable and notifying were two steps a caller could
// do one of. Guarding each site would have been another convention. Removing
// the ability to do half of it is not.
//
// The two ends differ in only four things, which is what the fields carry:
// which writer set they own, which harp keys that writer, how they ring, and
// whether the send is audited. Everything else was identical prose in two
// files.
type spoolCourier struct {
	// writers is this end's writer cache. Writers are per-harp and cached
	// because two writers for one directory can mint the same filename twice.
	writers *spoolWriterCache
	// keyFor maps a recipient onto the harp whose spool is written. The
	// coordinator writes into the RECIPIENT's spool; a runner always writes
	// into its OWN, whoever the message is addressed to.
	keyFor func(to string) string
	// ring delivers the doorbell. It reports its own drops (WarnOnce + a
	// counter) and returns nil for a drop: a doorbell that cannot go out is
	// not an error, because the file is already truth.
	ring func(to string, ref spool.Ref) error
	// onSent records the send where an end audits it. May be nil.
	onSent func(to string, msg Message, ref spool.Ref)
	// side names this end in diagnostics ("coordinator" / "runner").
	side string
}

// Send writes msg into the spool and rings its doorbell, in that order.
//
// A ring that fails does NOT fail the send, and that is the contract rather
// than leniency: the file is durable by then and the recipient's sweep is the
// at-least-once floor, so the only cost is one sweep interval. A WRITE that
// fails DOES fail, because nothing was delivered and nobody has been told
// otherwise.
func (x *spoolCourier) Send(msg Message) (spool.Ref, error) {
	sm, err := spoolMessageForMail(msg, msg.To)
	if err != nil {
		return spool.Ref{}, fmt.Errorf("%s: cannot project message %s for %s onto the spool: %w", x.side, msg.ID, msg.To, err)
	}
	ref, err := x.SendProjected(msg.To, sm)
	if err != nil {
		return spool.Ref{}, err
	}
	if x.onSent != nil {
		x.onSent(msg.To, msg, ref)
	}
	return ref, nil
}

// SendProjected writes an ALREADY-PROJECTED spool message and rings it.
//
// It exists for callers that build their own spool.Message and own their own
// failure reporting — the shadow tee does both, with its own counters. They
// still must not be able to write without ringing, so the pairing lives here
// and they compose on top rather than reaching past it.
func (x *spoolCourier) SendProjected(to string, sm *spool.Message) (spool.Ref, error) {
	w, err := x.writers.writerFor(x.keyFor(to))
	if err != nil {
		return spool.Ref{}, fmt.Errorf("%s: cannot open the spool for %s: %w", x.side, to, err)
	}
	ref, err := w.Write(sm)
	if err != nil {
		return spool.Ref{}, fmt.Errorf("%s: writing into %s's spool: %w", x.side, to, err)
	}
	// Unconditional, and unreachable from outside: a caller holding a courier
	// cannot obtain the ref without this having run.
	if rerr := x.ring(to, ref); rerr != nil {
		clidiag.Warn("ctxloom", "%s: wrote %s for %s but could not ring it: %v (it will be swept)", x.side, ref, to, rerr)
	}
	return ref, nil
}

// Announce rings for a spool mutation this end has ALREADY performed — a
// rename rather than a write.
//
// The consume-rename IS the delivery acknowledgement, and a withdrawal moves a
// file out of the directory the runner sweeps; in both cases the bytes have
// already moved and the doorbell is how the other end learns of it without
// polling. So the invariant the courier owns is not "a write is rung" but the
// general one: A SPOOL MUTATION IS ANNOUNCED, whether it wrote a file or moved
// one.
//
// what names the transition for the diagnostic, so a dropped announcement says
// which one was lost rather than only that something was.
func (x *spoolCourier) Announce(to string, ref spool.Ref, what string) {
	if rerr := x.ring(to, ref); rerr != nil {
		clidiag.Warn("ctxloom", "%s: %s %s but could not announce it: %v", x.side, what, ref, rerr)
	}
}
