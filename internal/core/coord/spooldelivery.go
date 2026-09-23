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

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// THE MAIL PLANE: coordinator<->child mail is DELIVERED FROM FILES.
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
// and the request plane.
//
// agent_report's REPORT still rides the events plane and is journaled into the
// reports fold, which remains its store of record — but a FINAL report now also
// queues a KindReport NOTICE to the child's parent (reports.go's
// notifyParentOfFinalReport), and that notice is ordinary mail, so it takes
// whichever route this file chooses for the recipient like any other. The
// report and the notice are two different things: one is the content, the other
// is the wake. They were previously the same thing only in the sense that
// neither reached a waiting parent.
//
// THE SESSION OWNER is a spool recipient too. Its in/ is read by its
// turn-start hook and in-process by AgentRecv. The owner is identified by
// DECLARATION (Options.OwnerHarp), never by a run record — no launch minted it,
// so it has none (spoolRoles).
//
// Any other recipient — a harp with no tracked current run — has no spool
// reader, so mail to it is REFUSED (ErrNoSpoolReader): a file written for it
// would sit in a directory nothing ever reads.

// spoolSweepInterval is the slow reconciliation cadence on BOTH sides: the
// backstop for a doorbell dropped on a stream that never went down (the
// saturated-pump case). It is a tunable constant rather than config surface —
// nothing hangs on the exact number, because the startup and reconnect sweeps
// already bound every case where a doorbell could be missed for longer.
const spoolSweepInterval = 30 * time.Second

// SpoolReactor serialises one side's spool reading.
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
type SpoolReactor struct {
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

func NewSpoolReactor(sweep func(role string), roles func() []string, tick time.Duration) *SpoolReactor {
	if tick <= 0 {
		tick = spoolSweepInterval
	}
	return &SpoolReactor{
		sweep:   sweep,
		roles:   roles,
		tick:    tick,
		pending: map[string]bool{},
		wake:    make(chan struct{}, 1),
	}
}

// Mark schedules roles for a sweep. It never blocks: the wake channel holds
// one slot, and a full one already means "there is work", which is the only
// fact the loop needs.
func (r *SpoolReactor) Mark(roles ...string) {
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
func (r *SpoolReactor) markAll() {
	if r == nil {
		return
	}
	r.Mark(r.roles()...)
}

// Run is the reactor loop. It sweeps FIRST and waits second, so the startup
// pass happens before anything can ring — the cold-start delivery path.
func (r *SpoolReactor) Run(ctx context.Context) {
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
func (r *SpoolReactor) drain(ctx context.Context) {
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
// number has nowhere faithful to sit, and refusing it at the projection
// would lose the message itself. The payload therefore travels as its
// ORIGINAL JSON TEXT under this key —
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

// DeliverableStructured normalises a spool payload for the PeerMessage WIRE
// shape, which is a protobuf Struct and therefore always an object.
//
// A bare array or scalar has nowhere to sit in a Struct. The file already
// carries the payload faithfully, and the only question left is what the
// engine's turn sees; wrapping it under the SAME marker key the file uses
// (spoolRawJSONKey) delivers the message with its payload legible instead of
// stranding it, and keeps one spelling of the wrapper rather than two.
func DeliverableStructured(raw json.RawMessage) (json.RawMessage, error) {
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

// MailFromSpool recovers the mailbox Message one spool file carries.
//
// The message ID is origin_id when the producer had one (the coordinator mints
// a mailbox id before it writes, so correlation registered by relayApproval
// against that id still resolves) and the FILENAME STEM otherwise — which is
// the spool's own identity and the one every reader can agree on. It is the
// dedupe key on both sides, so getting it from anywhere else would break
// at-least-once into at-least-twice.
func MailFromSpool(e spool.Entry, from string) (Message, error) {
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

// spoolDeliverTo reports whether mail for role is delivered by FILE rather
// than by mailbox — whether there is a spool READER on the other end.
//
// Two recipient classes have one:
//
//   - THE OWNER: this session's own harp, whose in/ is read by AgentRecv
//     in-process and by its turn-start hook (ownerSpool). It is a class of
//     its own because it is identified by declaration, not by a run record.
//   - A CHILD, drained by its runner: a run this coordinator tracks, whose
//     ctxloom runner sweeps its own spool. Mail written while the child waits
//     on the execution cap is already a file its runner's startup sweep will
//     find.
//
// Any other harp has neither: a file written for it would be a message
// delivered to a directory nobody reads, with every signal green.
func (c *Coordinator) spoolDeliverTo(role string) bool {
	if c.ownerSpool(role) {
		return true
	}
	if role == "" {
		return false
	}
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil {
		return false
	}
	tracked := false
	c.runs.View(func() { tracked = c.runsF.currentRun(role) != nil })
	return tracked
}

// ownerSpool reports whether role's inbox is a spool THIS PROCESS reads: the
// declared session owner. It is narrower than spoolDeliverTo on purpose — a
// migrated child's in/ is also a spool, but its reader is the child's runner,
// and the recv side must never drain a directory another process owns.
func (c *Coordinator) ownerSpool(role string) bool {
	return role != "" && role == c.ownerHarp
}

// ErrNoSpoolReader refuses mail for a recipient with no spool reader: not
// the declared owner, and not a run this coordinator tracks. A file written
// for it would be a message delivered to a directory nobody reads, with
// every signal green.
var ErrNoSpoolReader = errors.New("coordinator mail: the recipient has no spool reader (it is neither the session owner nor a tracked run)")

// queueMail is queueMailPayload's common-case wrapper: no structured
// companion, no reply correlation.
func (c *Coordinator) queueMail(from, to, kind, body string) (msgID string, err error) {
	return c.queueMailPayload(from, to, kind, body, nil, "")
}

// queueMailPayload delivers one message: the write into the recipient's in/
// spool IS the delivery, fsynced before return, and the doorbell only bounds
// latency. Routing policy is the caller's. structured is an optional
// JSON-object companion (e.g. the escalation ladder's relayed
// ApprovalRequest projection); inReplyTo correlates this message to an
// earlier one's id.
//
// Nothing is handed to a waiting receiver synchronously: the recipient's
// reader delivers it on the doorbell or its next sweep, and its
// consume-rename is what reports back that it landed.
func (c *Coordinator) queueMailPayload(from, to, kind, body string, structured json.RawMessage, inReplyTo string) (msgID string, err error) {
	return c.queueMailPayloadID(newMessageID(), from, to, kind, body, structured, inReplyTo)
}

// queueMailPayloadID is queueMailPayload with the message id supplied by the
// caller. It exists for correlation-carrying mail whose id must be REGISTERED
// somewhere before the mail is observable: this function publishes, and after
// it returns a reply quoting the id can already arrive. relayApproval is the
// case that forced it; see its comment.
func (c *Coordinator) queueMailPayloadID(msgID, from, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, error) {
	// Role "" is undrainable by construction — agent_recv drains the caller's
	// own harp and no session has the empty harp. Refused here, at the one
	// point every sender funnels through, rather than at each sender.
	if to == "" {
		return "", fmt.Errorf("coordinator mail: refusing to queue a %q message from %q with no recipient: no session can drain role %q", kind, from, to)
	}
	// A message with NO payload is refused at the same chokepoint. Delivered,
	// it completes a parked recv and is answered with the ordinary success
	// disposition — a recipient woken for a turn whose content is nothing at
	// all, with every signal green. A structured companion IS payload (the
	// relayed ApprovalRequest projection and its replies carry it), so only a
	// message with neither is empty.
	if strings.TrimSpace(body) == "" && len(structured) == 0 {
		return "", fmt.Errorf("coordinator mail: refusing to queue an empty message from %q to %q (kind %q): "+
			"it carries no text and no structured payload, so the recipient would be woken with nothing to act on "+
			"(check the sender's message composition)", from, to, kind)
	}
	if !c.spoolDeliverTo(to) {
		return "", fmt.Errorf("%w: %q (from %q, kind %q)", ErrNoSpoolReader, to, from, kind)
	}
	// The body bound, here because every mail write funnels through here: the
	// Send verb, the bare agent_send, a child's spool out/ (routeSpoolOut ->
	// peerSend), and the coordinator's own notices alike. The overflow is filed
	// under the RECIPIENT, whose own artifacts it may always read.
	body, err := c.boundBody(to, body)
	if err != nil {
		return "", err
	}
	msg := Message{ID: msgID, From: from, To: to, Kind: kind, Body: body, Structured: structured, InReplyTo: inReplyTo}
	// Write-and-ring is ONE operation (spoolcourier.go): the pairing used to be
	// a convention repeated at each site, which is what made "made durable and
	// handed to nobody" expressible here at all. A failure is RETURNED: there
	// is nothing behind the file, so a write that failed is a message that
	// does not exist and the sender must be told so.
	if _, err := c.mailCourier().Send(msg); err != nil {
		return "", err
	}
	return msg.ID, nil
}

// mailCourier delivers coordinator mail into the RECIPIENT's inbound spool.
func (c *Coordinator) mailCourier() *SpoolCourier {
	return &SpoolCourier{
		Rep:     c.rep,
		Writers: c.spoolIn,
		KeyFor:  func(to string) string { return to },
		Ring:    c.ringSpool,
		OnSent: func(to string, msg Message, ref spool.Ref) {
			c.audit("spool_mail_out", to, map[string]string{"message_id": msg.ID, "kind": msg.Kind, "ref": ref.String()})
			c.mu.Lock()
			seam := c.afterMailWritten
			c.mu.Unlock()
			if seam != nil {
				seam(to)
			}
		},
		Side: "coordinator",
	}
}

// pendingCount reports how many messages could still be delivered to role —
// the ended-child check: leftover mail triggers a resume, never strands. The
// owner needs no separate count: what a receive or the turn-start hook has
// already taken sits in in/claimed/, not in/, so the file-backed count of
// in/ is already "waiting, not spoken for".
func (c *Coordinator) pendingCount(role string) int {
	return c.spoolPendingCount(role)
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
		c.rep.Warnf("coordinator: %s's spool holds files that cannot be read as messages and are NOT counted as pending: %v", role, err)
	}
	return len(res.Entries)
}

// sweepSpoolDir reads one spool directory, distinguishing "not there" (no
// messages, no complaint) from a real failure (loud, counted). ok=false means
// the caller has nothing to process.
func (c *Coordinator) sweepSpoolDir(harp string, dir spool.Dir, why string) (spool.SweepResult, bool) {
	return c.sweepSpoolDirWith(harp, dir, why, spool.Sweep)
}

// sweepSpoolDirNames is sweepSpoolDir with the read-and-parse contract
// dropped: it lists the directory and validates filenames only, never opening
// a file's body. It exists for a directory where the filename IS the whole
// signal — see spool.SweepNames — and it shares every non-body-reading part
// of sweepSpoolDir's behaviour (path resolution, the not-there/real-failure
// distinction, the warn-and-count-failed path) by routing through the same
// function with only the sweep primitive swapped, so those cannot drift
// between the two modes.
func (c *Coordinator) sweepSpoolDirNames(harp string, dir spool.Dir, why string) (spool.SweepResult, bool) {
	return c.sweepSpoolDirWith(harp, dir, why, spool.SweepNames)
}

// sweepSpoolDirWith is the shared body of sweepSpoolDir and
// sweepSpoolDirNames, parameterized on which spool primitive actually reads
// the directory.
func (c *Coordinator) sweepSpoolDirWith(harp string, dir spool.Dir, why string, sweep func(spool.PathMapper, string, spool.Dir) (spool.SweepResult, error)) (spool.SweepResult, bool) {
	mapper := c.mapper
	path, err := spool.DirPath(mapper, harp, dir)
	if err != nil {
		c.rep.Warnf("coordinator: cannot resolve %s's %s spool (%s): %v", harp, dir, why, err)
		c.spoolDeliveryCount.Failed.Add(1)
		return spool.SweepResult{}, false
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return spool.SweepResult{Dir: dir}, false
	}
	res, err := sweep(mapper, harp, dir)
	if err != nil {
		c.rep.Warnf("coordinator: sweeping %s's %s spool (%s): %v", harp, dir, why, err)
		c.spoolDeliveryCount.Failed.Add(1)
		return spool.SweepResult{}, false
	}
	return res, true
}

// startSpoolReactor brings up the coordinator's spool reader.
func (c *Coordinator) startSpoolReactor() {
	c.spoolSeen = map[string]map[string]bool{}
	c.spoolReactor = NewSpoolReactor(c.sweepChildSpool, c.spoolRoles, c.spoolSweepInterval)
	// The reactor is registered AS the doorbell's consumer rather than being
	// called beside it. One seam: a second consumer cannot be added without
	// visibly replacing this one, and a nil handler stays a real fault rather
	// than becoming a second, silent delivery path.
	c.SetSpoolDoorbellHandler(func(role string, _ spool.Ref) { c.spoolReactor.Mark(role) })
	c.goTracked(func() { c.spoolReactor.Run(c.baseCtx) })
}

// spoolRoles is the reconciliation set: every harp this coordinator has a run
// record for, plus the session owner. It is deliberately wider than
// "currently attached" — the whole point of the startup pass is to find mail
// written for a run whose channel does not exist yet, or exists no longer.
//
// THE OWNER IS A SENDER TOO: its engine reaches this coordinator through the
// owner's own runner (the plugin-hosted owner arm), whose agent_send is a
// file in the OWNER's out/ with no coordinator round trip — the same local
// write every child's runner makes. The owner has no run record (it is
// identified by declaration, Options.OwnerHarp), so a set built from run
// records alone left the owner's every send in place, reported to the
// engine as queued.
func (c *Coordinator) spoolRoles() []string {
	var out []string
	c.runs.View(func() { out = c.runsF.currentHarps() })
	if c.ownerHarp != "" {
		out = append(out, c.ownerHarp)
	}
	return out
}

// sweepChildSpool is the coordinator's whole reading job for one spool: route
// what its owner SENT (out/), and — for a child — note what it CONSUMED
// (in/consumed). The session owner's spool gets only the first half: its
// acks forgive a relaunch budget, and this coordinator never relaunches its
// own owner.
func (c *Coordinator) sweepChildSpool(role string) {
	c.sweepChildOut(role)
	if c.ownerSpool(role) {
		return
	}
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
		c.rep.Warnf("coordinator: %s wrote a spool file that is not a message and will not be routed: %v", role, p.Error())
		c.spoolDeliveryCount.Failed.Add(1)
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
		c.rep.Warnf("coordinator: %s's spool holds an outbound message but that harp has no run record; leaving %s in place", role, e.Ref)
		c.spoolDeliveryCount.Failed.Add(1)
		return
	}
	msg, err := MailFromSpool(e, role)
	if err != nil {
		c.rep.Warnf("coordinator: refusing an unroutable message from %s: %v", role, err)
		c.spoolDeliveryCount.Failed.Add(1)
		c.noticeSpoolDrop(role, e, err)
		c.failSpoolOut(role, e.Ref, err)
		return
	}
	if _, _, err := c.peerSend(sender, msg.To, msg.Kind, msg.Body, msg.Structured, msg.InReplyTo); err != nil {
		// The routing chokepoint refused it (closed kind vocabulary,
		// hub-and-spoke, unknown recipient). The agent's local write already
		// returned success, so the refusal is reported back the only way that
		// still reaches it: as mail.
		c.rep.Warnf("coordinator: refusing %s's spool message %s: %v", role, e.Ref, err)
		c.spoolDeliveryCount.Failed.Add(1)
		c.replySpoolRefusal(role, msg, err)
		c.noticeSpoolDrop(role, e, err)
		c.failSpoolOut(role, e.Ref, err)
		return
	}
	c.spoolDeliveryCount.Delivered.Add(1)
	c.consumeSpool(role, e.Ref)
}

// failSpoolOut is routeSpoolOut's terminal outcome for an out/ entry this
// coordinator could not route: the file leaves out/ for the local out/failed/
// directory instead of out/consumed/.
//
// The distinction is the whole point, and it is the runner-side failSpoolEntry
// invariant applied to the direction that never had it. out/consumed/ means
// ROUTED — that is what the substrate's own contract says a consume-rename is,
// and what every reader of that directory assumes. A dropped message renamed
// there is a delivered one as far as disk is concerned: an operator, and an
// investigator reading the spool after the fact, cannot tell a report the
// coordinator handed to its parent from one it gave up on. That is not a
// hypothetical reading; it is how a lost report was written off as an agent
// that never reported.
//
// Leaving the file in out/ instead is not the alternative: the reader would
// re-parse it, re-fail it and re-warn about it on every sweep for the life of
// the process while later entries delivered around it, which is the
// silent-skip this project treats as its characteristic defect.
func (c *Coordinator) failSpoolOut(role string, ref spool.Ref, cause error) {
	FailSpool(c.rep, c.mapper, "coordinator", ref, fmt.Sprintf("could not route %s's message", role), cause)
}

// FailSpool moves ref out of its live directory into the failed/ sibling
// (spool.Fail picks which) and reports the outcome either way — the ONE
// terminal-state move for a file a reader parsed but could not deliver or
// route, on both sides and in both directions. A lost race (ErrAlreadyGone)
// is the other path having won: nothing to strand, nothing to warn about.
func FailSpool(rep report.Reporter, mapper spool.PathMapper, side string, ref spool.Ref, why string, cause error) {
	if err := spool.Fail(mapper, ref); err != nil {
		if errors.Is(err, spool.ErrAlreadyGone) {
			return
		}
		rep.Warnf("%s: %s: %v (also could not move %s to its failed/ directory: %v; it will be re-read, and re-refused, on the next sweep)",
			side, why, cause, ref, err)
		return
	}
	rep.Warnf("%s: %s: %v (moved %s to its failed/ directory; it will NOT be retried)", side, why, cause, ref)
}

// replySpoolRefusal tells a child that the message it wrote could not be
// routed. Without it a refused send is invisible to the agent: its local write
// succeeded, and the refusal happens later in another process.
func (c *Coordinator) replySpoolRefusal(role string, msg Message, cause error) {
	body := fmt.Sprintf("your message to %q was not delivered: %v", msg.To, cause)
	if _, err := c.queueMail(role, role, KindError, body); err != nil {
		c.rep.Warnf("coordinator: could not tell %s that its message was refused (%v): %v", role, cause, err)
	}
}

// noticeSpoolDrop tells the WAITING PARTY that a message its child wrote has
// been dropped, and hands over the text the child actually wrote.
//
// This is the half the refusal path was missing, and it is the half the
// incident was made of. A child writes its final report, the sweep cannot
// route it, replySpoolRefusal answers the SENDER — a session that has by then
// usually exited, whose reply therefore lands in a spool directory nothing
// will ever read again — the file is consumed, and the parent sits in
// agent_recv until it times out and concludes the child never reported. The
// only trace is a clidiag warning on a runner's stderr. Every signal the
// parent can see says the child was silent.
//
// So the notice goes UP, to the party whose work depends on the answer. It is
// synthesized by the coordinator exactly as KindExited is, and for the same
// stated reason: the parent always learns. Authorship stays honest — the
// message is queued FROM the child, because the text below the header is the
// child's own words — and it borrows no authority the child did not already
// have, since KindError is in the sender-allowed vocabulary and the parent is
// a child's only legal recipient anyway.
//
// The original TEXT is carried, not just the fact of the drop. A notice that
// said only "a message was lost" would tell a coordinator to go and ask an
// agent that no longer exists; carrying the body means a report that could not
// be routed is still READ, which is the outcome that actually matters.
func (c *Coordinator) noticeSpoolDrop(role string, e spool.Entry, cause error) {
	parent := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(role); r != nil {
			parent = r.ParentHarp
		}
	})
	if parent == "" {
		c.rep.Warnf("coordinator: dropped %s and cannot tell anyone: %s has no parent on record (%v)", e.Ref, role, cause)
		return
	}
	kind, body := SpoolKindUnkinded, ""
	if e.Message != nil {
		kind, body = e.Message.Kind, e.Message.Body
	}
	notice := fmt.Sprintf(
		"UNDELIVERED: a %q message %s wrote to %q could not be routed and has been dropped: %v\n"+
			"(spool file %s; its sender was told, but a session that has ended cannot read that reply)\n"+
			"\n--- the message text, as %s wrote it ---\n%s",
		kind, role, spoolAddressee(e), cause, e.Ref, role, body)
	if _, err := c.queueMail(role, parent, KindError, notice); err != nil {
		c.rep.Warnf("coordinator: dropped %s and could not tell %s about it: %v (the original cause was %v)", e.Ref, parent, err, cause)
	}
}

// spoolAddressee renders who a dropped file claimed to be for. The frontmatter
// `to` is the file's own claim and never a routing input (SENDER IDENTITY IS
// THE DIRECTORY); it is quoted here only so the notice can say what the sender
// believed it was doing.
func spoolAddressee(e spool.Entry) string {
	if e.Message == nil || e.Message.To == "" {
		return "(no recipient)"
	}
	return e.Message.To
}

// spoolSenderIdentity resolves a spool directory's owning harp to the identity
// its messages are sent under — the file-plane analog of deriving a caller's
// identity from its credential rather than from anything it wrote. The
// session owner's out/ is the owner's identity (depth 0, declared — see
// spoolRoles); every other harp's is its current run record.
func (c *Coordinator) spoolSenderIdentity(role string) (Identity, bool) {
	if c.ownerSpool(role) {
		return Identity{Harp: role, Depth: 0, Project: c.projectDir}, true
	}
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
	done, err := spool.Consume(c.mapper, ref)
	if err != nil {
		if errors.Is(err, spool.ErrAlreadyGone) {
			return
		}
		c.rep.Warnf("coordinator: routed %s but could not mark it consumed: %v (it will be routed again on the next sweep)", ref, err)
		c.spoolDeliveryCount.Failed.Add(1)
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
	res, ok := c.sweepSpoolDirNames(role, spool.DirInConsumed, "reading delivery acknowledgements")
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
	c.spoolDeliveryCount.Consumed.Add(uint64(fresh))
	c.noteMailConsumed(role) // real progress: the relaunch budget is forgiven
}

// SpoolDeliveryStats reports this coordinator's cumulative file-plane
// outcomes.
func (c *Coordinator) SpoolDeliveryStats() SpoolDeliveryStats { return c.spoolDeliveryCount.Stats() }

// SpoolDeliveryStats reports what the file mail plane did and could not do.
// Every counter is cumulative for the process's lifetime.
type SpoolDeliveryStats struct {
	// Delivered counts messages this side handed to its own surface: turns or
	// recv batches on a runner, routed sends on a coordinator.
	Delivered uint64
	// Consumed counts consume-renames observed or performed.
	Consumed uint64
	// Failed counts everything that did not get through — an unreadable file,
	// an unroutable message, a rename that errored. Each one is a message
	// that has not arrived.
	Failed uint64
}

type SpoolDeliveryCounters struct {
	Delivered atomic.Uint64
	Consumed  atomic.Uint64
	Failed    atomic.Uint64
}

func (s *SpoolDeliveryCounters) Stats() SpoolDeliveryStats {
	return SpoolDeliveryStats{
		Delivered: s.Delivered.Load(),
		Consumed:  s.Consumed.Load(),
		Failed:    s.Failed.Load(),
	}
}

// ---- runner side -------------------------------------------------------

// ErrNeedsOwner refuses a coordinator that was not told whose inbox it drains
// (Options.OwnerHarp): every child->parent message is a file in the owner's
// in/, and an owner nobody declared is a directory nobody reads.
var ErrNeedsOwner = errors.New("coord: the coordinator needs the session owner's harp (Options.OwnerHarp): the owner's inbox is a spool and this process is its reader")
