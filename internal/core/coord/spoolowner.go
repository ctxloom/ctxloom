package coord

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// THE OWNER-SIDE SPOOL READER: the session owner's in/ spool, drained by the
// process that IS the owner's inbox.
//
// The owner is the one recipient with no runner. A migrated child's spool is
// read by a Home on the far side of a run channel; the owner's is read by
// AgentRecv, here, in the coordinator's own process. Everything recvMail
// already does — park one long-poll per role, wake it on arrival, settle an
// arrival burst, ack a delivery on the NEXT receive, preempt an older poll
// with a newer one — is substrate-agnostic and is kept as is. Only the two
// operations that touch the store are swapped for the owner:
//
//   - CLAIM (tryClaimDeliverable -> claimSpoolInbox): read in/, hand back
//     what is not already reserved, and reserve it in the same runtime ledger
//     (Coordinator.delivered) the mailbox uses, remembering which FILE each
//     id came from.
//   - ACK (ackDelivered -> ackSpoolInbox): consume-rename every file behind
//     the reserved ids into in/consumed/, then release the reservation.
//
// The rename is the ack, and it happens one receive LATE on purpose: renaming
// at claim time would turn at-least-once into at-most-once for a harness that
// stopped listening between the wake and the return. A crash between the two
// leaves the file in in/, where the next coordinator's first receive finds it
// again under the same id — which is the durable half of at-least-once, and
// the reason the id is the file's origin id rather than anything minted here.
//
// There is no reactor for the owner. A doorbell for the owner is delivered
// in-process (ringSpool) as a bare wake to the parked poll, and the receive
// that wakes reads the directory itself; a receive that finds no parked poll
// to wake changes nothing, because every receive begins with a read. The
// periodic reconciliation sweep that guards a runner against a dropped wire
// doorbell has nothing to guard against here.

// claimSpoolInbox is tryClaimDeliverable for the owner: it reads role's in/
// and reserves and returns every message not already reserved. ok=false means
// nothing new is deliverable right now.
//
// A file that parses as a spool entry but not as a mailbox message (an
// unknown kind, a structured payload that will not decode) is moved to
// in/failed/ rather than left to be re-read and re-warned about on every
// receive for the life of the process — the same terminal state the runner's
// reader gives such a file, for the same reason.
func (c *Coordinator) claimSpoolInbox(role string) ([]Message, bool) {
	res, ok := c.sweepSpoolDir(role, spool.DirIn, "draining the session owner's inbox")
	if !ok {
		return nil, false
	}
	for _, p := range res.Problems {
		c.rep.Warnf("coordinator: a file in the owner's in/ spool is not a message and will not be delivered: %v", p.Error())
		c.spoolDeliveryCount.failed.Add(1)
	}
	type unreadable struct {
		entry spool.Entry
		err   error
	}
	var (
		out    []Message
		failed []unreadable
	)
	c.mu.Lock()
	reserved := make(map[string]bool, len(c.delivered[role]))
	for _, id := range c.delivered[role] {
		reserved[id] = true
	}
	for _, e := range res.Entries {
		msg, err := mailFromSpool(e, e.Message.FromHarp)
		if err != nil {
			failed = append(failed, unreadable{e, err})
			continue
		}
		if reserved[msg.ID] {
			continue // delivered by an earlier receive, awaiting this one's ack
		}
		reserved[msg.ID] = true
		c.delivered[role] = append(c.delivered[role], msg.ID)
		if c.spoolRefs == nil {
			c.spoolRefs = map[string]spool.Ref{}
		}
		c.spoolRefs[msg.ID] = e.Ref
		out = append(out, msg)
	}
	c.mu.Unlock()
	for _, f := range failed {
		c.spoolDeliveryCount.failed.Add(1)
		failSpool(c.rep, c.mapper, "coordinator", f.entry.Ref, "refusing an undeliverable message in the owner's in/ spool", f.err)
	}
	if len(out) == 0 {
		return nil, false
	}
	c.spoolDeliveryCount.delivered.Add(uint64(len(out)))
	return out, true
}

// ackSpoolInbox is ackDelivered for the owner: every id a prior receive
// reserved is consumed by renaming its file into in/consumed/, and the
// reservation is released.
//
// A rename that fails for any reason other than the file already being gone
// leaves the file in in/ and releases the reservation anyway: the next claim
// delivers it again, which is a duplicate the reader dedupes on id, and the
// alternative — a reservation nothing ever clears — is a message that is
// permanently invisible.
func (c *Coordinator) ackSpoolInbox(role string) {
	c.mu.Lock()
	ids := append([]string(nil), c.delivered[role]...)
	refs := make(map[string]spool.Ref, len(ids))
	for _, id := range ids {
		if ref, ok := c.spoolRefs[id]; ok {
			refs[id] = ref
			delete(c.spoolRefs, id)
		}
	}
	c.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	consumed := uint64(0)
	for id, ref := range refs {
		if _, err := spool.Consume(c.mapper, ref); err != nil {
			if errors.Is(err, spool.ErrAlreadyGone) {
				continue
			}
			c.rep.Warnf("coordinator: the owner received %s but it could not be marked consumed: %v (it will be delivered again on the next receive)", id, err)
			c.spoolDeliveryCount.failed.Add(1)
			continue
		}
		consumed++
	}
	c.unreserve(role, ids)
	c.spoolDeliveryCount.consumed.Add(consumed)
}
