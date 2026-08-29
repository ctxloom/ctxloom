package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Typed mailbox failures — the recv timeout is a contract, not a fault: the
// caller is expected to drop the coordination and write its report/deferral
// state. (Vocabulary carried over from the retired agentbus broker.)
var (
	// ErrRecvTimeout completes a parked agent_recv whose bounded wait
	// elapsed with no message.
	ErrRecvTimeout = errors.New("agent_recv: timed out with no message; drop the coordination, write your report/deferral state, and finish")
	// ErrPeerRouting rejects executor→executor addressing (hub-and-spoke).
	ErrPeerRouting = errors.New(`agent_send: executors may only address "parent"; route via coordinator`)
	// ErrRecvPreempted completes the OLDER of two long-polls for one role:
	// one active long-poll per role, newest preempts.
	ErrRecvPreempted = errors.New("agent_recv: preempted by a newer receive for this session")
	// ErrRevoked completes a parked long-poll whose credential was revoked
	// (run ended / agent_stop): revocation severs parked polls.
	ErrRevoked = errors.New("agent_recv: this session's credential was revoked")
)

// ParentAddress is the one recipient a spawned child may address: its own
// coordinator's session, resolved from journaled lineage.
const ParentAddress = "parent"

// UserSender is the sender identity on messages the human injects through the
// observation viewer — never a harp, so a recipient can tell a user message
// from its parent's.
const UserSender = "user"

// KindUserInjected marks the O3 mirror notice a user injection always sends
// to the target's parent.
const KindUserInjected = "user_injected"

// KindExited marks the synthesized terminal notice the coordinator queues to
// a parent when a child run ends (runner loss, chat-stream close, stop) —
// the orchestrator, and now the parent, always learns.
const KindExited = "exited"

// parkedPoll is one held agent_recv long-poll. done flips under c.mu so the
// deliver/timeout/preempt/revoke races resolve to exactly one completion.
type parkedPoll struct {
	done bool
	ch   chan pollResult
}

// pollResult is what completes a parked poll's channel.
//
// msgs is set ONLY by deliverTerminalFallback — the one path that hands a
// message down a poll's channel directly, because it is deliberately
// non-durable (queueMail already failed) and so has no fold entry for a claim
// to find. Every DURABLE delivery (deliverToPoll) sends a bare wake
// (msgs==nil, err==nil): the payload lives in the mailbox fold, and
// tryClaimDeliverable — called by whichever goroutine is actually about to
// hand messages back to a still-live caller — is the one place a claim on it
// is ever made. That is what keeps "woken" and "received" from being
// conflated: a wake sent to a poll nobody drains reserves nothing, so it
// costs nothing and the next recv finds the mail exactly where it left it.
type pollResult struct {
	msgs []Message
	err  error
}

// newMessageID mints a mailbox message id (the dedupe key).
func newMessageID() string { return randID("m-", 12) }

// queueMail is queueMailPayload's common-case wrapper: no structured
// companion, no reply correlation.
func (c *Coordinator) queueMail(from, to, kind, body string) (msgID string, completed bool, err error) {
	return c.queueMailPayload(from, to, kind, body, nil, "")
}

// queueMailPayload durably queues one message (fsynced before return), then
// completes the recipient's waiting receive when one is open: a LOCAL parked
// long-poll (bare-mcp path), or — tentatively — the recipient runner's
// parked agent_recv via a pushed CoordinatorNotice (the cursor advances only
// on the runner's mail_consumed fact). completed reports either. Routing
// policy is the caller's. structured is an optional JSON-object companion
// (e.g. the escalation ladder's relayed ApprovalRequest projection, Wave
// C2); inReplyTo correlates this message to an earlier one's id.
func (c *Coordinator) queueMailPayload(from, to, kind, body string, structured json.RawMessage, inReplyTo string) (msgID string, completed bool, err error) {
	return c.queueMailPayloadID(newMessageID(), from, to, kind, body, structured, inReplyTo)
}

// queueMailPayloadID is queueMailPayload with the message id supplied by the
// caller. It exists for correlation-carrying mail whose id must be REGISTERED
// somewhere before the mail is observable: this function publishes, and after
// it returns (indeed, from inside it — a parked recv completes synchronously)
// a reply quoting the id can already arrive. relayApproval is the case that
// forced it; see its comment.
func (c *Coordinator) queueMailPayloadID(msgID, from, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, bool, error) {
	// Role "" is undrainable by construction — agent_recv drains the caller's
	// own harp and no session has the empty harp — so queuing it fsyncs a fact
	// that is replayed on every relaunch and read by nobody. Refused here, at
	// the one point every sender funnels through, rather than at each sender.
	if to == "" {
		return "", false, fmt.Errorf("coordinator mail: refusing to queue a %q message from %q with no recipient: no session can drain role %q", kind, from, to)
	}
	// A message with NO payload is refused at the same chokepoint. Queued, it is
	// journaled as a durable fact, completes a parked recv, and is answered with
	// the ordinary success disposition — a recipient woken for a turn whose
	// content is nothing at all, with every signal green. A structured companion
	// IS payload (the relayed ApprovalRequest projection and its replies carry
	// it), so only a message with neither is empty.
	if strings.TrimSpace(body) == "" && len(structured) == 0 {
		return "", false, fmt.Errorf("mailbox: refusing to queue an empty message from %q to %q (kind %q): "+
			"it carries no text and no structured payload, so the recipient would be woken with nothing to act on "+
			"(check the sender's message composition)", from, to, kind)
	}
	msg := Message{ID: msgID, From: from, To: to, Kind: kind, Body: body, Structured: structured, InReplyTo: inReplyTo}
	// THE CUTOVER (spooldelivery.go). For a recipient delivered by FILE the
	// spool write IS the delivery: no queue fact, no poll completion, no push.
	// It is branched here, before the journal, rather than teed after it —
	// under the cutover there is no second copy to keep, and a mailbox fact
	// nobody would ever consume is a message the fold reports as pending
	// forever.
	if c.spoolDeliverTo(to) {
		return c.deliverMailViaSpool(msg)
	}
	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailQueued, c.now(), mailQueued{
			MessageID: msg.ID, From: from, To: to, Kind: kind, Body: body,
			Structured: structured, InReplyTo: inReplyTo,
		})}, nil
	}); err != nil {
		return "", false, err
	}
	// The SHADOW TEE (spooltee.go), at the one point every sender funnels
	// through, and AFTER the durable queue fact — the moment the coordinator
	// has actually committed this mail. It is a no-op unless the tee is
	// enabled, it never fails this delivery, and it changes nothing about what
	// follows: reads still come from the mailbox in S4.
	c.teeMailToRun(to, msg)
	if c.deliverToPoll(to) {
		return msg.ID, true, nil
	}
	// Push-down: a recipient whose runner-side recv is parked gets the mail
	// pushed as a notice (tentative delivery; at-least-once) — and a
	// MIGRATED child's live channel is always pushable: its runner
	// delivers by state (§6a — parked recv, new turn, or queue to the
	// boundary). Only a completed recv reports completed=true; a turn
	// delivery's disposition is the caller's (driveQueued by state).
	c.mu.Lock()
	ch := c.chans[to]
	rt := c.byHarp[to]
	parked := ch != nil && ch.parked
	pushable := parked || (ch != nil && rt != nil && rt.viaStartRun)
	c.mu.Unlock()
	if pushable {
		c.pushMail(to)
		return msg.ID, parked, nil
	}
	return msg.ID, false, nil
}

// deliverToPoll WAKES role's parked poll if one is waiting — it does not
// reserve anything and does not hand the payload through the channel. The
// wake means "your mail is in the fold, go claim it"; tryClaimDeliverable is
// the one place a claim (and so a reservation) is ever made, by whichever
// goroutine is actually about to return it to a still-live caller.
//
// This is deliberate: reserving HERE, at hand-off, would mark the message
// ack-eligible for a channel that a preempting recv, or an MCP client that
// simply stopped listening without cancelling anything, may never drain —
// exactly the shape that let ackDelivered journal a message consumed before
// anyone had received it (B2). Completion (including the unpark slot
// re-acquisition) runs asynchronously so the sender never blocks on the
// recipient's slot.
func (c *Coordinator) deliverToPoll(role string) bool {
	c.mu.Lock()
	p := c.polls[role]
	if p == nil || p.done {
		c.mu.Unlock()
		return false
	}
	p.done = true
	delete(c.polls, role)
	c.mu.Unlock()
	go func() {
		c.onRoleUnpark(role)
		p.ch <- pollResult{}
	}()
	return true
}

// tryClaimDeliverable reserves and returns role's currently deliverable mail,
// if any — the ONE place a hand-off to a live caller becomes real. It is what
// a wake (deliverToPoll) is redeemed against, and it is what the top of every
// recvMail call checks first. ok=false means there is nothing to claim right
// now: either genuinely no mail, or another claim already won the race (a
// second wake for the same delivery, or an overlapping recv).
func (c *Coordinator) tryClaimDeliverable(role string) ([]Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := c.undeliveredLocked(role)
	if len(msgs) == 0 {
		return nil, false
	}
	for _, m := range msgs {
		c.delivered[role] = append(c.delivered[role], m.ID)
	}
	return msgs, true
}

// deliverTerminalFallback completes role's parked agent_recv poll with msg
// WITHOUT journaling or reserving it — the last-resort path terminateRun takes
// when the DURABLE queueMail for a child's terminal notice failed. The durable
// write is exactly what just failed, so this is non-durable by construction: a
// best-effort unblock of a parent parked in agent_recv RIGHT NOW, which would
// otherwise hang until its own timeout with no indication its child died. It
// reuses the same async unpark+send shape as deliverToPoll (so the caller never
// blocks on the recipient's slot re-acquire) but skips the delivery ledger — an
// unjournaled id reserved there would make a later ackDelivered journal a
// mail_consumed fact for a message no mailQueued fact ever recorded. Reports
// whether a parked poll was completed; a parent not currently parked cannot be
// reached this way (the caller logs that residual gap loudly).
func (c *Coordinator) deliverTerminalFallback(role string, msg Message) bool {
	c.mu.Lock()
	p := c.polls[role]
	if p == nil || p.done {
		c.mu.Unlock()
		return false
	}
	p.done = true
	delete(c.polls, role)
	c.mu.Unlock()
	go func() {
		c.onRoleUnpark(role)
		p.ch <- pollResult{msgs: []Message{msg}}
	}()
	return true
}

// undeliveredLocked lists the role's pending messages minus the runtime
// delivery ledger (delivered-but-unacked). Caller holds c.mu.
//
// The filter builds its OWN slice rather than compacting in place. Compacting
// into what the fold handed back only works while mailFold.pendingFor returns a
// copy — an invariant that lives in another file, guards a mutation of the
// fold's live queue, and would fail silently if pendingFor were ever made to
// return its backing array. The message count here is a mailbox
// depth, not a hot loop; a fold whose state the read path can corrupt is not
// worth the saved allocation.
func (c *Coordinator) undeliveredLocked(role string) []Message {
	reserved := make(map[string]bool, len(c.delivered[role]))
	for _, id := range c.delivered[role] {
		reserved[id] = true
	}
	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor(role) })
	out := make([]Message, 0, len(pending))
	for _, m := range pending {
		if !reserved[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// pendingCount reports how many messages could still be delivered to role —
// the ended-child check: leftover mail triggers a resume, never strands.
func (c *Coordinator) pendingCount(role string) int {
	// Under the cutover the fold holds nothing for this role — the files do.
	// Reading the mailbox here would report a permanent zero and strand every
	// leftover-mail resume and standup drain that depends on this answer.
	if c.spoolDeliverTo(role) {
		return c.spoolPendingCount(role)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.undeliveredLocked(role))
}

// takeNextMail removes and returns the oldest deliverable message for role —
// the turn-boundary drain (§6a: a message queued mid-turn is delivered at the
// next boundary as a new turn). Delivery INTO a turn the coordinator itself
// drives is consumed at take (there is no recv to ack it); the crash window
// between take and the engine seeing the turn is accepted and documented —
// the recv path is where the at-least-once guarantee lives.
//
// The error return is load-bearing. "No mail" and "the take
// FAILED" are different facts and every caller acts on them differently: a
// caller that reads a failure as an empty mailbox drives a child with no
// prompt, or parks it idle holding mail the fold still calls deliverable.
// Callers must never treat an error as false.
func (c *Coordinator) takeNextMail(role string) (Message, bool, error) {
	c.mu.Lock()
	deliverable := c.undeliveredLocked(role)
	if len(deliverable) == 0 {
		c.mu.Unlock()
		return Message{}, false, nil
	}
	msg := deliverable[0]
	// Reserve under the lock so a racing poll delivery cannot double-take.
	c.delivered[role] = append(c.delivered[role], msg.ID)
	c.mu.Unlock()

	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailConsumed, c.now(), mailConsumed{Role: role, MessageIDs: []string{msg.ID}})}, nil
	}); err != nil {
		// RELEASE THE RESERVATION. The id was reserved above so a
		// racing poll delivery could not double-take it; if the consume never
		// journaled, nothing consumed it, and leaving it reserved makes the
		// message permanently INVISIBLE — undeliveredLocked filters reserved
		// ids out and nothing else ever clears this one (severChan un-reserves
		// only ch.pushed, ackDelivered only ids it copied).
		c.unreserve(role, []string{msg.ID})
		return Message{}, false, fmt.Errorf("mailbox: journaling the consume of message %s for %s failed, the message is left queued: %w", msg.ID, role, err)
	}
	c.unreserve(role, []string{msg.ID})
	c.noteMailConsumed(role) // real progress: the relaunch budget is forgiven
	return msg, true, nil
}

// requeueUndelivered puts back a message takeNextMail already journaled as
// CONSUMED but that could not be handed to the child (sendTurn's two drop
// paths: an input channel already closed, and coordinator shutdown).
// takeNextMail's own doc accepts a crash window between the take and the
// engine seeing the turn; it does NOT accept the coordinator noticing the
// non-delivery and doing nothing about it, which is what a bare return was.
//
// The requeue carries a FRESH message id. mailFold dedupes queued facts on
// message_id, so re-queuing under the original id folds to nothing at all
// and the message stays exactly as lost as before. Everything a recipient
// acts on — sender, kind, body, structured companion, reply correlation —
// is preserved.
//
// It lands at the BACK of the role's queue. The mailbox fold has no
// re-insert-at-position fact and inventing one is a change to a persisted
// format, so ARRIVAL ORDER between a redelivery and mail queued during the
// drop window is not preserved. Delivery is, and delivery is the guarantee
// that was broken.
//
// A requeue that cannot itself be journaled is the end of the line: it is
// reported as loudly as a lost message deserves, naming the message and its
// sender, rather than being swallowed.
func (c *Coordinator) requeueUndelivered(role string, msg Message) {
	newID, _, err := c.queueMailPayloadID(newMessageID(), msg.From, role, msg.Kind, msg.Body, msg.Structured, msg.InReplyTo)
	if err != nil {
		clidiag.Warn("ctxloom", "agent %s: message %s from %s was consumed but never delivered, and re-queueing it FAILED (%v) — this message is lost",
			role, msg.ID, msg.From, err)
		return
	}
	clidiag.Warn("ctxloom", "agent %s: message %s from %s could not be delivered to the child; re-queued as %s",
		role, msg.ID, msg.From, newID)
}

// unreserve drops ids from the runtime delivery ledger (they are consumed in
// the fold now).
func (c *Coordinator) unreserve(role string, ids []string) {
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	c.mu.Lock()
	kept := c.delivered[role][:0]
	for _, id := range c.delivered[role] {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		delete(c.delivered, role)
	} else {
		c.delivered[role] = kept
	}
	c.mu.Unlock()
}

// ackDelivered appends the consume fact for everything previously delivered
// to role — the cursor-ack a SUBSEQUENT recv carries (consume
// facts append only then; a crash before the ack re-delivers, at-least-once).
func (c *Coordinator) ackDelivered(role string) error {
	c.mu.Lock()
	ids := append([]string(nil), c.delivered[role]...)
	c.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailConsumed, c.now(), mailConsumed{Role: role, MessageIDs: ids})}, nil
	}); err != nil {
		return err
	}
	c.unreserve(role, ids)
	c.noteMailConsumed(role) // real progress: the relaunch budget is forgiven
	return nil
}

// recvMail is the long-poll behind agent_recv for one role: ack prior
// deliveries, drain deliverable mail, or park for up to wait. One active
// long-poll per role; a newer receive preempts the parked one (ErrRecvPreempted).
func (c *Coordinator) recvMail(ctx context.Context, role string, wait time.Duration) ([]Message, error) {
	if err := c.ackDelivered(role); err != nil {
		return nil, err
	}

	if msgs, ok := c.tryClaimDeliverable(role); ok {
		return msgs, nil
	}
	if wait <= 0 {
		return nil, ErrRecvTimeout
	}
	c.mu.Lock()
	prev := c.polls[role]
	fresh := prev == nil || prev.done
	if !fresh {
		// Newest preempts: the older poll completes with a typed error. No
		// park-hook churn — the role stays parked, only the waiter swaps.
		prev.done = true
		go func() { prev.ch <- pollResult{err: ErrRecvPreempted} }()
	}
	p := &parkedPoll{ch: make(chan pollResult, 1)}
	c.polls[role] = p
	c.mu.Unlock()

	if fresh {
		c.onRolePark(role)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		return c.resolvePollWake(role, r)
	case <-timer.C:
		// The timer expiring does not end the CALLER: it is still waiting for
		// this call's return value, so a delivery that won the race is handed
		// to it.
		return c.abandonPoll(role, p, ErrRecvTimeout, false)
	case <-ctx.Done():
		// A cancelled context DOES end the caller — nothing it returns can be
		// received — so a delivery that won the race has to be released.
		return c.abandonPoll(role, p, ctx.Err(), true)
	}
}

// resolvePollWake turns a completed poll's result into what THIS call
// returns, and is the one place a bare wake (deliverToPoll) gets redeemed
// into an actual claim — the call receiving it is, by construction, still
// live (it is mid-select on this very channel), so a claim made here is a
// genuine hand-off, safe for the next recv's ackDelivered to trust.
func (c *Coordinator) resolvePollWake(role string, r pollResult) ([]Message, error) {
	if r.err != nil {
		return nil, r.err
	}
	if len(r.msgs) > 0 {
		return r.msgs, nil // deliverTerminalFallback's non-durable direct payload
	}
	msgs, ok := c.tryClaimDeliverable(role)
	if !ok {
		// Woken, but something else (another claim) already took the mail.
		return nil, ErrRecvTimeout
	}
	return append(msgs, c.settleBurst(role)...), nil
}

// A spoolReactor pass routes EVERY swept child's report serially, so N children
// finishing inside one sweep window arrive as N queue operations microseconds
// apart. A wake redeemed the instant the first lands claims exactly that one,
// and entries 2..N are then queued with no poll parked — deliverToPoll returns
// false, and for a session owner's own harp pushMail has no channel to fall
// back to. They wait for the caller's NEXT receive, with nothing in the
// response able to say they exist.
//
// So a redeemed wake keeps claiming until the arrivals stop, and only the
// caller that is already returning pays for it.
const (
	// mailSettleQuiet is how long the burst must be silent before the batch is
	// considered whole.
	mailSettleQuiet = 40 * time.Millisecond
	// mailSettleTick is how often the window is re-checked.
	mailSettleTick = 5 * time.Millisecond
	// mailSettleCap bounds the total wait, so a role receiving continuously
	// cannot hold its own caller open indefinitely. Reaching it is not an
	// error: whatever was claimed is returned, and the remainder is still
	// deliverable to the next receive.
	mailSettleCap = 400 * time.Millisecond
)

// settleBurst collects the rest of an in-flight arrival burst for role, having
// already claimed its first message. It returns only what it additionally
// claimed, and never an error: a settle that finds nothing simply means the
// burst was one message, which is the common case.
func (c *Coordinator) settleBurst(role string) []Message {
	var extra []Message
	hardStop := time.Now().Add(mailSettleCap)
	quietUntil := time.Now().Add(mailSettleQuiet)

	for time.Now().Before(quietUntil) && time.Now().Before(hardStop) {
		time.Sleep(mailSettleTick)
		more, ok := c.tryClaimDeliverable(role)
		if !ok {
			continue
		}
		extra = append(extra, more...)
		// Progress restarts the quiet period: a burst is only whole once
		// nothing new has arrived for a full window.
		quietUntil = time.Now().Add(mailSettleQuiet)
	}
	return extra
}

// abandonPoll resolves the timeout/cancel race against a concurrent delivery:
// if the delivery already won (done), its completion is authoritative — wait
// for it; otherwise claim the poll, re-acquire the slot (unpark), and fail
// with err.
//
// callerGone says whether the recv's caller can still receive what this
// returns. It cannot when the caller's own context was cancelled, and a
// delivery that won the race is then a delivery to NOBODY: the id is reserved
// in the runtime ledger, so the next recv's cursor-ack (ackDelivered) would
// journal a mail-consumed fact for a message no agent ever saw, and
// undeliveredLocked would filter it out forever. Releasing the reservation is
// what keeps at-least-once true for the one case where the message was already
// spoken for; the timeout path keeps it, because there the caller is still
// there to be given it.
func (c *Coordinator) abandonPoll(role string, p *parkedPoll, err error, callerGone bool) ([]Message, error) {
	c.mu.Lock()
	if p.done {
		c.mu.Unlock()
		r := <-p.ch
		if r.err != nil {
			return nil, r.err
		}
		if len(r.msgs) > 0 {
			// deliverTerminalFallback's non-durable direct payload: never
			// reserved, so a gone caller simply never sees it — there is
			// nothing to release.
			if callerGone {
				return nil, err
			}
			return r.msgs, nil
		}
		// A bare wake (deliverToPoll): claiming is the ONLY thing that
		// reserves anything, so a caller that is gone must not claim on its
		// behalf — that would reserve (and let the next recv's ackDelivered
		// journal as consumed) a message nobody was left to receive. Leaving
		// it unclaimed is what keeps it deliverable.
		if callerGone {
			return nil, err
		}
		if msgs, ok := c.tryClaimDeliverable(role); ok {
			return msgs, nil
		}
		return nil, err
	}
	p.done = true
	if c.polls[role] == p {
		delete(c.polls, role)
	}
	c.mu.Unlock()
	c.onRoleUnpark(role)
	return nil, err
}

// severPoll completes a role's parked poll with err WITHOUT the unpark slot
// re-acquisition — used by credential revocation (the session is gone; its
// slot accounting is settled by the terminal path).
func (c *Coordinator) severPoll(role string, err error) {
	c.mu.Lock()
	p := c.polls[role]
	if p == nil || p.done {
		c.mu.Unlock()
		return
	}
	p.done = true
	delete(c.polls, role)
	c.mu.Unlock()
	p.ch <- pollResult{err: err}
}
