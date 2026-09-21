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
// owner's by agent_recv, here, in the coordinator's own process — and by the
// owner's turn-start hook (`ctxloom hook mail-drain`), a subprocess that
// shares the on-disk states below and nothing else. spoolInbox is the
// in-process reader and the parking in front of it, as one type:
//
//   - PARK. One held long-poll per role; a newer receive preempts the parked
//     one (ErrRecvPreempted); a delivery WAKES the poll without handing it
//     anything (the payload is on disk); revocation severs it (ErrRevoked).
//   - CLAIM. A receive that runs, or is woken, takes in/ into in/claimed/
//     (spool.Claim) and hands the in-flight set to a live caller — the ONE
//     place a hand-off becomes real. It remembers only which NAMES it handed
//     out, for the ack.
//   - ACK. The next receive acknowledges every name the previous one handed
//     out: in/claimed/ → in/consumed/ (spool.Ack).
//
// THE RESERVATION IS ON DISK, not here. in/claimed/ is what keeps a message
// from being counted pending or handed out twice, and it is shared with the
// owner's OTHER reader, the turn-start hook (`ctxloom hook mail-drain`),
// which is a subprocess with no memory to keep one in. What this type keeps
// is only the ack cursor: the ack happens one receive LATE on purpose, since
// acknowledging at claim time would turn at-least-once into at-most-once for
// a harness that stopped listening between the wake and the return. A crash
// between the two leaves the file in in/claimed/, where the next reader's
// Claim — this process's or a hook's — hands it out again under the same id:
// the durable half of at-least-once, and why the id is the file's origin id
// rather than anything minted here.
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
	// onPark / onUnpark tie a parked receive to the coordinator's slot
	// accounting (onRolePark / onRoleUnpark).
	onPark, onUnpark func(role string)

	mu sync.Mutex
	// polls holds the one parked receive per role.
	polls map[string]*parkedPoll
	// handed is the ack cursor: per role, the spool file names a receive has
	// handed to a live caller and the next receive has not yet acknowledged.
	// It is not the reservation — that is in/claimed/ on disk.
	handed map[string][]string
}

func newSpoolInbox(rep report.Reporter, mapper spool.PathMapper, counters *spoolDeliveryCounters, onPark, onUnpark func(string)) *spoolInbox {
	return &spoolInbox{
		rep: rep, mapper: mapper, counters: counters, onPark: onPark, onUnpark: onUnpark,
		polls:  make(map[string]*parkedPoll),
		handed: make(map[string][]string),
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
// caller — is the one place a hand-off is ever made. That is what keeps
// "woken" and "received" from being conflated: a wake sent to a poll nobody
// drains claims nothing, and the next receive finds the mail where it was.
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

// claim takes role's in/ into in/claimed/ and hands back the whole in-flight
// set as mailbox messages. ok=false means nothing is deliverable right now.
//
// A file that parses as a spool entry but not as a mailbox message (an
// unknown kind, a structured payload that will not decode) is moved to
// in/failed/ rather than left to be re-read and re-warned about on every
// receive for the life of the process — the terminal state the runner's
// reader gives such a file, for the same reason.
func (in *spoolInbox) claim(role string) ([]Message, bool) {
	res, err := spool.Claim(in.mapper, role)
	if err != nil {
		in.rep.Warnf("coordinator: claiming the session owner's inbox: %v", err)
		in.counters.failed.Add(1)
		return nil, false
	}
	for _, p := range res.Problems {
		in.rep.Warnf("coordinator: a file in the owner's in/ spool is not a message and will not be delivered: %v", p.Error())
		in.counters.failed.Add(1)
	}
	var (
		out   []Message
		names []string
	)
	for _, e := range res.Entries {
		msg, err := mailFromSpool(e, e.Message.FromHarp)
		if err != nil {
			in.counters.failed.Add(1)
			failSpool(in.rep, in.mapper, "coordinator", e.Ref, "refusing an undeliverable message in the owner's in/ spool", err)
			continue
		}
		names = append(names, e.Ref.Name)
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, false
	}
	in.mu.Lock()
	in.handed[role] = append(in.handed[role], names...)
	in.mu.Unlock()
	in.counters.delivered.Add(uint64(len(out)))
	return out, true
}

// ack acknowledges every file a prior receive handed to role's caller by
// renaming it from in/claimed/ into in/consumed/, and drops the cursor.
//
// A rename that fails for any reason other than the file already being gone
// leaves the file in in/claimed/, where the next claim delivers it again — a
// duplicate the reader dedupes on id — and the cursor is dropped anyway,
// because a cursor nothing ever clears would ack the file on a later receive
// the caller never saw.
func (in *spoolInbox) ack(role string) {
	in.mu.Lock()
	names := in.handed[role]
	delete(in.handed, role)
	in.mu.Unlock()
	consumed := uint64(0)
	for _, name := range names {
		if err := spool.Ack(in.mapper, role, name); err != nil {
			if errors.Is(err, spool.ErrAlreadyGone) {
				continue
			}
			in.rep.Warnf("coordinator: the owner received %s but it could not be acknowledged: %v (it will be delivered again on the next receive)", name, err)
			in.counters.failed.Add(1)
			continue
		}
		consumed++
	}
	in.counters.consumed.Add(consumed)
}
