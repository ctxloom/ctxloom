package coord

import (
	"context"
	"errors"
	"time"
)

// Runner liveness parameters. The runner heartbeats every HeartbeatInterval;
// the coordinator synthesizes RunExited for a runner's active runs after
// RunnerLossHeartbeats MISSED heartbeats (docker-stop kills runner
// and harness together, so RunExited is never sent in the motivating
// scenario — synthesis on disconnect or heartbeat silence is the load-bearing
// path). With N=3 at 5s the silent-loss detection bound is 20s
// (interval×(N+1)), checked by a watchdog ticking every interval — so queue
// drain after a silent runner death starts within 25s; a DISCONNECT (the
// docker-stop case) synthesizes immediately.
const (
	HeartbeatInterval    = 5 * time.Second
	RunnerLossHeartbeats = 3
	runnerLossTimeout    = HeartbeatInterval * (RunnerLossHeartbeats + 1)
)

// RunnerSession is the coordinator's side of one connected runner: the
// runner identified by its credential (the hash), heartbeat-tracked, with
// the coordinator-initiated requests (StartRun foremost) it issues over
// the one BidiSession scaffold — the mirror of a run channel's requests,
// direction reversed. The wire adapter registers it after the handshake
// (AttachRunner), pumps its requests onto the stream (Pump) and resolves the
// runner's answers (Resolve). lastBeat is guarded by Coordinator.mu.
type RunnerSession struct {
	BidiSession[RunnerRequest, RunnerRequest, RunnerResponse]
	credHash string
	runID    string
	lastBeat time.Time
}

func newRunnerSession(credHash, runID string, lastBeat time.Time, cancel context.CancelFunc) *RunnerSession {
	return &RunnerSession{
		BidiSession: NewBidiSession[RunnerRequest, RunnerRequest, RunnerResponse](cancel, 8),
		credHash:    credHash,
		runID:       runID,
		lastBeat:    lastBeat,
	}
}

// Pump drains the session's outbound requests onto write until ctx ends or
// a write fails — the single writer for this session.
func (rs *RunnerSession) Pump(ctx context.Context, write func(RunnerRequest) error) {
	rs.BidiSession.Pump(ctx, write)
}

// Resolve hands the runner's answer to the request that awaits it.
func (rs *RunnerSession) Resolve(resp RunnerResponse) { rs.BidiSession.Resolve(resp.RequestID, resp) }

// end fails every in-flight request (the session died before an answer
// arrived) and marks the session ended, so no requestRunner caller hangs
// past it and none registers a waiter nobody is left to resolve.
func (rs *RunnerSession) end() {
	rs.FailPending(func(id string) RunnerResponse {
		return RunnerResponse{RequestID: id, Err: ErrRunnerSessionEnded}
	})
}

// RunnerHello is the handshake's verdict for a runner presenting credHash:
// a runner dialing in with NO active runs while the coordinator drains is
// refused (ErrDraining — AgentRun/StartOwnedRun already refuse every new run
// once draining, so no StartRun would ever reach it; a runner reconnecting
// to finish runs it already holds is exempt, so in-flight turns are not
// orphaned by a network blip during drain), and every claimed active run
// must have been issued to THIS credential (ErrRunNotIssued). An accepted
// runner that names a run this process did not start is one that outlived
// the previous coordinator: it is re-adopted here, BEFORE it registers as
// connected, so a caller that sees the runner connected sees the run owned.
func (c *Coordinator) RunnerHello(credHash string, hello RunnerHello) error {
	if c.Draining() && len(hello.ActiveRunIDs) == 0 {
		return ErrDraining
	}
	for _, runID := range hello.ActiveRunIDs {
		owned := false
		c.runs.View(func() {
			if r := c.runsF.run(runID); r != nil && r.CredHash == credHash {
				owned = true
			}
		})
		if !owned {
			return refusal(ErrRunNotIssued, "run %s was not issued to this credential", runID)
		}
	}
	for _, runID := range hello.ActiveRunIDs {
		c.readopt(runID)
	}
	return nil
}

// AttachRunner registers the runner connected under credHash: one session
// per credential, newest wins (reconnect); a spawn waiting in awaitRunner is
// released. cancel is the stream context's.
func (c *Coordinator) AttachRunner(id Identity, credHash string, cancel context.CancelFunc) *RunnerSession {
	rs := newRunnerSession(credHash, id.RunID, c.now(), cancel)
	c.mu.Lock()
	if prev := c.runners[credHash]; prev != nil {
		prev.cancel()
	}
	c.runners[credHash] = rs
	if ready, ok := c.runnerReady[credHash]; ok {
		close(ready)
		delete(c.runnerReady, credHash)
	}
	c.mu.Unlock()
	c.audit("runner_hello", id.Harp, map[string]string{"run_id": id.RunID})
	return rs
}

// DetachRunner is the runner stream's teardown. Disconnect IS runner loss
// (∪ RunExited): the credential's remaining active runs are terminated as
// lost, deduped against the chat-close path exactly-once inside
// terminateRun. Only this session's own registration is removed, never a
// successor's.
func (c *Coordinator) DetachRunner(rs *RunnerSession) {
	c.mu.Lock()
	registered := c.runners[rs.credHash] == rs
	if registered {
		delete(c.runners, rs.credHash)
	}
	c.mu.Unlock()
	rs.cancel()
	rs.end()
	if registered {
		c.runnerLost(rs.credHash, "RunnerChannel disconnected")
	}
}

// RunnerHeartbeat records a live runner's heartbeat (a duplicate hello on a
// live stream counts as one).
func (c *Coordinator) RunnerHeartbeat(rs *RunnerSession) {
	c.mu.Lock()
	rs.lastBeat = c.now()
	c.mu.Unlock()
}

// CredHash is the hash of the credential the runner connected under.
func (rs *RunnerSession) CredHash() string { return rs.credHash }

// RunnerExited processes an explicit process-level exit fact from the
// runner connected under credHash: validate ownership, record the harness
// resume handle, terminate.
func (c *Coordinator) RunnerExited(credHash string, exited RunExited) {
	runID := exited.RunID
	owned := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && r.CredHash == credHash {
			owned = true
		}
	})
	if !owned {
		c.rep.Warnf("coordinator: RunExited for %s from a credential that does not own it; ignored", runID)
		return
	}
	c.recordHarnessSession(runID, exited.HarnessSessionID)
	detail := ""
	if exited.Signal != "" {
		detail = "signal " + exited.Signal
	}
	c.terminateRun(runID, CauseRunnerExit, detail)
}

// runnerLost synthesizes RunExited for every active run issued to the lost
// credential — queue drain and terminal-record synthesis key on RUNNER LOSS
// (disconnect ∪ heartbeat silence ∪ explicit RunExited), reconciled
// exactly-once with the chat-close path inside terminateRun.
func (c *Coordinator) runnerLost(credHash, why string) {
	var active []string
	c.runs.View(func() { active = c.runsF.activeRunsForCred(credHash) })
	for _, runID := range active {
		c.terminateRun(runID, CauseRunnerLoss, why)
	}
}

// runnerWatchdog scans connected runners every HeartbeatInterval and declares
// loss after runnerLossTimeout of heartbeat silence.
func (c *Coordinator) runnerWatchdog() {
	c.every(HeartbeatInterval, func() { c.checkRunnerLiveness(c.now()) })
}

// checkRunnerLiveness is the watchdog body, callable with an explicit now for
// deterministic tests.
func (c *Coordinator) checkRunnerLiveness(now time.Time) {
	var lost []*RunnerSession
	c.mu.Lock()
	for hash, rs := range c.runners {
		if now.Sub(rs.lastBeat) > runnerLossTimeout {
			delete(c.runners, hash)
			lost = append(lost, rs)
		}
	}
	c.mu.Unlock()
	for _, rs := range lost {
		rs.cancel()
		rs.end()
		c.runnerLost(rs.credHash, "missed heartbeats past the loss bound")
	}
}

// awaitRunner blocks until credHash's runner registers (or ctx ends),
// returning its live session immediately if already connected. The
// StartRun-issuing spawn path uses this: the coordinator spawns the runner
// PROCESS and then must wait for that same process to dial home before it
// can StartRun on it.
func (c *Coordinator) awaitRunner(ctx context.Context, credHash string) (*RunnerSession, error) {
	c.mu.Lock()
	if rs := c.runners[credHash]; rs != nil {
		c.mu.Unlock()
		return rs, nil
	}
	ready, ok := c.runnerReady[credHash]
	if !ok {
		ready = make(chan struct{})
		c.runnerReady[credHash] = ready
	}
	c.mu.Unlock()
	select {
	case <-ready:
		c.mu.Lock()
		rs := c.runners[credHash]
		c.mu.Unlock()
		if rs == nil {
			return nil, errors.New("coord: runner registration signaled but its session already ended")
		}
		return rs, nil
	case <-ctx.Done():
		c.mu.Lock()
		// Only clean up the waiter WE created and nobody claimed yet — a
		// concurrent awaitRunner for the same credHash (shouldn't happen in
		// practice; one spawn mints one credential) must not lose its wakeup.
		select {
		case <-ready:
		default:
			delete(c.runnerReady, credHash)
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// requestRunner issues one coordinator-initiated request (StartRun, Pause,
// Resume, Turn) to the runner connected under credHash and waits for its
// answer, bounded by ctx or a default budget. The error is the transport's
// (no runner, the session ended, ctx ran out); the runner's own refusal is
// the answer's Err, which the caller reads — a refusal and a request that
// never completed are different outcomes with different remedies.
func (c *Coordinator) requestRunner(ctx context.Context, credHash string, req RunnerRequest) (RunnerResponse, error) {
	if req.RequestID == "" {
		req.RequestID = RandID("rreq-", 12)
	}
	c.mu.Lock()
	rs := c.runners[credHash]
	c.mu.Unlock()
	if rs == nil {
		return RunnerResponse{}, errors.New("coord: no connected runner for this credential")
	}
	ch, ok := rs.Register(req.RequestID, req)
	if !ok {
		// The session was torn down between the lookup above and here: its
		// send pump is gone, so a waiter registered now would buy a
		// full-budget wait for an answer that cannot come.
		return RunnerResponse{}, errors.New("coord: the runner session ended before this request could be issued")
	}

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	select {
	case rs.send <- req:
	case <-ctx.Done():
		rs.Withdraw(req.RequestID)
		return RunnerResponse{}, ctx.Err()
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		rs.Withdraw(req.RequestID)
		return RunnerResponse{}, ctx.Err()
	}
}
