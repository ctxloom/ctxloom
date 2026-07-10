package coord

import (
	"context"
	"errors"
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
	// one active long-poll per role, newest preempts (review R6).
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
// blue-paper (2): the orchestrator, and now the parent, always learns.
const KindExited = "exited"

// parkedPoll is one held agent_recv long-poll. done flips under c.mu so the
// deliver/timeout/preempt/revoke races resolve to exactly one completion.
type parkedPoll struct {
	role string
	done bool
	ch   chan pollResult
}

type pollResult struct {
	msgs []Message
	err  error
}

// newMessageID mints a mailbox message id (the dedupe key).
func newMessageID() string { return randID("m-", 12) }

// queueMail durably queues one message (fsynced before return), then
// completes the recipient's waiting receive when one is open: a LOCAL parked
// long-poll (bare-mcp path), or — tentatively — the recipient runner's
// parked agent_recv via a pushed CoordinatorNotice (the cursor advances only
// on the runner's mail_consumed fact). completed reports either. Routing
// policy is the caller's.
func (c *Coordinator) queueMail(from, to, kind, body string) (msgID string, completed bool, err error) {
	msg := Message{ID: newMessageID(), From: from, To: to, Kind: kind, Body: body}
	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailQueued, c.now(), mailQueued{
			MessageID: msg.ID, From: from, To: to, Kind: kind, Body: body,
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

// undeliveredLocked lists the role's pending messages minus the runtime
// delivery ledger (delivered-but-unacked). Caller holds c.mu.
func (c *Coordinator) undeliveredLocked(role string) []Message {
	reserved := make(map[string]bool, len(c.delivered[role]))
	for _, id := range c.delivered[role] {
		reserved[id] = true
	}
	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor(role) })
	out := pending[:0]
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
func (c *Coordinator) takeNextMail(role string) (Message, bool) {
	c.mu.Lock()
	deliverable := c.undeliveredLocked(role)
	if len(deliverable) == 0 {
		c.mu.Unlock()
		return Message{}, false
	}
	msg := deliverable[0]
	// Reserve under the lock so a racing poll delivery cannot double-take.
	c.delivered[role] = append(c.delivered[role], msg.ID)
	c.mu.Unlock()

	if err := c.mail.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factMailConsumed, c.now(), mailConsumed{Role: role, MessageIDs: []string{msg.ID}})}, nil
	}); err != nil {
		return Message{}, false
	}
	c.unreserve(role, []string{msg.ID})
	return msg, true
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
// to role — the cursor-ack a SUBSEQUENT recv carries (review R6: consume
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
	p := &parkedPoll{role: role, ch: make(chan pollResult, 1)}
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
		return c.abandonPoll(role, p, ErrRecvTimeout)
	case <-ctx.Done():
		return c.abandonPoll(role, p, ctx.Err())
	}
}

// abandonPoll resolves the timeout/cancel race against a concurrent delivery:
// if the delivery already won (done), its completion is authoritative — wait
// for it; otherwise claim the poll, re-acquire the slot (unpark), and fail
// with err.
func (c *Coordinator) abandonPoll(role string, p *parkedPoll, err error) ([]Message, error) {
	c.mu.Lock()
	if p.done {
		c.mu.Unlock()
		r := <-p.ch
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
