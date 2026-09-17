package coord

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// THE OWNER'S RECEIVE: the long-poll behind agent_recv for the one recipient
// whose spool THIS PROCESS reads (spoolowner.go). Every other recipient is a
// run whose own runner drains its spool (Home.Recv), so a receive here for
// any role but the declared owner has nothing to read and is refused.
//
// What lives here is substrate-agnostic parking: one held poll per role,
// woken on arrival, preempted by a newer receive, settled over an arrival
// burst, and acknowledged one receive LATE (the next receive, or a clean
// close, proves the harness took the last batch — at-least-once). The store
// operations — claim and ack — are the owner spool's.

// Typed completions. Neither the timeout nor the preemption is a fault, and
// what the caller should do next depends on WHO is parked — a child that
// times out finishes, a coordinator re-arms — which this package cannot
// know: recvMail sees a role, not an audience. So the sentinels name the
// event and nothing more; the MCP handlers, which do know their audience,
// attach the guidance.
var (
	// ErrRecvTimeout completes a parked agent_recv whose bounded wait
	// elapsed with no message.
	ErrRecvTimeout = errors.New("agent_recv: timed out with no message")
	// ErrPeerRouting rejects executor→executor addressing (hub-and-spoke).
	ErrPeerRouting = errors.New(`agent_send: executors may only address "parent"; route via coordinator`)
	// ErrRecvPreempted completes the OLDER of two long-polls for one role:
	// one active long-poll per role, newest preempts. It is a YIELD, not a
	// failure — no mail is lost, the newer poll holds the park — and the
	// tool surfaces render it as a successful empty receive; it rides the
	// error channel only because that is the one completion path a poll has.
	ErrRecvPreempted = errors.New("agent_recv: yielded to a newer receive for this session")
	// ErrRevoked completes a parked long-poll whose credential was revoked
	// (run ended / agent_stop): revocation severs parked polls.
	ErrRevoked = errors.New("agent_recv: this session's credential was revoked")
	// ErrRecvNotOwner refuses a receive here for a role that is not the
	// declared session owner: that role's inbox is a spool its own runner
	// drains, and reading it from this process would either find nothing
	// or consume mail another process is about to deliver.
	ErrRecvNotOwner = errors.New("agent_recv: only the session owner receives at the coordinator; a run's runner drains its own spool")
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

// pollResult is what completes a parked poll's channel. A delivery sends a
// bare wake (err==nil): the payload is on disk, and tryClaimDeliverable —
// called by whichever goroutine is actually about to hand messages back to
// a still-live caller — is the one place a claim on it is ever made. That is
// what keeps "woken" and "received" from being conflated: a wake sent to a
// poll nobody drains reserves nothing, so it costs nothing and the next recv
// finds the mail exactly where it left it.
type pollResult struct {
	err error
}

// newMessageID mints a message id (the dedupe key).
func newMessageID() string { return randID("m-", 12) }

// deliverToPoll WAKES role's parked poll if one is waiting — it does not
// reserve anything and does not hand the payload through the channel. The
// wake means "your mail is on disk, go claim it"; tryClaimDeliverable is the
// one place a claim (and so a reservation) is ever made, by whichever
// goroutine is actually about to return it to a still-live caller.
//
// This is deliberate: reserving HERE, at hand-off, would mark the message
// ack-eligible for a channel that a preempting recv, or an MCP client that
// simply stopped listening without cancelling anything, may never drain —
// exactly the shape that lets an ack consume a message before anyone had
// received it. Completion (including the unpark slot re-acquisition) runs
// asynchronously so the sender never blocks on the recipient's slot.
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
	if !c.ownerSpool(role) {
		return nil, false
	}
	return c.claimSpoolInbox(role)
}

// pendingCount reports how many messages could still be delivered to role —
// the ended-child check: leftover mail triggers a resume, never strands.
func (c *Coordinator) pendingCount(role string) int {
	return c.spoolPendingCount(role)
}

// unreserve drops ids from the runtime delivery ledger (they are consumed
// now).
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

// ackDelivered acknowledges everything previously delivered to role — the
// cursor-ack a SUBSEQUENT recv carries (a crash before the ack re-delivers,
// at-least-once). The ack is the consume-rename (spoolowner.go).
func (c *Coordinator) ackDelivered(role string) {
	c.ackSpoolInbox(role)
}

// recvMail is the long-poll behind agent_recv for the owner: ack prior
// deliveries, drain deliverable mail, or park for up to wait. One active
// long-poll per role; a newer receive preempts the parked one
// (ErrRecvPreempted).
func (c *Coordinator) recvMail(ctx context.Context, role string, wait time.Duration) ([]Message, error) {
	if !c.ownerSpool(role) {
		return nil, fmt.Errorf("%w (asked for %q; the owner is %q)", ErrRecvNotOwner, role, c.ownerHarp)
	}
	c.ackDelivered(role)

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
	msgs, ok := c.tryClaimDeliverable(role)
	if !ok {
		// Woken, but something else (another claim) already took the mail.
		return nil, ErrRecvTimeout
	}
	return append(msgs, c.settleBurst(role)...), nil
}

// A spoolReactor pass routes EVERY swept child's report serially, so N children
// finishing inside one sweep window arrive as N deliveries microseconds
// apart. A wake redeemed the instant the first lands claims exactly that one,
// and entries 2..N then land with no poll parked — deliverToPoll returns
// false. They wait for the caller's NEXT receive, with nothing in the
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
// delivery that won the race is then a delivery to NOBODY: the id would be
// reserved in the runtime ledger, so the next recv's cursor-ack would
// consume a message no agent ever saw. Not claiming is what keeps
// at-least-once true for that case; the timeout path claims, because there
// the caller is still there to be given it.
func (c *Coordinator) abandonPoll(role string, p *parkedPoll, err error, callerGone bool) ([]Message, error) {
	c.mu.Lock()
	if p.done {
		c.mu.Unlock()
		r := <-p.ch
		if r.err != nil {
			return nil, r.err
		}
		// A bare wake (deliverToPoll): claiming is the ONLY thing that
		// reserves anything, so a caller that is gone must not claim on its
		// behalf — that would reserve (and let the next recv's ack consume)
		// a message nobody was left to receive. Leaving it unclaimed is what
		// keeps it deliverable.
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
