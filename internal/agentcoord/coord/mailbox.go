package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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
	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailQueued, c.now(), mailQueued{
			MessageID: msg.ID, From: from, To: to, Kind: kind, Body: body,
			Structured: structured, InReplyTo: inReplyTo,
		})}, nil
	}); err != nil {
		return "", false, err
	}
	if c.deliverToPoll(to, msg) {
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

// deliverToPoll hands msg to the role's parked poll if one is waiting,
// reserving the message id in the runtime delivery ledger (it stays pending
// in the fold until a subsequent recv acks it — at-least-once). Completion
// (including the unpark slot re-acquisition) runs asynchronously so the
// sender never blocks on the recipient's slot.
func (c *Coordinator) deliverToPoll(role string, msg Message) bool {
	c.mu.Lock()
	p := c.polls[role]
	if p == nil || p.done {
		c.mu.Unlock()
		return false
	}
	p.done = true
	delete(c.polls, role)
	c.delivered[role] = append(c.delivered[role], msg.ID)
	c.mu.Unlock()
	go func() {
		c.onRoleUnpark(role)
		p.ch <- pollResult{msgs: []Message{msg}}
	}()
	return true
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

	c.mu.Lock()
	if msgs := c.undeliveredLocked(role); len(msgs) > 0 {
		for _, m := range msgs {
			c.delivered[role] = append(c.delivered[role], m.ID)
		}
		c.mu.Unlock()
		return msgs, nil
	}
	if wait <= 0 {
		c.mu.Unlock()
		return nil, ErrRecvTimeout
	}
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
		return r.msgs, r.err
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
		if callerGone && len(r.msgs) > 0 {
			ids := make([]string, 0, len(r.msgs))
			for _, m := range r.msgs {
				ids = append(ids, m.ID)
			}
			c.unreserve(role, ids)
			return nil, err
		}
		return r.msgs, r.err
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
