package coord

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// THE ONE INBOX. The session owner is the one recipient with no runner: a
// child's in/ spool is read by a Home on the far side of a run channel, the
// owner's by agent_recv, here, in the coordinator's own process. spoolInbox
// is that reader and the parking in front of it, as one type:
//
//   - PARK. One held long-poll per role; a newer receive preempts the parked
//     one (ErrRecvPreempted); a delivery WAKES the poll without handing it
//     anything (the payload is on disk); revocation severs it (ErrRevoked).
//   - CLAIM. A receive that runs, or is woken, reads in/ and reserves what it
//     returns — the ONE place a hand-off to a live caller becomes real. It
//     remembers which FILE each id came from.
//   - ACK. The next receive (or a clean close) consume-renames every file
//     behind the reserved ids into in/consumed/ and releases the reservation.
//
// The rename is the ack, and it happens one receive LATE on purpose: renaming
// at claim time would turn at-least-once into at-most-once for a harness that
// stopped listening between the wake and the return. A crash between the two
// leaves the file in in/, where the next coordinator's first receive finds it
// again under the same id — the durable half of at-least-once, and why the id
// is the file's origin id rather than anything minted here.
//
// There is no arrival-burst settling: a claim reads the whole directory, and
// whatever lands after it is deliverable to the NEXT receive without parking
// (a receive begins with a read). For a terminal-driven owner the
// mail-pending reminder is the wake for what landed with no receive parked;
// while one IS parked no reminder is injected (TerminalInjector).
//
// There is no reactor for the owner either. A doorbell for the owner is
// delivered in-process (ringSpool) as a bare wake to the parked poll, and the
// receive that wakes reads the directory itself; a wake with no poll parked
// changes nothing.
type spoolInbox struct {
	rep      report.Reporter
	mapper   spool.PathMapper
	counters *spoolDeliveryCounters
	// sweep reads a role's spool directory the coordinator's way
	// (sweepSpoolDir: "not there" is empty, a real failure is loud).
	sweep func(role string, dir spool.Dir, why string) (spool.SweepResult, bool)
	// onPark / onUnpark tie a parked receive to the coordinator's slot
	// accounting (onRolePark / onRoleUnpark).
	onPark, onUnpark func(role string)

	mu sync.Mutex
	// polls holds the one parked receive per role.
	polls map[string]*parkedPoll
	// delivered is the runtime ledger: per role, the ids a receive has handed
	// to a live caller and the next receive has not yet acked.
	delivered map[string][]string
	// refs maps a delivered id to the file it came from, for the ack's
	// consume-rename.
	refs map[string]spool.Ref
}

func newSpoolInbox(rep report.Reporter, mapper spool.PathMapper, counters *spoolDeliveryCounters,
	sweep func(string, spool.Dir, string) (spool.SweepResult, bool), onPark, onUnpark func(string)) *spoolInbox {
	return &spoolInbox{
		rep: rep, mapper: mapper, counters: counters, sweep: sweep, onPark: onPark, onUnpark: onUnpark,
		polls:     make(map[string]*parkedPoll),
		delivered: make(map[string][]string),
		refs:      make(map[string]spool.Ref),
	}
}

// parkedPoll is one held agent_recv long-poll. done flips under the inbox's
// mu so the deliver/timeout/preempt/revoke races resolve to exactly one
// completion.
type parkedPoll struct {
	done bool
	ch   chan pollResult
}

// pollResult is what completes a parked poll's channel. A delivery sends a
// bare wake (err==nil): the payload is on disk, and claim — called by
// whichever goroutine is actually about to hand messages back to a still-live
// caller — is the one place a reservation is ever made. That is what keeps
// "woken" and "received" from being conflated: a wake sent to a poll nobody
// drains reserves nothing, and the next receive finds the mail where it was.
type pollResult struct {
	err error
}

// parked reports whether role has a live parked receive.
func (in *spoolInbox) parked(role string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	p := in.polls[role]
	return p != nil && !p.done
}

// wake completes role's parked poll if one is waiting — a bare wake, nothing
// reserved. Completion (including the unpark slot re-acquisition) runs
// asynchronously so the sender never blocks on the recipient's slot.
func (in *spoolInbox) wake(role string) bool {
	in.mu.Lock()
	p := in.polls[role]
	if p == nil || p.done {
		in.mu.Unlock()
		return false
	}
	p.done = true
	delete(in.polls, role)
	in.mu.Unlock()
	go func() {
		in.onUnpark(role)
		p.ch <- pollResult{}
	}()
	return true
}

// sever completes role's parked poll with err WITHOUT the unpark slot
// re-acquisition — credential revocation: the session is gone, and its slot
// accounting is settled by the terminal path.
func (in *spoolInbox) sever(role string, err error) {
	in.mu.Lock()
	p := in.polls[role]
	if p == nil || p.done {
		in.mu.Unlock()
		return
	}
	p.done = true
	delete(in.polls, role)
	in.mu.Unlock()
	p.ch <- pollResult{err: err}
}

// recv is the long-poll behind agent_recv: ack prior deliveries, claim
// deliverable mail, or park for up to wait.
func (in *spoolInbox) recv(ctx context.Context, role string, wait time.Duration) ([]Message, error) {
	in.ack(role)
	if msgs, ok := in.claim(role); ok {
		return msgs, nil
	}
	if wait <= 0 {
		return nil, ErrRecvTimeout
	}
	in.mu.Lock()
	prev := in.polls[role]
	fresh := prev == nil || prev.done
	if !fresh {
		// Newest preempts: the older poll completes with a typed error. No
		// park-hook churn — the role stays parked, only the waiter swaps.
		prev.done = true
		go func() { prev.ch <- pollResult{err: ErrRecvPreempted} }()
	}
	p := &parkedPoll{ch: make(chan pollResult, 1)}
	in.polls[role] = p
	in.mu.Unlock()

	if fresh {
		in.onPark(role)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		if r.err != nil {
			return nil, r.err
		}
		// The wake is redeemed here: the call receiving it is, by
		// construction, still live (mid-select on this very channel), so a
		// claim made here is a genuine hand-off the next receive's ack can
		// trust. Woken but beaten to the mail by another claim is a timeout.
		if msgs, ok := in.claim(role); ok {
			return msgs, nil
		}
		return nil, ErrRecvTimeout
	case <-timer.C:
		// The timer expiring does not end the CALLER: it is still waiting for
		// this call's return value, so a delivery that won the race is handed
		// to it.
		return in.abandon(role, p, ErrRecvTimeout, false)
	case <-ctx.Done():
		// A cancelled context DOES end the caller — nothing it returns can be
		// received — so a delivery that won the race has to be released.
		return in.abandon(role, p, ctx.Err(), true)
	}
}

// abandon resolves the timeout/cancel race against a concurrent delivery: if
// the delivery already won (done), its completion is authoritative — wait for
// it; otherwise take the poll down, re-acquire the slot (unpark), and fail
// with err.
//
// callerGone says whether the receive's caller can still take what this
// returns. It cannot when its own context was cancelled, and a delivery that
// won the race is then a delivery to NOBODY: claiming would reserve — and let
// the next receive's ack consume — a message no agent ever saw. Not claiming
// is what keeps at-least-once true there; the timeout path claims, because
// there the caller is still present to be given it.
func (in *spoolInbox) abandon(role string, p *parkedPoll, err error, callerGone bool) ([]Message, error) {
	in.mu.Lock()
	if p.done {
		in.mu.Unlock()
		r := <-p.ch
		if r.err != nil {
			return nil, r.err
		}
		if callerGone {
			return nil, err
		}
		if msgs, ok := in.claim(role); ok {
			return msgs, nil
		}
		return nil, err
	}
	p.done = true
	if in.polls[role] == p {
		delete(in.polls, role)
	}
	in.mu.Unlock()
	in.onUnpark(role)
	return nil, err
}

// claim reads role's in/ and reserves and returns every message not already
// reserved. ok=false means nothing new is deliverable right now.
//
// A file that parses as a spool entry but not as a mailbox message (an
// unknown kind, a structured payload that will not decode) is moved to
// in/failed/ rather than left to be re-read and re-warned about on every
// receive for the life of the process — the terminal state the runner's
// reader gives such a file, for the same reason.
func (in *spoolInbox) claim(role string) ([]Message, bool) {
	res, ok := in.sweep(role, spool.DirIn, "draining the session owner's inbox")
	if !ok {
		return nil, false
	}
	for _, p := range res.Problems {
		in.rep.Warnf("coordinator: a file in the owner's in/ spool is not a message and will not be delivered: %v", p.Error())
		in.counters.failed.Add(1)
	}
	type unreadable struct {
		entry spool.Entry
		err   error
	}
	var (
		out    []Message
		failed []unreadable
	)
	in.mu.Lock()
	reserved := make(map[string]bool, len(in.delivered[role]))
	for _, id := range in.delivered[role] {
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
		in.delivered[role] = append(in.delivered[role], msg.ID)
		in.refs[msg.ID] = e.Ref
		out = append(out, msg)
	}
	in.mu.Unlock()
	for _, f := range failed {
		in.counters.failed.Add(1)
		failSpool(in.rep, in.mapper, "coordinator", f.entry.Ref, "refusing an undeliverable message in the owner's in/ spool", f.err)
	}
	if len(out) == 0 {
		return nil, false
	}
	in.counters.delivered.Add(uint64(len(out)))
	return out, true
}

// pending counts role's deliverable mail: what is in in/ minus what a
// receive has already handed to a live caller and not yet acked — those
// files are still there because the ack is one receive late, but they are
// spoken for, not waiting.
func (in *spoolInbox) pending(role string) int {
	res, ok := in.sweep(role, spool.DirIn, "counting the owner's pending mail")
	if !ok {
		return 0
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	reserved := make(map[string]bool, len(in.delivered[role]))
	for _, id := range in.delivered[role] {
		reserved[id] = true
	}
	n := 0
	for _, e := range res.Entries {
		if !reserved[spoolMessageID(e)] {
			n++
		}
	}
	return n
}

// ack consumes every id a prior receive reserved for role by renaming its
// file into in/consumed/, and releases the reservation.
//
// A rename that fails for any reason other than the file already being gone
// leaves the file in in/ and releases the reservation anyway: the next claim
// delivers it again — a duplicate the reader dedupes on id — where a
// reservation nothing ever clears would be a message permanently invisible.
func (in *spoolInbox) ack(role string) {
	in.mu.Lock()
	ids := append([]string(nil), in.delivered[role]...)
	refs := make(map[string]spool.Ref, len(ids))
	for _, id := range ids {
		if ref, ok := in.refs[id]; ok {
			refs[id] = ref
			delete(in.refs, id)
		}
	}
	in.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	consumed := uint64(0)
	for id, ref := range refs {
		if _, err := spool.Consume(in.mapper, ref); err != nil {
			if errors.Is(err, spool.ErrAlreadyGone) {
				continue
			}
			in.rep.Warnf("coordinator: the owner received %s but it could not be marked consumed: %v (it will be delivered again on the next receive)", id, err)
			in.counters.failed.Add(1)
			continue
		}
		consumed++
	}
	in.unreserve(role, ids)
	in.counters.consumed.Add(consumed)
}

// unreserve drops ids from the runtime delivery ledger (they are consumed).
func (in *spoolInbox) unreserve(role string, ids []string) {
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	in.mu.Lock()
	kept := in.delivered[role][:0]
	for _, id := range in.delivered[role] {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		delete(in.delivered, role)
	} else {
		in.delivered[role] = kept
	}
	in.mu.Unlock()
}
