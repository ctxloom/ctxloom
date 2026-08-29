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
	w, err := x.writers.writerFor(x.keyFor(msg.To))
	if err != nil {
		return spool.Ref{}, fmt.Errorf("%s: cannot open the spool for message %s to %s: %w", x.side, msg.ID, msg.To, err)
	}
	ref, err := w.Write(sm)
	if err != nil {
		return spool.Ref{}, fmt.Errorf("%s: writing message %s into %s's spool: %w", x.side, msg.ID, msg.To, err)
	}
	if x.onSent != nil {
		x.onSent(msg.To, msg, ref)
	}
	// Unconditional, and unreachable from outside: a caller holding a courier
	// cannot obtain the ref without this having run.
	if rerr := x.ring(msg.To, ref); rerr != nil {
		clidiag.Warn("ctxloom", "%s: wrote %s for %s but could not ring it: %v (it will be swept)", x.side, ref, msg.To, rerr)
	}
	return ref, nil
}
