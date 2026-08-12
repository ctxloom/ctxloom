package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// THE MAIL-PLANE CUTOVER (config delegation.spool_delivery): ordinary
// coordinator<->child mail is DELIVERED FROM FILES.
//
// The shape, stated once, because every function below is a consequence of it:
//
//   - THE FILE IS THE MESSAGE. The coordinator writes one spool file into the
//     child's in/ and that is the whole delivery — no mailbox fact, no queued
//     twin, nothing else to keep in step. The child's runner writes out/ for
//     everything it sends. One writer per direction, always.
//   - CONSUMPTION IS A RENAME, and the rename is the ACK. A reader moves the
//     file into consumed/ only after the delivery it made is real (the engine
//     accepted the turn, or a later Recv proved the harness took the batch).
//     Renaming earlier would silently convert at-least-once into at-most-once.
//   - THE DOORBELL IS ONLY A WAKE. It carries a reference and no state, it is
//     dropped freely when the channel is down, and receiving one means "sweep",
//     never "process exactly that file". A doorbell that names a file which is
//     no longer there (the sweep won, or a container mount has not caught up)
//     is ErrAlreadyGone — a race resolved, never an error a user sees.
//   - THE SWEEP IS THE FLOOR, and at startup it is a FIRST-CLASS DELIVERY PATH:
//     a coordinator or runner coming up cold drains its spool before any
//     channel traffic exists to tell it to. Everything else — reconnect, turn
//     boundary, the slow timer — is the same sweep on a different trigger.
//   - SENDER IDENTITY IS THE DIRECTORY. A file in child X's out/ is from X
//     because of where it is, whatever its own from_harp says. Routing that
//     trusted the file's interior claim would let a child aim the coordinator
//     at a sibling's parent.
//
// SCOPE (S5a): ordinary mail only, in both directions. Steer, question,
// summarize, pause/resume, approvals and the up-asks still ride the mailbox
// and the request plane; agent_report stays on the events plane. Mail to the
// session owner's own in-process mailbox, and to a FROZEN legacy go-plugin
// child, also stays on the mailbox — neither has a runner sweeping a spool, so
// a file written for them would sit in a directory nothing ever reads.
//
// FLAG OFF means byte-identical pre-spool behaviour: no branch below is
// entered, no reactor runs, and no directory is created.

// spoolSweepInterval is the slow reconciliation cadence on BOTH sides: the
// backstop for a doorbell dropped on a stream that never went down (the
// saturated-pump case). It is a tunable constant rather than config surface —
// nothing hangs on the exact number, because the startup and reconnect sweeps
// already bound every case where a doorbell could be missed for longer.
const spoolSweepInterval = 30 * time.Second

// spoolReactor serialises one side's spool reading.
//
// Serialisation is not an optimisation, it is the in-process arbiter: a
// doorbell and a timer sweep that ran concurrently could both read the same
// file and both deliver it, and the consume-rename — which resolves that race
// ACROSS processes — would then be adjudicating two deliveries that already
// happened. One reader goroutine per side means the second look finds the file
// already renamed (or already deduped) instead.
//
// It is a set, never a queue: pending roles collapse, because a doorbell says
// "look at this spool", not "process this message", so N doorbells for one
// role are one unit of work.
type spoolReactor struct {
	// sweep does one role's worth of reading. It runs on the reactor
	// goroutine and may block for as long as it needs to.
	sweep func(role string)
	// roles enumerates everything to sweep on the startup and periodic
	// passes — the reconciliation set, as opposed to the doorbell's one role.
	roles func() []string
	tick  time.Duration

	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}
}

func newSpoolReactor(sweep func(role string), roles func() []string, tick time.Duration) *spoolReactor {
	if tick <= 0 {
		tick = spoolSweepInterval
	}
	return &spoolReactor{
		sweep:   sweep,
		roles:   roles,
		tick:    tick,
		pending: map[string]bool{},
		wake:    make(chan struct{}, 1),
	}
}

// mark schedules roles for a sweep. It never blocks: the wake channel holds
// one slot, and a full one already means "there is work", which is the only
// fact the loop needs.
func (r *spoolReactor) mark(roles ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	for _, role := range roles {
		if role != "" {
			r.pending[role] = true
		}
	}
	empty := len(r.pending) == 0
	r.mu.Unlock()
	if empty {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// markAll schedules every role the reconciliation set knows about.
func (r *spoolReactor) markAll() {
	if r == nil {
		return
	}
	r.mark(r.roles()...)
}

// run is the reactor loop. It sweeps FIRST and waits second, so the startup
// pass happens before anything can ring — the cold-start delivery path.
func (r *spoolReactor) run(ctx context.Context) {
	r.markAll()
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		r.drain(ctx)
		select {
		case <-r.wake:
		case <-t.C:
			r.markAll()
		case <-ctx.Done():
			return
		}
	}
}

// drain sweeps every pending role, repeating until the set is empty: a
// doorbell that arrives while a sweep is running must not be lost to the
// snapshot it missed.
func (r *spoolReactor) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		if len(r.pending) == 0 {
			r.mu.Unlock()
			return
		}
		batch := make([]string, 0, len(r.pending))
		for role := range r.pending {
			batch = append(batch, role)
		}
		r.pending = map[string]bool{}
		r.mu.Unlock()
		for _, role := range batch {
			if ctx.Err() != nil {
				return
			}
			r.sweep(role)
		}
	}
}

// ---- the message projection, both directions ---------------------------

// spoolRawJSONKey is the frontmatter marker under which a structured payload
// that is NOT a JSON object travels.
//
// YAML frontmatter's `structured` is a mapping, so a bare array, string or
// number has nowhere faithful to sit. Before the cutover that was refused at
// the projection, which was right for a shadow (the tee dropped a file and
// counted it) and is fatal for a delivery (the message itself would be lost).
// The payload therefore travels as its ORIGINAL JSON TEXT under this key —
// bytes in, identical bytes out — rather than as a YAML value, because a YAML
// round trip is exactly where a large integer loses its precision and a
// numeric-looking string stops being a string.
//
// An object payload that would itself be mistaken for a wrapper (this key
// alone, with a string value) is wrapped too. That case is vanishingly rare
// and the alternative is an ambiguity: unwrapping would hand a reader back a
// payload its sender never wrote.
const spoolRawJSONKey = "spool_raw_json"

// spoolStructured projects a mailbox message's raw-JSON companion onto the
// frontmatter mapping — an object as itself, anything else wrapped verbatim
// (see spoolRawJSONKey). The only refusal left is a payload that is not JSON
// at all, which no in-tree producer can make: every caller either marshals a
// proto Struct or hands a literal this package wrote.
func spoolStructured(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("coord: mail structured payload is not valid JSON, refusing to write it onto the spool: %q", clip(string(raw)))
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		// A literal `null` decodes without error into a nil map: no payload.
		if obj == nil {
			return nil, nil
		}
		if !looksSpoolWrapped(obj) {
			return obj, nil
		}
	}
	return map[string]any{spoolRawJSONKey: string(raw)}, nil
}

// mailStructured inverts spoolStructured: the frontmatter mapping back to the
// raw-JSON companion a mailbox Message carries.
func mailStructured(head map[string]any) (json.RawMessage, error) {
	if len(head) == 0 {
		return nil, nil
	}
	if looksSpoolWrapped(head) {
		raw := json.RawMessage(head[spoolRawJSONKey].(string))
		if !json.Valid(raw) {
			return nil, fmt.Errorf("coord: frontmatter %q does not hold valid JSON: %q", spoolRawJSONKey, clip(string(raw)))
		}
		return raw, nil
	}
	raw, err := json.Marshal(head)
	if err != nil {
		return nil, fmt.Errorf("coord: frontmatter structured payload cannot be read back as JSON: %w", err)
	}
	return raw, nil
}

// looksSpoolWrapped reports whether head is the wrapper shape: that one key,
// alone, with a string value. All three conditions matter — a mapping that
// merely CONTAINS the key alongside others is an ordinary payload.
func looksSpoolWrapped(head map[string]any) bool {
	if len(head) != 1 {
		return false
	}
	v, ok := head[spoolRawJSONKey]
	if !ok {
		return false
	}
	_, isString := v.(string)
	return isString
}

func clip(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// deliverableStructured normalises a spool payload for the PeerMessage WIRE
// shape, which is a protobuf Struct and therefore always an object.
//
// A bare array or scalar has nowhere to sit in a Struct, so today's mailbox
// path warns and leaves such a message pending forever (pushMail's "cannot
// project mail onto the wire"). Under the cutover the file already carries the
// payload faithfully, and the only question left is what the engine's turn
// sees; wrapping it under the SAME marker key the file uses (spoolRawJSONKey)
// delivers the message with its payload legible instead of stranding it, and
// keeps one spelling of the wrapper rather than two.
func deliverableStructured(raw json.RawMessage) (json.RawMessage, error) {
	head, err := spoolStructured(raw)
	if err != nil {
		return nil, err
	}
	if len(head) == 0 {
		return nil, nil
	}
	out, err := json.Marshal(head)
	if err != nil {
		return nil, fmt.Errorf("coord: structured payload cannot be projected onto the delivery seam: %w", err)
	}
	return out, nil
}

// mailFromSpool recovers the mailbox Message one spool file carries.
//
// The message ID is origin_id when the producer had one (the coordinator mints
// a mailbox id before it writes, so correlation registered by relayApproval
// against that id still resolves) and the FILENAME STEM otherwise — which is
// the spool's own identity and the one every reader can agree on. It is the
// dedupe key on both sides, so getting it from anywhere else would break
// at-least-once into at-least-twice.
func mailFromSpool(e spool.Entry, from string) (Message, error) {
	if e.Message == nil {
		return Message{}, fmt.Errorf("coord: spool entry %s carries no message", e.Ref)
	}
	kind, err := MailKindForSpool(e.Message.Kind)
	if err != nil {
		return Message{}, fmt.Errorf("coord: %s: %w", e.Ref, err)
	}
	structured, err := mailStructured(e.Message.Structured)
	if err != nil {
		return Message{}, fmt.Errorf("coord: %s: %w", e.Ref, err)
	}
	id := e.Message.OriginID
	if id == "" {
		id = strings.TrimSuffix(e.Ref.Name, spool.MessageFileExt)
	}
	return Message{
		ID:         id,
		From:       from,
		To:         e.Message.To,
		Kind:       kind,
		Body:       e.Message.Body,
		Structured: structured,
		InReplyTo:  e.Message.InReplyTo,
	}, nil
}

// ---- coordinator side --------------------------------------------------

// spoolPosture is what this coordinator stamps onto every runner it spawns.
func (c *Coordinator) spoolPosture() spoolPosture {
	return spoolPosture{Tee: c.spoolTee, Delivery: c.spoolDelivery}
}

// SpoolDeliveryEnabled reports whether this coordinator delivers mail by file.
func (c *Coordinator) SpoolDeliveryEnabled() bool { return c.spoolDelivery }

// spoolDeliverTo reports whether mail for role is delivered by FILE rather
// than by mailbox.
//
// Two conditions beyond the flag, and both are about there being a reader on
// the other end. The recipient must be a run this coordinator tracks, and it
// must be a MIGRATED (StartRun) run — one with a ctxloom runner that sweeps
// its own spool. The session owner's own mailbox is drained in this process by
// AgentRecv, and a frozen legacy go-plugin child has no runner at all; writing
// a file for either would be a message delivered to a directory nobody reads,
// with every signal green.
func (c *Coordinator) spoolDeliverTo(role string) bool {
	if !c.spoolDelivery || role == "" {
		return false
	}
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil || !rt.viaStartRun {
		return false
	}
	tracked := false
	c.runs.View(func() { tracked = c.runsF.currentRun(role) != nil })
	return tracked
}

// deliverMailViaSpool IS the delivery: it writes msg as the single copy in the
// recipient's in/ and rings the doorbell.
//
// Unlike the shadow tee, a failure here is RETURNED. The tee could swallow its
// own errors because the mailbox was still the system of record behind it;
// under the cutover there is nothing behind it, so a write that failed is a
// message that does not exist and the sender must be told so.
//
// completed is always false: nothing was handed to a waiting receiver
// synchronously. The recipient's runner delivers it on the doorbell or its
// next sweep, and its consume-rename is what reports back that it landed.
func (c *Coordinator) deliverMailViaSpool(msg Message) (string, bool, error) {
	sm, err := spoolMessageForMail(msg, msg.To)
	if err != nil {
		return "", false, fmt.Errorf("coordinator mail: cannot project message %s for %s onto the spool: %w", msg.ID, msg.To, err)
	}
	w, err := c.spoolIn.writerFor(msg.To)
	if err != nil {
		return "", false, fmt.Errorf("coordinator mail: cannot open %s's spool to deliver message %s: %w", msg.To, msg.ID, err)
	}
	ref, err := w.Write(sm)
	if err != nil {
		return "", false, fmt.Errorf("coordinator mail: writing message %s into %s's spool: %w", msg.ID, msg.To, err)
	}
	c.audit("spool_mail_out", msg.To, map[string]string{"message_id": msg.ID, "kind": msg.Kind, "ref": ref.String()})
	// Fire-and-forget by contract: the file is already durable, so a doorbell
	// that cannot go out costs the recipient one sweep interval and never the
	// message.
	if err := c.RingSpool(msg.To, ref); err != nil {
		clidiag.Warn("ctxloom", "coordinator: delivered %s into %s's spool but could not ring it: %v (it will be swept)", ref, msg.To, err)
	}
	return msg.ID, false, nil
}

// spoolPendingCount counts role's UNDELIVERED spool mail — the file-backed
// answer to "is there mail this child still has to see", which drives the
// ended-child resume and the standup drain.
//
// A directory that does not exist is zero, not a fault: nothing has ever been
// written for that role. Any OTHER failure is reported loudly and counted
// before returning zero, because this function has no error channel and the
// callers all read zero as "nothing to do" — the one shape in which a
// readdir failure would silently strand a child's mail.
func (c *Coordinator) spoolPendingCount(role string) int {
	res, ok := c.sweepSpoolDir(role, spool.DirIn, "counting pending mail")
	if !ok {
		return 0
	}
	if err := res.ProblemErr(); err != nil {
		clidiag.Warn("ctxloom", "coordinator: %s's spool holds files that cannot be read as messages and are NOT counted as pending: %v", role, err)
	}
	return len(res.Entries)
}

// sweepSpoolDir reads one spool directory, distinguishing "not there" (no
// messages, no complaint) from a real failure (loud, counted). ok=false means
// the caller has nothing to process.
func (c *Coordinator) sweepSpoolDir(harp string, dir spool.Dir, why string) (spool.SweepResult, bool) {
	mapper := spool.NewHomeMapper()
	path, err := spool.DirPath(mapper, harp, dir)
	if err != nil {
		clidiag.Warn("ctxloom", "coordinator: cannot resolve %s's %s spool (%s): %v", harp, dir, why, err)
		c.spoolDeliveryCount.failed.Add(1)
		return spool.SweepResult{}, false
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return spool.SweepResult{Dir: dir}, false
	}
	res, err := spool.Sweep(mapper, harp, dir)
	if err != nil {
		clidiag.Warn("ctxloom", "coordinator: sweeping %s's %s spool (%s): %v", harp, dir, why, err)
		c.spoolDeliveryCount.failed.Add(1)
		return spool.SweepResult{}, false
	}
	return res, true
}

// startSpoolReactor brings up the coordinator's spool reader. It is a no-op
// unless the cutover is on — the reactor is what would create directories and
// the flag-off promise is that nothing on disk changes.
func (c *Coordinator) startSpoolReactor() {
	if !c.spoolDelivery {
		return
	}
	c.spoolSeen = map[string]map[string]bool{}
	c.spoolReactor = newSpoolReactor(c.sweepChildSpool, c.spoolRoles, c.spoolSweepInterval)
	c.goTracked(func() { c.spoolReactor.run(c.baseCtx) })
}

// spoolRoles is the reconciliation set: every harp this coordinator has a run
// record for. It is deliberately wider than "currently attached" — the whole
// point of the startup pass is to find mail written for a run whose channel
// does not exist yet, or exists no longer.
func (c *Coordinator) spoolRoles() []string {
	var out []string
	c.runs.View(func() { out = c.runsF.currentHarps() })
	return out
}

// sweepChildSpool is the coordinator's whole reading job for one child: route
// what the child SENT (out/), and note what the child CONSUMED (in/consumed).
func (c *Coordinator) sweepChildSpool(role string) {
	c.sweepChildOut(role)
	c.sweepChildConsumed(role)
}

// sweepChildOut routes every message sitting in role's out/, oldest first, and
// consumes each one only after it has been routed.
//
// DELIVER THEN CONSUME is the at-least-once ordering: a crash between the two
// re-routes on the next sweep (deduped downstream on message id), while
// consuming first would drop the message on the floor with nothing to show for
// it. The duplicate that ordering admits is what the reactor's serialisation
// and the rename together rule out.
func (c *Coordinator) sweepChildOut(role string) {
	res, ok := c.sweepSpoolDir(role, spool.DirOut, "routing what the child sent")
	if !ok {
		return
	}
	for _, p := range res.Problems {
		clidiag.Warn("ctxloom", "coordinator: %s wrote a spool file that is not a message and will not be routed: %v", role, p.Error())
		c.spoolDeliveryCount.failed.Add(1)
	}
	for _, e := range res.Entries {
		c.routeSpoolOut(role, e)
	}
}

// routeSpoolOut routes ONE out/ file to its recipient.
//
// SENDER IDENTITY IS THE DIRECTORY: the message is from role because it was
// found in role's spool, and the identity handed to peerSend is resolved from
// role alone. The file's own from_harp is display metadata that never reaches
// a routing decision — a child that wrote a sibling's harp there would
// otherwise have peerSend resolve the SIBLING's parent and deliver a message
// in its name.
func (c *Coordinator) routeSpoolOut(role string, e spool.Entry) {
	sender, ok := c.spoolSenderIdentity(role)
	if !ok {
		clidiag.Warn("ctxloom", "coordinator: %s's spool holds an outbound message but that harp has no run record; leaving %s in place", role, e.Ref)
		c.spoolDeliveryCount.failed.Add(1)
		return
	}
	msg, err := mailFromSpool(e, role)
	if err != nil {
		clidiag.Warn("ctxloom", "coordinator: refusing an unroutable message from %s: %v", role, err)
		c.spoolDeliveryCount.failed.Add(1)
		c.consumeSpool(role, e.Ref)
		return
	}
	if _, _, _, err := c.peerSend(sender, msg.To, msg.Kind, msg.Body, msg.Structured, msg.InReplyTo); err != nil {
		// The routing chokepoint refused it (closed kind vocabulary,
		// hub-and-spoke, unknown recipient). The agent's local write already
		// returned success, so the refusal is reported back the only way that
		// still reaches it: as mail.
		clidiag.Warn("ctxloom", "coordinator: refusing %s's spool message %s: %v", role, e.Ref, err)
		c.spoolDeliveryCount.failed.Add(1)
		c.replySpoolRefusal(role, msg, err)
		c.consumeSpool(role, e.Ref)
		return
	}
	c.spoolDeliveryCount.delivered.Add(1)
	c.consumeSpool(role, e.Ref)
}

// replySpoolRefusal tells a child that the message it wrote could not be
// routed. Without it a refused send is invisible to the agent: its local write
// succeeded, and the refusal happens later in another process.
func (c *Coordinator) replySpoolRefusal(role string, msg Message, cause error) {
	body := fmt.Sprintf("your message to %q was not delivered: %v", msg.To, cause)
	if _, _, err := c.queueMail(role, role, KindError, body); err != nil {
		clidiag.Warn("ctxloom", "coordinator: could not tell %s that its message was refused (%v): %v", role, cause, err)
	}
}

// spoolSenderIdentity resolves a spool directory's owning harp to the identity
// its messages are sent under — the file-plane analog of deriving a caller's
// identity from its credential rather than from anything it wrote.
func (c *Coordinator) spoolSenderIdentity(role string) (Identity, bool) {
	var id Identity
	ok := false
	c.runs.View(func() {
		r := c.runsF.currentRun(role)
		if r == nil {
			return
		}
		id = Identity{Harp: role, RunID: r.RunID, Depth: r.Depth, OneShot: r.OneShot, Project: c.projectDir}
		ok = true
	})
	return id, ok
}

// consumeSpool renames a processed file into its consumed/ sibling. A lost
// race (ErrAlreadyGone) is the expected outcome of the other path having won
// and is never reported as a failure.
func (c *Coordinator) consumeSpool(role string, ref spool.Ref) {
	done, err := spool.Consume(spool.NewHomeMapper(), ref)
	if err != nil {
		if errors.Is(err, spool.ErrAlreadyGone) {
			return
		}
		clidiag.Warn("ctxloom", "coordinator: routed %s but could not mark it consumed: %v (it will be routed again on the next sweep)", ref, err)
		c.spoolDeliveryCount.failed.Add(1)
		return
	}
	_ = done
}

// sweepChildConsumed reads in/consumed — the child's acknowledgements — and
// credits the ONE thing they mean to the coordinator: real progress, which
// forgives the relaunch budget (the file-plane replacement for the runner's
// mail_consumed fact).
//
// Entries are remembered so a later sweep does not re-credit them. The set
// grows with the run's delivered mail and is dropped with the process; there
// is no retention prune of consumed/ yet, which is what makes the set
// necessary.
func (c *Coordinator) sweepChildConsumed(role string) {
	res, ok := c.sweepSpoolDir(role, spool.DirInConsumed, "reading delivery acknowledgements")
	if !ok {
		return
	}
	fresh := 0
	c.spoolSeenMu.Lock()
	seen := c.spoolSeen[role]
	if seen == nil {
		seen = map[string]bool{}
		c.spoolSeen[role] = seen
	}
	for _, e := range res.Entries {
		if seen[e.Ref.Name] {
			continue
		}
		seen[e.Ref.Name] = true
		fresh++
	}
	c.spoolSeenMu.Unlock()
	if fresh == 0 {
		return
	}
	c.spoolDeliveryCount.consumed.Add(uint64(fresh))
	c.noteMailConsumed(role) // real progress: the relaunch budget is forgiven
}

// SpoolDeliveryStats reports this coordinator's cumulative file-plane
// outcomes.
func (c *Coordinator) SpoolDeliveryStats() SpoolDeliveryStats { return c.spoolDeliveryCount.stats() }

// SpoolDeliveryStats reports what the file mail plane did and could not do.
// Every counter is cumulative for the process's lifetime.
type SpoolDeliveryStats struct {
	// Delivered counts messages this side handed to its own surface: turns or
	// recv batches on a runner, routed sends on a coordinator.
	Delivered uint64
	// Consumed counts consume-renames observed or performed.
	Consumed uint64
	// Failed counts everything that did not get through — an unreadable file,
	// an unroutable message, a rename that errored. Under the cutover these
	// are not shadow divergences; each one is a message that has not arrived.
	Failed uint64
}

type spoolDeliveryCounters struct {
	delivered atomic.Uint64
	consumed  atomic.Uint64
	failed    atomic.Uint64
}

func (s *spoolDeliveryCounters) stats() SpoolDeliveryStats {
	return SpoolDeliveryStats{
		Delivered: s.delivered.Load(),
		Consumed:  s.consumed.Load(),
		Failed:    s.failed.Load(),
	}
}

// ---- runner side -------------------------------------------------------

// SpoolDeliveryEnabled reports whether this runner takes its mail from files.
// It answers from the resolved state, not the config bit: a run with no harp
// has no spool and stays on the mailbox however it was stamped.
func (h *Home) SpoolDeliveryEnabled() bool { return h.spoolDelivery }

// SpoolDeliveryStats reports this runner's cumulative file-plane outcomes.
func (h *Home) SpoolDeliveryStats() SpoolDeliveryStats { return h.spoolDeliveryCount.stats() }

// startSpoolReactor brings up the runner's own in/ reader. Its reconciliation
// set is a single role — a runner has exactly one spool — but it is the same
// machinery as the coordinator's on purpose: the startup pass, the collapse of
// concurrent wakes, and the serialisation that keeps two triggers from
// delivering one file twice are all properties this side needs identically.
func (h *Home) startSpoolReactor() {
	h.spoolIn = newSpoolReactor(
		func(string) { h.sweepSpoolIn() },
		func() []string { return []string{h.cfg.Harp} },
		h.cfg.SpoolSweepInterval,
	)
	h.goTracked(func() { h.spoolIn.run(h.ctx) })
}

// SweepSpoolIn asks for a reconciliation sweep of this run's in/ spool. It is
// the one call every trigger funnels through — turn boundary, run-channel
// reattach, doorbell — so a new trigger cannot accidentally introduce a second
// way of reading the same directory.
func (h *Home) SweepSpoolIn() {
	if !h.spoolDelivery {
		return
	}
	h.spoolIn.mark(h.cfg.Harp)
}

// sweepSpoolIn delivers everything currently in this run's in/ spool, oldest
// first, through the SAME delivery-by-state seam a pushed mailbox notice uses
// (deliverNotice): a parked recv completes, a live engine gets a new turn,
// and anything arriving before the engine exists waits in the buffer.
//
// Delivery is where the file's journey through this process starts, not where
// it ends: the consume-rename happens later, at the moment the delivery is
// proven (the engine accepted the turn, or a subsequent Recv proved the
// harness took the batch). deliverNotice's own dedupe on message id is what
// makes a doorbell and a sweep that race resolve to one delivery.
func (h *Home) sweepSpoolIn() {
	mapper := spool.NewHomeMapper()
	path, err := spool.DirPath(mapper, h.cfg.Harp, spool.DirIn)
	if err != nil {
		clidiag.Warn("ctxloom", "runner: cannot resolve this run's in/ spool: %v", err)
		h.spoolDeliveryCount.failed.Add(1)
		return
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return // nothing has ever been written for this run
	}
	res, err := spool.Sweep(mapper, h.cfg.Harp, spool.DirIn)
	if err != nil {
		clidiag.Warn("ctxloom", "runner: sweeping this run's in/ spool: %v", err)
		h.spoolDeliveryCount.failed.Add(1)
		return
	}
	for _, p := range res.Problems {
		clidiag.Warn("ctxloom", "runner: a file in this run's in/ spool is not a message and will not be delivered: %v", p.Error())
		h.spoolDeliveryCount.failed.Add(1)
	}
	for _, e := range res.Entries {
		msg, err := mailFromSpool(e, e.Message.FromHarp)
		if err != nil {
			clidiag.Warn("ctxloom", "runner: refusing an undeliverable spool message: %v", err)
			h.spoolDeliveryCount.failed.Add(1)
			continue
		}
		wire, err := deliverableStructured(msg.Structured)
		if err != nil {
			clidiag.Warn("ctxloom", "runner: cannot project spool message %s's payload onto the delivery seam: %v", e.Ref, err)
			h.spoolDeliveryCount.failed.Add(1)
			continue
		}
		msg.Structured = wire
		pm, err := peerMessageProto(msg)
		if err != nil {
			clidiag.Warn("ctxloom", "runner: cannot project spool message %s onto the delivery seam: %v", e.Ref, err)
			h.spoolDeliveryCount.failed.Add(1)
			continue
		}
		h.rememberSpoolRef(msg.ID, e.Ref)
		h.spoolDeliveryCount.delivered.Add(1)
		h.deliverNotice(pm)
	}
}

// rememberSpoolRef records which file a delivered id came from, so the
// consume-rename can find it at the acknowledgement moment.
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

// ackMailConsumed is the ONE acknowledgement point for delivered mail, whichever
// substrate carried it: the mailbox's consumption fact, or the spool's
// consume-rename plus the doorbell that announces it.
//
// Routing both through one function is what keeps the two from drifting: the
// ack timing (after the engine accepted / after the next Recv) is a property
// of the CALLER, and it is the property that keeps at-least-once true.
func (h *Home) ackMailConsumed(ids []string) {
	if len(ids) == 0 {
		return
	}
	if !h.spoolDelivery {
		h.emitMailConsumed(ids)
		return
	}
	var viaMailbox []string
	for _, id := range ids {
		ref, ok := h.takeSpoolRef(id)
		if !ok {
			// Not a file delivery: a cutover run can still be handed a
			// mailbox message (a terminal fallback, say), and swallowing its
			// ack would leave it pending forever.
			viaMailbox = append(viaMailbox, id)
			continue
		}
		done, err := spool.Consume(spool.NewHomeMapper(), ref)
		if err != nil {
			if errors.Is(err, spool.ErrAlreadyGone) {
				continue
			}
			clidiag.Warn("ctxloom", "runner: delivered %s but could not mark it consumed: %v (the coordinator will see it as still pending)", ref, err)
			h.spoolDeliveryCount.failed.Add(1)
			continue
		}
		h.spoolDeliveryCount.consumed.Add(1)
		// The consume-rename IS the delivery ack, and this ring is how the
		// coordinator learns of it without polling.
		if err := h.RingSpool(done); err != nil {
			clidiag.Warn("ctxloom", "runner: consumed %s but could not announce it: %v", done, err)
		}
	}
	h.emitMailConsumed(viaMailbox)
}

// emitMailConsumed sends the mailbox's own consumption fact.
func (h *Home) emitMailConsumed(ids []string) {
	if len(ids) == 0 {
		return
	}
	vals := make([]any, len(ids))
	for i, id := range ids {
		vals[i] = id
	}
	h.emitCustomEvent(CustomMailConsumed, map[string]any{"message_ids": vals})
}

// sendPeerViaSpool is agent_send under the cutover: a LOCAL, durable file
// write into this run's out/ plus a doorbell, with no coordinator round trip
// at all. handled=false leaves the request to the ordinary plane-2 path.
//
// The guards duplicated from servePeerSend (a recipient, some text, a kind
// from the closed sender vocabulary) are duplicated ON PURPOSE: they are the
// refusals an agent can still be told about synchronously, and losing them to
// "the coordinator will complain later, by mail" would make a mistyped kind a
// silently-dropped message instead of an immediate error.
func (h *Home) sendPeerViaSpool(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, bool) {
	if !h.spoolDelivery {
		return nil, false
	}
	send := req.GetPeerSend()
	if send == nil {
		return nil, false
	}
	to := send.GetToAgentId()
	if role := send.GetToRole(); role != "" {
		if to != "" {
			return spoolSendErr(codes.InvalidArgument, "agent_send: set exactly one of to_agent_id / to_role, not both"), true
		}
		to = role
	}
	if to == "" {
		return spoolSendErr(codes.InvalidArgument, `agent_send: a recipient is required — to_agent_id (a child harp) or to_role: "parent"`), true
	}
	if send.GetText() == "" {
		return spoolSendErr(codes.InvalidArgument, "agent_send: text is required"), true
	}
	kind := ""
	var structured json.RawMessage
	if s := send.GetStructured(); s != nil {
		if v, ok := s.GetFields()["kind"]; ok {
			kind = v.GetStringValue()
		}
		raw, err := protojson.Marshal(s)
		if err != nil {
			return spoolSendErr(codes.InvalidArgument,
				fmt.Sprintf("agent_send: structured payload cannot be encoded, refusing to send it stripped: %v", err)), true
		}
		structured = raw
	}
	if err := SenderMailKind(kind); err != nil {
		return spoolSendErr(codes.InvalidArgument, err.Error()), true
	}
	sm, err := spoolMessageForMail(Message{
		From: h.cfg.Harp, To: to, Kind: kind,
		Body: send.GetText(), Structured: structured, InReplyTo: send.GetInReplyTo(),
	}, to)
	if err != nil {
		h.spoolDeliveryCount.failed.Add(1)
		return spoolSendErr(codes.InvalidArgument, fmt.Sprintf("agent_send: %v", err)), true
	}
	w, err := h.spoolOut.writerFor(h.cfg.Harp)
	if err != nil {
		h.spoolDeliveryCount.failed.Add(1)
		return spoolSendErr(codes.Internal, fmt.Sprintf("agent_send: cannot open this run's outbound spool: %v", err)), true
	}
	ref, err := w.Write(sm)
	if err != nil {
		h.spoolDeliveryCount.failed.Add(1)
		return spoolSendErr(codes.Internal, fmt.Sprintf("agent_send: writing the message: %v", err)), true
	}
	h.spoolDeliveryCount.delivered.Add(1)
	if err := h.RingSpool(ref); err != nil {
		clidiag.Warn("ctxloom", "runner: wrote %s but could not ring the coordinator: %v (it will be swept)", ref, err)
	}
	return &agentcoordpb.CoordinatorResponse{
		RequestId: req.GetRequestId(),
		Status:    okStatus("written to this session's outbound spool"),
		Kind: &agentcoordpb.CoordinatorResponse_PeerSend{PeerSend: &agentcoordpb.PeerSendResult{
			// The FILENAME STEM is the message id, because under the cutover
			// the file is the message: there is no coordinator-minted mailbox
			// id to quote, and an id the recipient could not resolve back to a
			// file would make every in_reply_to written against it dangling.
			MessageId: strings.TrimSuffix(ref.Name, spool.MessageFileExt),
			Delivery:  agentcoordpb.PeerSendResult_DELIVERY_QUEUED,
		}},
	}, true
}

func spoolSendErr(code codes.Code, msg string) *agentcoordpb.CoordinatorResponse {
	return &agentcoordpb.CoordinatorResponse{Status: statusErr(code, msg)}
}
