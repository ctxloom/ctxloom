package coord

import (
	"fmt"
	"sync/atomic"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The spool doorbell: the wire half of the file-spool substrate.
//
// A doorbell says "a file now exists at this logical spool coordinate; look".
// It carries a spool.Ref and NOTHING else — the file's own YAML frontmatter is
// the control plane, and a doorbell carrying payload would recreate the
// two-carrier desync the spool exists to kill.
//
// Three properties hold everywhere in this file, and each is a deliberate
// contrast with the mail push (pushMail) the doorbell will eventually replace:
//
//   - FIRE-AND-FORGET. A doorbell that cannot be sent right now — no channel,
//     saturated send pump, no stream — is DROPPED, with zero rollback
//     bookkeeping. pushMail must roll its delivery reservation back because the
//     wire was the message's only carrier; here the FILE is the truth and the
//     receiver's sweep is the at-least-once floor, so a dropped doorbell costs
//     latency and never a message. Dropping is COUNTED and logged, though:
//     silent-invisible is how a systematic sender bug reads as "the system is
//     just a bit slow" forever.
//   - VALIDATED AT THE RECEIVE CHOKEPOINT. Every field arrives from a
//     less-trusted peer, so an inbound doorbell passes spool.Ref.Validate
//     before anything resolves it to a path. An invalid ref is refused and
//     counted, and the consumer hook never sees it.
//   - IDENTITY COMES FROM THE CHANNEL, NEVER THE CLAIM (coordinator side).
//     Refs arriving on a child's channel name THAT child's spool whatever harp
//     the frame claims, exactly as inbound mail's sender identity comes from
//     the connection credential and not the message's `from`. Without that, a
//     child could aim the coordinator's reader at a sibling's spool.
//
// Conversion lives HERE rather than beside the generated types because the
// spool package's layering forbids the other direction: spool depends on
// internal/paths and internal/shared/harp only, and coord depends on IT. This
// package is the one place that already knows both vocabularies.

// SpoolDoorbellStats reports what the doorbell deliberately did not retry.
// Both counters are cumulative for the process's lifetime.
type SpoolDoorbellStats struct {
	// Dropped counts outbound doorbells that could not be sent at the moment
	// they were rung (no channel, saturated pump, no stream). Each one costs
	// latency until a sweep, never a message.
	Dropped uint64
	// Rejected counts inbound doorbells refused at the receive chokepoint —
	// a ref that did not validate. These are faults, not races: a doorbell
	// naming a file that no longer exists is a normal outcome resolved by the
	// reader, while a ref that does not even parse means a broken or hostile
	// sender.
	Rejected uint64
}

// spoolDoorbellCounters is the shared counter pair, embedded by both ends.
type spoolDoorbellCounters struct {
	dropped  atomic.Uint64
	rejected atomic.Uint64
}

func (s *spoolDoorbellCounters) stats() SpoolDoorbellStats {
	return SpoolDoorbellStats{Dropped: s.dropped.Load(), Rejected: s.rejected.Load()}
}

// spoolDirToWire is the ONE table mapping the closed spool.Dir set onto the
// closed wire enum. It is a map rather than a switch so that the inverse can be
// derived from it (below) instead of hand-written — two hand-kept tables are
// two chances to disagree, and a disagreement here silently routes a message
// into the wrong directory.
var spoolDirToWire = map[spool.Dir]agentcoordpb.SpoolDir{
	spool.DirIn:          agentcoordpb.SpoolDir_SPOOL_DIR_IN,
	spool.DirOut:         agentcoordpb.SpoolDir_SPOOL_DIR_OUT,
	spool.DirInConsumed:  agentcoordpb.SpoolDir_SPOOL_DIR_IN_CONSUMED,
	spool.DirOutConsumed: agentcoordpb.SpoolDir_SPOOL_DIR_OUT_CONSUMED,
	spool.DirInWithdrawn: agentcoordpb.SpoolDir_SPOOL_DIR_IN_WITHDRAWN,
}

// spoolDirFromWire inverts spoolDirToWire, built once at init so the two
// directions cannot drift. A duplicate on the wire side would collapse two
// spool directories into one and is a build-time panic: it is a table typo,
// not a runtime condition, and the first symptom in production would be mail
// consumed out of the wrong directory.
var spoolDirFromWire = func() map[agentcoordpb.SpoolDir]spool.Dir {
	inv := make(map[agentcoordpb.SpoolDir]spool.Dir, len(spoolDirToWire))
	for d, w := range spoolDirToWire {
		if prev, dup := inv[w]; dup {
			panic(fmt.Sprintf("coord: spool dir table maps %q and %q onto the same wire value %s", prev, d, w))
		}
		inv[w] = d
	}
	return inv
}()

// SpoolDirToWire projects a spool directory onto the wire enum. An unknown
// directory is an error, never SPOOL_DIR_UNSPECIFIED: the zero value is
// invalid at every consumer, so encoding an unmappable directory as it would
// turn a sender-side table gap into a receiver-side rejection far from the
// cause.
func SpoolDirToWire(d spool.Dir) (agentcoordpb.SpoolDir, error) {
	w, ok := spoolDirToWire[d]
	if !ok {
		return agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED,
			fmt.Errorf("coord: spool directory %q has no wire representation", string(d))
	}
	return w, nil
}

// SpoolDirFromWire resolves a wire enum value to a spool directory.
// SPOOL_DIR_UNSPECIFIED and any value this build does not know are errors —
// forward compatibility for a doorbell means the receiver refuses it loudly,
// not that it guesses a directory.
func SpoolDirFromWire(w agentcoordpb.SpoolDir) (spool.Dir, error) {
	d, ok := spoolDirFromWire[w]
	if !ok {
		return "", fmt.Errorf("coord: unknown wire spool directory %s (%d)", w, int32(w))
	}
	return d, nil
}

// SpoolChangedProto projects a Ref onto the doorbell frame. The ref is
// validated first: a sender that rings about a name it could not itself
// resolve is a bug worth failing at the writer, where the stack still says who
// did it.
func SpoolChangedProto(ref spool.Ref) (*agentcoordpb.SpoolChanged, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	dir, err := SpoolDirToWire(ref.Dir)
	if err != nil {
		return nil, err
	}
	return &agentcoordpb.SpoolChanged{Harp: ref.Harp, Dir: dir, Name: ref.Name}, nil
}

// SpoolRefFromProto is THE receive chokepoint: it turns a wire doorbell into a
// Ref, or refuses it. Everything it accepts has passed spool.Ref.Validate —
// harp grammar, closed directory, bare-filename grammar — so no caller
// downstream has to re-check before resolving a path, and no unvalidated ref
// can reach a path join.
func SpoolRefFromProto(msg *agentcoordpb.SpoolChanged) (spool.Ref, error) {
	if msg == nil {
		return spool.Ref{}, fmt.Errorf("coord: spool doorbell carried no reference")
	}
	dir, err := SpoolDirFromWire(msg.GetDir())
	if err != nil {
		return spool.Ref{}, err
	}
	ref := spool.Ref{Harp: msg.GetHarp(), Dir: dir, Name: msg.GetName()}
	if err := ref.Validate(); err != nil {
		return spool.Ref{}, err
	}
	return ref, nil
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
	msg, err := SpoolChangedProto(ref)
	if err != nil {
		return err
	}
	frame := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Notice{
		Notice: &agentcoordpb.CoordinatorNotice{Kind: &agentcoordpb.CoordinatorNotice_SpoolChanged{
			SpoolChanged: msg,
		}},
	}}

	c.mu.Lock()
	ch := c.chans[role]
	c.mu.Unlock()
	if ch == nil {
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
	clidiag.WarnOnce("ctxloom", "coordinator: spool doorbell for %s dropped (%s); %s is still on disk and will be delivered by the next sweep",
		role, why, ref)
	c.spoolDoorbell.dropped.Add(1)
}

// SetSpoolDoorbellHandler registers the consumer for validated inbound
// doorbells. Passing nil deregisters — and nil is the DEFAULT: until something
// wires delivery, an arriving doorbell is validated, counted and dropped.
func (c *Coordinator) SetSpoolDoorbellHandler(fn SpoolDoorbellHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spoolHandler = fn
}

// handleSpoolChanged is the coordinator's receive chokepoint for a doorbell
// arriving on ch.
//
// The claimed harp is validated (a ref that does not parse is a fault worth
// refusing whole) and then IGNORED in favour of ch.role. That substitution is
// the isolation fence: the coordinator watches a channel's own spool, never a
// spool the peer on that channel names.
func (c *Coordinator) handleSpoolChanged(ch *runChan, msg *agentcoordpb.SpoolChanged) {
	ref, err := SpoolRefFromProto(msg)
	if err != nil {
		// Report, then count — see noteSpoolDrop.
		clidiag.Warn("ctxloom", "coordinator: refusing an invalid spool doorbell from %s: %v", ch.role, err)
		c.spoolDoorbell.rejected.Add(1)
		return
	}
	if ref.Harp != ch.role {
		// Not fatal — the ref is simply re-aimed at the channel's own spool —
		// but a runner that names someone else's harp is either broken or
		// probing, and both deserve a trace.
		clidiag.Warn("ctxloom", "coordinator: spool doorbell from %s claimed harp %q; resolving against %s's own spool instead",
			ch.role, ref.Harp, ch.role)
		ref.Harp = ch.role
	}
	// THE CUTOVER's wake. A doorbell means "look at that spool", never
	// "process exactly that file": the reactor re-derives the whole picture by
	// sweeping, which is what makes a lost or duplicated ring harmless.
	c.spoolReactor.mark(ch.role)
	c.mu.Lock()
	fn := c.spoolHandler
	c.mu.Unlock()
	if fn == nil {
		// Not a drop under the cutover — the reactor above IS the consumer.
		if !c.spoolDelivery {
			c.spoolDoorbell.dropped.Add(1)
		}
		return
	}
	fn(ch.role, ref)
}

// SpoolDoorbellStats reports this coordinator's cumulative doorbell drops and
// rejections.
func (c *Coordinator) SpoolDoorbellStats() SpoolDoorbellStats { return c.spoolDoorbell.stats() }

// ---- runner side -------------------------------------------------------

// ringSpool sends the coordinator a doorbell for ref. FIRE-AND-FORGET on the
// same terms as the coordinator's: a down or absent stream drops it and
// returns nil; only an unusable ref is an error.
func (h *Home) ringSpool(ref spool.Ref) error {
	msg, err := SpoolChangedProto(ref)
	if err != nil {
		return err
	}
	frame := &agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_SpoolChanged{SpoolChanged: msg}}
	if !h.trySend(frame) {
		// Report, then count — see Coordinator.noteSpoolDrop.
		clidiag.WarnOnce("ctxloom", "runner: spool doorbell dropped (run channel down); %s is still on disk and will be delivered by the next sweep", ref)
		h.spoolDoorbell.dropped.Add(1)
	}
	return nil
}

// SetSpoolDoorbellHandler registers the runner-side consumer for validated
// inbound doorbells. Nil deregisters, and nil is the default.
//
// The handler takes the coordinator's role name (always the empty string
// today: the coordinator is the only peer on this channel) so that both sides
// register the same signature.
func (h *Home) SetSpoolDoorbellHandler(fn SpoolDoorbellHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spoolHandler = fn
}

// handleSpoolChanged is the runner's receive chokepoint. Unlike the
// coordinator's it does not substitute an identity: this channel has exactly
// one peer, and the harp a coordinator names is the harp whose spool it wrote
// into. Validation is identical, and just as unconditional.
func (h *Home) handleSpoolChanged(msg *agentcoordpb.SpoolChanged) {
	ref, err := SpoolRefFromProto(msg)
	if err != nil {
		clidiag.Warn("ctxloom", "runner: refusing an invalid spool doorbell from the coordinator: %v", err)
		h.spoolDoorbell.rejected.Add(1)
		return
	}
	// INTERIOR-CLAIM DISCIPLINE, runner side. A runner has exactly one spool,
	// and a doorbell naming any other harp is refused rather than followed: the
	// ref arrives from a peer, and a runner that swept whatever spool it was
	// pointed at would read a sibling session's mail across the one boundary
	// the per-session mount exists to draw.
	if h.spoolDelivery {
		switch {
		case ref.Harp != h.cfg.Harp:
			clidiag.Warn("ctxloom", "runner: refusing a spool doorbell for %q; this run's spool is %q", ref.Harp, h.cfg.Harp)
			h.spoolDoorbell.rejected.Add(1)
			return
		case ref.Dir == spool.DirInWithdrawn:
			// A RETRACTION (spoolcontrol.go's WithdrawSteer): the coordinator
			// renamed an unread instruction out of in/ and is announcing the
			// transition. There is nothing to deliver — the file has already
			// left the directory this runner sweeps — and nothing to refuse
			// either: a sweep re-derives the picture and finds it gone, which
			// is exactly the outcome. Counting it as a rejection would make
			// every successful withdrawal read as a doorbell fault.
			h.SweepSpoolIn()
		case ref.Dir != spool.DirIn:
			// out/ and the remaining terminal directories are this runner's
			// own writes coming back at it; nothing to read there.
			clidiag.Warn("ctxloom", "runner: ignoring a spool doorbell for %s: only inbound mail is delivered to this run", ref.Dir)
			h.spoolDoorbell.rejected.Add(1)
			return
		default:
			h.SweepSpoolIn()
		}
	}
	h.mu.Lock()
	fn := h.spoolHandler
	h.mu.Unlock()
	if fn == nil {
		if !h.spoolDelivery {
			h.spoolDoorbell.dropped.Add(1)
		}
		return
	}
	fn("", ref)
}

// SpoolDoorbellStats reports this runner's cumulative doorbell drops and
// rejections.
func (h *Home) SpoolDoorbellStats() SpoolDoorbellStats { return h.spoolDoorbell.stats() }
