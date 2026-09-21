package coord

import (
	"sync/atomic"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The spool doorbell: the wire half of the file-spool substrate.
//
// A doorbell says "a file now exists at this logical spool coordinate; look".
// It carries a spool.Ref and NOTHING else — the file's own YAML frontmatter is
// the control plane, and a doorbell carrying payload would recreate the
// two-carrier desync the spool exists to kill.
//
// Three properties hold everywhere in this file:
//
//   - FIRE-AND-FORGET. A doorbell that cannot be sent right now — no channel,
//     saturated send pump, no stream — is DROPPED, with zero rollback
//     bookkeeping: the FILE is the truth and the receiver's sweep is the
//     at-least-once floor, so a dropped doorbell costs latency and never a
//     message. Dropping is COUNTED and logged, though:
//     silent-invisible is how a systematic sender bug reads as "the system is
//     just a bit slow" forever.
//   - VALIDATED AT THE RECEIVE CHOKEPOINT. Every field arrives from a
//     less-trusted peer, so an inbound doorbell passes spool.Ref.Validate
//     before anything resolves it to a path. An invalid ref is refused and
//     counted, and the consumer hook never sees it.
//   - IDENTITY COMES FROM THE CHANNEL, NEVER THE CLAIM. A ref arriving on a
//     child's channel may only name THAT child's spool; one naming any other
//     harp is refused and counted, on both sides. The coordinator does not
//     re-aim such a ref at the channel's own spool: the spool contract is
//     "rejected, never sanitised", and a rewritten field would leave a peer
//     probing for sibling harps indistinguishable in the counters from a
//     quiet one. Refusing costs nothing — the sweep is the delivery floor.
//
// Conversion lives HERE rather than beside the generated types because the
// spool package's layering forbids the other direction: spool depends on
// internal/core/paths and internal/shared/harp only, and coord depends on IT. This
// package is the one place that already knows both vocabularies.

// SpoolDoorbellStats reports what the doorbell deliberately did not retry.
// Both counters are cumulative for the process's lifetime.
type SpoolDoorbellStats struct {
	// Dropped counts outbound doorbells that could not be sent at the moment
	// they were rung (no channel, saturated pump, no stream). Each one costs
	// latency until a sweep, never a message.
	Dropped uint64
	// Rejected counts inbound doorbells refused at the receive chokepoint —
	// a ref that did not validate, or one naming a spool the sending channel
	// does not own. These are faults, not races: a doorbell naming a file
	// that no longer exists is a normal outcome resolved by the reader, while
	// a ref that does not even parse, or that names a sibling's harp, means a
	// broken or hostile sender.
	Rejected uint64
}

// SpoolDoorbellCounters is the shared counter pair, embedded by both ends.
type SpoolDoorbellCounters struct {
	Dropped  atomic.Uint64
	Rejected atomic.Uint64
}

func (s *SpoolDoorbellCounters) Stats() SpoolDoorbellStats {
	return SpoolDoorbellStats{Dropped: s.Dropped.Load(), Rejected: s.Rejected.Load()}
}

// SpoolDoorbellHandler is what a consumer registers to be told about validated
// spool changes. The coordinator's form carries the ROLE the doorbell arrived
// from, which is the authoritative harp; the runner's form carries the ref
// alone.
//
// Handlers run on the receiving channel's goroutine and must not block: a slow
// handler stalls that stream's whole receive loop. Delivery behaviour belongs
// behind a queue of the consumer's own.
type SpoolDoorbellHandler func(role string, ref spool.Ref)

// ---- coordinator side --------------------------------------------------

// ringSpool sends role's runner a doorbell for ref. FIRE-AND-FORGET: it
// returns nil when the doorbell went out AND when it was dropped for want of a
// live, unsaturated channel. The only error is an unusable ref, which never
// reaches the wire.
//
// There is deliberately no "was it delivered" answer to give a caller. A caller
// that could learn a doorbell was dropped would be tempted to compensate, and
// every compensation mechanism (retry queues, reservations, reissue buffers) is
// exactly the machinery the file-is-the-truth design exists to delete. The
// receiver's sweep already covers it.
func (c *Coordinator) ringSpool(role string, ref spool.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	frame := OutFrame{Notice: &Notice{SpoolChanged: &ref}}

	c.mu.Lock()
	ch := c.chans[role]
	c.mu.Unlock()
	if ch == nil {
		// The owner's reader is this process, so its doorbell is delivered
		// in-process: the file is already on disk (the courier rings only
		// after the write), and the whole job of the bell is to complete a
		// parked agent_recv — which then reads the directory itself. A
		// wake sent to nobody costs nothing; the next receive sweeps anyway.
		// An owner that DOES have a channel (a container-hosted owner run)
		// is rung over the wire like any runner, and never reaches here.
		if c.ownerSpool(role) {
			c.inbox.wake(role)
			return nil
		}
		c.noteSpoolDrop(role, ref, "no live run channel")
		return nil
	}
	select {
	case ch.send <- frame:
	default:
		// Saturated pump. No rollback: nothing was reserved, because nothing
		// about this doorbell is state.
		c.noteSpoolDrop(role, ref, "send pump saturated")
	}
	return nil
}

// noteSpoolDrop reports and then counts, in that order. The counter is what an
// observer polls, so incrementing it LAST is what makes "the count moved"
// imply "the report is already written" rather than "is about to be".
func (c *Coordinator) noteSpoolDrop(role string, ref spool.Ref, why string) {
	c.rep.WarnOncef("coordinator: spool doorbell for %s dropped (%s); %s is still on disk and will be delivered by the next sweep",
		role, why, ref)
	c.spoolDoorbell.Dropped.Add(1)
}

// SetSpoolDoorbellHandler registers THE consumer for validated inbound
// doorbells; passing nil deregisters. startSpoolReactor registers the reactor
// here, so this is the single seam every doorbell is consumed through rather
// than a hook beside a hard-wired path — which is also what lets a test
// observe the ref the wire actually delivered, field for field.
func (c *Coordinator) SetSpoolDoorbellHandler(fn SpoolDoorbellHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spoolHandler = fn
}

// RefuseSpoolChanged counts a doorbell the wire could not decode into a
// valid ref: a ref that does not parse is a fault worth refusing whole.
// Report, then count — see noteSpoolDrop.
func (c *Coordinator) RefuseSpoolChanged(ch *RunChannel, err error) {
	c.rep.Warnf("coordinator: refusing an invalid spool doorbell from %s: %v", ch.role, err)
	c.spoolDoorbell.Rejected.Add(1)
}

// HandleSpoolChanged is the coordinator's receive chokepoint for a validated
// doorbell arriving on ch.
//
// The claimed harp is checked against ch.role. That check is the isolation
// fence: the coordinator watches a channel's own spool, never a spool the
// peer on that channel names — and a ref naming any other harp is REFUSED,
// not re-aimed. The handler therefore only ever sees a ref whose harp is the
// role it arrived from.
func (c *Coordinator) HandleSpoolChanged(ch *RunChannel, ref spool.Ref) {
	if ref.Harp != ch.role {
		// A runner that names someone else's harp is either broken or
		// probing, and both deserve a trace AND a count: a warn line alone
		// leaves a probe reading like ordinary doorbell contention. Nothing
		// is lost by refusing — the file is on disk and the reactor's tick
		// sweeps ch.role's spool regardless.
		c.rep.Warnf("coordinator: refusing a spool doorbell from %s that names %q's spool; %s's own spool is swept regardless",
			ch.role, ref.Harp, ch.role)
		c.spoolDoorbell.Rejected.Add(1)
		return
	}
	// THE WAKE. A doorbell means "look at that spool", never
	// "process exactly that file": the reactor re-derives the whole picture by
	// sweeping, which is what makes a lost or duplicated ring harmless.
	c.mu.Lock()
	fn := c.spoolHandler
	c.mu.Unlock()
	if fn == nil {
		// A doorbell with no consumer IS a drop, and this counter now means
		// only that. Production registers the reactor in startSpoolReactor, so
		// reaching here means delivery is off or registration was missed.
		c.spoolDoorbell.Dropped.Add(1)
		return
	}
	fn(ch.role, ref)
}

// SpoolDoorbellStats reports this coordinator's cumulative doorbell drops and
// rejections.
func (c *Coordinator) SpoolDoorbellStats() SpoolDoorbellStats { return c.spoolDoorbell.Stats() }

// ---- runner side -------------------------------------------------------
