package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

const (
	// maxAgentDepth is the recursion guard: depth 0 spawns depth 1. In the
	// B window, grandchildren are REFUSED — a delegated child (depth ≥ 1)
	// may not spawn; flat-hub semantics arrive in Wave D.
	maxAgentDepth = 1
	// agentTurnCap is D4: children execute serially until the isolation
	// concurrency defects land. The cap counts EXECUTING turns — a child
	// parked in agent_recv or idle at a turn boundary yields its slot.
	agentTurnCap = 1
)

// childRt is the RUNTIME attachment of one live run: the engine channels and
// slot bookkeeping. All durable/mutable STATE (queue membership, roster
// state, lineage, credentials) lives in the folds; this struct is rebuilt
// from them and holds only what a fold cannot: live channels. Guarded by
// Coordinator.mu except where noted.
type childRt struct {
	runID      string
	harp       string
	agentName  string
	parentHarp string
	plan       *SpawnPlan

	slotHeld   bool
	oneshot    bool
	in         chan<- agent.ChatMessage
	close      func()
	wake       chan struct{}
	turnOutput []string // oneshot bridging: this turn's entries
}

// newRunID mints a run attempt id (UUID-shaped; retries get a fresh one).
func newRunID() string { return randID("run-", 16) }

// RunOutcome is agent_run's return payload, fixed at enqueue.
type RunOutcome struct {
	Harp     string
	RunID    string
	Engine   string
	Profiles []string
	Runtime  string
	Queued   bool
	Degraded []string
}

// AgentRun launches a configured agent as a delegated child of caller:
// resolve exactly as `run --agent` does, gate the permission enum (D3), mint
// the harp + run id + credential, journal the enqueue under the D4 cap, and
// return immediately — everything after spawn rides the mailboxes.
func (c *Coordinator) AgentRun(ctx context.Context, caller Identity, agentName, prompt string) (*RunOutcome, error) {
	if agentName == "" {
		return nil, errors.New("agent_run: agent is required (a configured agent name; see `ctxloom agent list`)")
	}
	if prompt == "" {
		return nil, errors.New("agent_run: prompt is required (the child's briefing/first turn)")
	}
	// Depth derives from the CREDENTIAL, never from env (review R11). The
	// B window refuses grandchildren outright: flat-hub semantics arrive
	// in Wave D.
	if caller.Depth >= maxAgentDepth {
		return nil, fmt.Errorf("agent_run: refused: this session is itself a delegated child (depth %d) — delegated children cannot spawn grandchildren yet; report the work back to your coordinator (agent_send to \"parent\") and let it fan out", caller.Depth)
	}

	plan, err := c.spawner.Resolve(ctx, agentName)
	if err != nil {
		return nil, err
	}

	harp, err := c.spawner.AssignSession(c.projectDir, plan.Backend)
	if err != nil {
		return nil, fmt.Errorf("agent_run: session accounting unavailable (the harp is the child's address): %w", err)
	}

	// Resolve the reach-back endpoint BEFORE enqueue so a container child
	// that could never message its parent is refused loudly at the verb.
	url, err := c.spawnReachURL(harp, plan.Runtime)
	if err != nil {
		return nil, err
	}

	rt, token, err := c.enqueueRun(caller, plan, harp, prompt, false)
	if err != nil {
		return nil, err
	}
	c.audit("agent_run", caller.Harp, map[string]string{"agent": agentName, "harp": harp, "run_id": rt.runID})

	go c.runChild(rt, prompt, token, url)

	runtime := plan.Runtime
	if runtime == "" {
		runtime = "host"
	}
	c.mu.Lock()
	queued := !rt.slotHeld
	c.mu.Unlock()
	return &RunOutcome{
		Harp:     harp,
		RunID:    rt.runID,
		Engine:   plan.Label,
		Profiles: plan.Profiles,
		Runtime:  runtime,
		Queued:   queued,
		Degraded: plan.Degraded,
	}, nil
}

// enqueueRun mints the run id + credential, journals the enqueue (fsynced
// before return — the durability-asserting response is agent_run's), and
// publishes the runtime attachment. resume marks a re-attempt for an ended
// harp; the claim (current run still ended) is checked inside the journal's
// serialized window so concurrent resumes cannot double-launch.
func (c *Coordinator) enqueueRun(caller Identity, plan *SpawnPlan, harp, prompt string, resume bool) (*childRt, string, error) {
	runID := newRunID()
	token, credHash, err := mintToken()
	if err != nil {
		return nil, "", err
	}
	won := true
	if err := c.runs.Exec(func() ([]Fact, error) {
		if resume {
			cur := c.runsF.currentRun(harp)
			if cur == nil || !cur.Ended {
				won = false
				return nil, nil
			}
		}
		return []Fact{factAt(factRunEnqueued, c.now(), runEnqueued{
			RunID:      runID,
			Harp:       harp,
			Agent:      plan.AgentName,
			ParentHarp: caller.Harp,
			Runtime:    plan.Runtime,
			CredHash:   credHash,
			Depth:      caller.Depth + 1,
			Prompt:     prompt,
			Resume:     resume,
		})}, nil
	}); err != nil {
		return nil, "", err
	}
	if !won {
		return nil, "", errResumeLost
	}

	rt := &childRt{
		runID:      runID,
		harp:       harp,
		agentName:  plan.AgentName,
		parentHarp: caller.Harp,
		plan:       plan,
		wake:       make(chan struct{}, 1),
	}
	// Claim a free slot now when one exists so `queued` is truthful at
	// return. Claimed BEFORE publication — after it, slotHeld belongs to
	// the mutex (the park hooks may touch it).
	rt.slotHeld = c.slots.tryAcquire()
	c.mu.Lock()
	c.attach[runID] = rt
	c.byHarp[harp] = rt
	c.mu.Unlock()
	return rt, token, nil
}

// errResumeLost reports a resume claim lost to a concurrent resume — benign,
// the winner delivers.
var errResumeLost = errors.New("coord: resume already claimed")

// childEnv builds the child ENGINE's extra environment: ambient identity
// only. The coordinator reach-back trio no longer rides here — the RUNNER
// terminates MCP now, so the credential goes to the runner PROCESS via the
// per-spawn seam (runnerEnv) and the harness never sees it (one credential
// holder, one egress).
func (c *Coordinator) childEnv(harp string) map[string]string {
	env := map[string]string{
		"CTXLOOM_SESSION_HARP": harp,
	}
	// Ambient project identity, inherited from this process's env (the
	// parent run exported it): a containerized child's taskloom must key
	// the SAME shared host log.
	if pid := os.Getenv("CTXLOOM_PROJECT_ID"); pid != "" {
		env["CTXLOOM_PROJECT_ID"] = pid
	}
	return env
}

// runnerEnv builds the per-spawn env stamped onto the RUNNER process (host:
// cmd.Env on the `llm serve` subprocess; container: bare-name `-e` forms
// with the values on the run-process env — never `-e KEY=VAL` argv, never
// the process-global launcher env, which is racy across concurrent spawns).
// The runner consumes the trio, unsets it, and exports only the MCP socket
// path to the harness. url may be empty (degraded launch without
// reach-back); the trio is then omitted whole and the harness's shim falls
// back to its local mode.
func runnerEnv(harp, runID, token, url string) map[string]string {
	env := map[string]string{
		"CTXLOOM_SESSION_HARP": harp,
	}
	if url != "" {
		env[EnvCoordURL] = url
		env[EnvCoordCred] = token
		env[EnvRunID] = runID
	}
	return env
}

// spawnReachURL resolves the coordinator URL a child on runtimeAxis can dial,
// widening the listeners for a container child. A container child without
// reach-back is exactly the blue-paper stranding bug, so an unresolvable
// endpoint is a fatal finding (fail-loud) — degraded mode downgrades it and
// the child launches with no coordinator env (a local, message-less
// orchestrator, today's broken-but-running posture).
func (c *Coordinator) spawnReachURL(harp, runtimeAxis string) (string, error) {
	url, err := c.reachURL(runtimeAxis)
	if err == nil {
		return url, nil
	}
	if strictness.Degraded() {
		clidiag.Warn("ctxloom", "agent child %s: no coordinator endpoint reachable from runtime %q (%v); launching WITHOUT coordinator reach-back — its agent_send cannot reach you", harp, runtimeAxis, err)
		return "", nil
	}
	return "", fmt.Errorf("agent_run: no coordinator endpoint reachable from runtime %q: %v — check the container runtime's bridge network, or pass --degraded (env CTXLOOM_DEGRADED=1) to launch the child without coordinator reach-back", runtimeAxis, err)
}

// runChild is a spawned child's driver goroutine: wait for an execution slot
// (D4), launch the engine with the agent's intents honored, deliver the
// briefing as the first turn, then drive turns from mailbox deliveries.
func (c *Coordinator) runChild(rt *childRt, prompt, token, url string) {
	c.mu.Lock()
	held := rt.slotHeld
	c.mu.Unlock()
	if !held {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			c.failChild(rt, err)
			return
		}
		c.mu.Lock()
		rt.slotHeld = true
		c.mu.Unlock()
	}
	c.setState(rt, StateExecuting)

	launch, err := c.spawner.Launch(c.baseCtx, rt.plan, rt.plan.Context,
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.attachLaunch(rt, launch)
	c.sendTurn(rt, prompt)
	c.driveChild(rt, launch)
}

// driveChild consumes the child's event stream, handling turn boundaries and
// idle wakes, until the stream closes. The stream is teed through the
// observation hub so observers can subscribe by harp; the tee never adds
// backpressure here (TapHub's invariant).
func (c *Coordinator) driveChild(rt *childRt, launch *operations.AgentChatLaunch) {
	events := c.hub.Tee(rt.harp, launch.Events)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				c.endChild(rt, launch)
				return
			}
			c.handleChildEvent(rt, ev)
		case <-rt.wake:
			c.wakeChild(rt)
		}
	}
}

func (c *Coordinator) handleChildEvent(rt *childRt, ev agent.ChatEvent) {
	switch {
	case ev.Entry != nil:
		// Chat children report over the coordinator themselves; their
		// entries are the engine transcript's job (§6b). Oneshot children
		// have no reach-back, so their output is bridged to the parent at
		// the boundary.
		if rt.oneshot && ev.Entry.Content != "" {
			c.mu.Lock()
			rt.turnOutput = append(rt.turnOutput, ev.Entry.Content)
			c.mu.Unlock()
		}
	case ev.Complete != nil:
		c.onTurnBoundary(rt)
	}
}

// onTurnBoundary applies the §6a delivery rule "queued mid-turn → deliver at
// the next boundary": pending mail starts the next turn (the slot is kept);
// an empty mailbox parks the child idle and yields the slot.
func (c *Coordinator) onTurnBoundary(rt *childRt) {
	c.mu.Lock()
	out := rt.turnOutput
	rt.turnOutput = nil
	oneshot := rt.oneshot
	c.mu.Unlock()
	if oneshot && len(out) > 0 {
		if _, _, err := c.queueMail(rt.harp, rt.parentHarp, "result", strings.Join(out, "\n")); err != nil {
			clidiag.Warn("ctxloom", "agent %s: bridge oneshot result: %v", rt.harp, err)
		}
	}
	if msg, ok := c.takeNextMail(rt.harp); ok {
		c.sendTurn(rt, msg.Body)
		return
	}
	c.setState(rt, StateIdle)
	c.releaseSlot(rt)
}

// wakeChild starts a new turn on an idle child after a mailbox delivery (§6a
// "idle at a turn boundary: start a new turn, message as the prompt").
func (c *Coordinator) wakeChild(rt *childRt) {
	if c.runState(rt.runID) != StateIdle {
		return
	}
	msg, ok := c.takeNextMail(rt.harp)
	if !ok {
		return // spurious wake (a recv or boundary drain consumed it)
	}
	if err := c.slots.acquire(c.baseCtx); err != nil {
		return
	}
	c.mu.Lock()
	rt.slotHeld = true
	c.mu.Unlock()
	c.setState(rt, StateExecuting)
	c.sendTurn(rt, msg.Body)
}

// sendTurn writes one turn to the child's input channel. The driver goroutine
// is the channel's only writer, so this never races the close in endChild.
func (c *Coordinator) sendTurn(rt *childRt, text string) {
	c.mu.Lock()
	in := rt.in
	c.mu.Unlock()
	if in == nil {
		return
	}
	select {
	case in <- agent.ChatMessage{Text: text}:
	case <-c.baseCtx.Done():
	}
}

// endChild finalizes a child whose event stream closed: surface the terminal
// stream error and route the terminal through terminateRun — reconciled
// EXACTLY-ONCE with the runner-loss synthesis path (whichever claims the
// terminal fact first wins; the loser is a no-op).
func (c *Coordinator) endChild(rt *childRt, launch *operations.AgentChatLaunch) {
	var streamErr string
	for err := range launch.Errs {
		if err != nil {
			streamErr = err.Error()
			clidiag.Warn("ctxloom", "agent %s (%s): chat stream ended: %v", rt.harp, rt.agentName, err)
		}
	}
	c.mu.Lock()
	in := rt.in
	rt.in = nil
	c.mu.Unlock()
	if in != nil {
		close(in)
	}
	launch.Close()
	c.terminateRun(rt.runID, CauseChatClose, streamErr)
}

// failChild reports a launch failure to the parent's mailbox — the spawn verb
// already returned (async), so the mailbox is where the coordinator learns.
func (c *Coordinator) failChild(rt *childRt, err error) {
	clidiag.Warn("ctxloom", "agent_run: child %s (%s) failed to launch: %v", rt.harp, rt.agentName, err)
	c.terminateRun(rt.runID, CauseLaunchFailed, err.Error())
}

// setState journals a §6a state transition (the folds are the single owner
// of roster/queue state; the runtime only mirrors slot bookkeeping).
func (c *Coordinator) setState(rt *childRt, state string) {
	if err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(rt.runID)
		if r == nil || r.Ended {
			return nil, nil
		}
		return []Fact{factAt(factRunState, c.now(), runState{RunID: rt.runID, State: state})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "agent %s: journal state %s: %v", rt.harp, state, err)
	}
}

// runState reads the run's fold state ("" when unknown).
func (c *Coordinator) runState(runID string) string {
	state := ""
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			state = r.State
		}
	})
	return state
}

func (c *Coordinator) releaseSlot(rt *childRt) {
	c.mu.Lock()
	held := rt.slotHeld
	rt.slotHeld = false
	c.mu.Unlock()
	if held {
		c.slots.release()
	}
}

func (c *Coordinator) attachLaunch(rt *childRt, launch *operations.AgentChatLaunch) {
	c.mu.Lock()
	rt.in = launch.In
	rt.close = launch.Close
	rt.oneshot = launch.Oneshot
	c.mu.Unlock()
}

// terminateRun is the EXACTLY-ONCE terminal seam every death path funnels
// through: the legacy chat-stream-close (endChild), the runner-loss
// synthesis (review R3), an explicit RunExited, agent_stop, launch failure,
// and restart adoption. The terminal fact is claimed inside the journal's
// single-writer window; only the claimant runs the runtime consequences —
// slot release (queue advances), credential revocation + severing, the
// synthesized terminal notice into the parent's mailbox, and session-end
// accounting. The record stays: a later send/inject resumes the harp as a
// fresh run.
func (c *Coordinator) terminateRun(runID, cause, detail string) {
	var (
		won bool
		rec RunRecord
	)
	if err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(runID)
		if r == nil || r.Ended {
			return nil, nil
		}
		won = true
		rec = *r
		return []Fact{factAt(factRunEnded, c.now(), runEnded{RunID: runID, Cause: cause, Detail: detail})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "agent run %s: journal terminal: %v", runID, err)
		return
	}
	if !won {
		return
	}
	c.audit("run_terminal", rec.Harp, map[string]string{"run_id": runID, "cause": cause})

	c.mu.Lock()
	rt := c.attach[runID]
	delete(c.attach, runID)
	var closeFn func()
	if rt != nil {
		closeFn = rt.close
		rt.close = nil
	}
	// Sever the revoked credential's runner stream (if one is connected).
	if rs := c.runners[rec.CredHash]; rs != nil {
		delete(c.runners, rec.CredHash)
		rs.cancel()
	}
	c.mu.Unlock()

	if rt != nil {
		c.releaseSlot(rt)
	}
	if closeFn != nil {
		closeFn()
	}
	// Revocation severs the credential's parked long-poll AND its live run
	// channel (the channel teardown un-reserves tentative deliveries so the
	// leftover-mail check below sees them).
	c.severPoll(rec.Harp, ErrRevoked)
	c.severChan(rec.Harp)

	// The synthesized terminal notice: the parent ALWAYS learns of a child
	// death (blue-paper). Kind distinguishes a launch failure (error) from
	// a lifecycle end (exited).
	if rec.ParentHarp != "" {
		kind, body := KindExited, fmt.Sprintf("agent %q (session %s) exited (%s)", rec.Agent, rec.Harp, cause)
		if cause == CauseLaunchFailed {
			kind, body = "error", fmt.Sprintf("agent %q (session %s) failed to launch: %s", rec.Agent, rec.Harp, detail)
		} else if detail != "" {
			body += ": " + detail
		}
		if _, _, err := c.queueMail(rec.Harp, rec.ParentHarp, kind, body); err != nil {
			clidiag.Warn("ctxloom", "agent %s: queue terminal notice: %v", rec.Harp, err)
		}
	}
	c.spawner.MarkSessionEnded(rec.Harp)

	// A message that raced the death (queued after the last boundary drain)
	// must not strand: the ended-child delivery rule is resume (§6a).
	if cause != CauseStopped && c.pendingCount(rec.Harp) > 0 {
		go c.resumeChild(rec.Harp)
	}
}

// resumeChild relaunches an ended harp as a FRESH run attempt (new run id,
// new credential) so queued mail can be delivered as its next turn. The
// session-load machinery primes the fresh engine with the recorded history
// when the transcript is bound.
func (c *Coordinator) resumeChild(harp string) {
	var rec RunRecord
	found := false
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil && r.Ended {
			rec = *r
			found = true
		}
	})
	if !found {
		return
	}
	plan, err := c.spawner.Resolve(c.baseCtx, rec.Agent)
	if err != nil {
		clidiag.Warn("ctxloom", "agent resume %s: %v", harp, err)
		if _, _, qerr := c.queueMail(harp, rec.ParentHarp, "error", fmt.Sprintf("agent %q (session %s) could not be resumed: %v", rec.Agent, harp, err)); qerr != nil {
			clidiag.Warn("ctxloom", "agent %s: queue resume failure: %v", harp, qerr)
		}
		return
	}
	caller := Identity{Harp: rec.ParentHarp, Depth: rec.Depth - 1, Project: c.projectDir}
	rt, token, err := c.enqueueRun(caller, plan, harp, "", true)
	if errors.Is(err, errResumeLost) {
		return // a concurrent resume claimed it; the winner delivers
	}
	if err != nil {
		clidiag.Warn("ctxloom", "agent resume %s: %v", harp, err)
		return
	}
	c.audit("agent_resume", rec.ParentHarp, map[string]string{"harp": harp, "run_id": rt.runID})

	if !rt.slotHeld {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			return
		}
		c.mu.Lock()
		rt.slotHeld = true
		c.mu.Unlock()
	}
	c.setState(rt, StateExecuting)

	url, uerr := c.spawnReachURL(harp, plan.Runtime)
	if uerr != nil {
		c.failChild(rt, uerr)
		return
	}
	contextText := c.spawner.ResumeContext(c.baseCtx, plan, harp)
	launch, err := c.spawner.Launch(c.baseCtx, plan, contextText,
		c.childEnv(harp), runnerEnv(harp, rt.runID, token, url))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.attachLaunch(rt, launch)
	if msg, ok := c.takeNextMail(harp); ok {
		c.sendTurn(rt, msg.Body)
	}
	c.driveChild(rt, launch)
}

// driveQueued drives the recipient of an already-enqueued message per its
// fold state (§6a): resume an ended child or wake an idle one into a new
// turn; executing/parked/queued children reach the message at their own
// boundary. Returns the state the delivery observed.
func (c *Coordinator) driveQueued(harp string) string {
	state := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			state = r.State
		}
	})
	switch state {
	case StateEnded:
		go c.resumeChild(harp)
	case StateIdle:
		c.mu.Lock()
		rt := c.byHarp[harp]
		c.mu.Unlock()
		if rt != nil {
			select {
			case rt.wake <- struct{}{}:
			default:
			}
		}
	}
	return state
}

// onRolePark ties recv parking to the execution-slot accounting (§6a slot
// yield): a child parked in agent_recv consumes no compute, so its slot is
// released while it waits. Roles without a child attachment (the parent
// session) park without slot bookkeeping.
func (c *Coordinator) onRolePark(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	release := rt != nil && rt.slotHeld
	if release {
		rt.slotHeld = false
	}
	c.mu.Unlock()
	if release {
		c.setState(rt, StateParked)
		c.slots.release()
	}
}

// onRoleUnpark re-acquires the slot before a parked recv completes — the
// child resumes an EXECUTING turn, and the cap counts executing turns.
func (c *Coordinator) onRoleUnpark(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil || c.runState(rt.runID) != StateParked {
		return
	}
	if err := c.slots.acquire(c.baseCtx); err != nil {
		return
	}
	c.mu.Lock()
	rt.slotHeld = true
	c.mu.Unlock()
	c.setState(rt, StateExecuting)
}

// turnSlots is the D4 execution-slot queue: a fixed cap with FIFO waiters, so
// enqueued children start in spawn order. It is a runtime scheduling
// PRIMITIVE — the authoritative queue is the queueFold; waiters are rebuilt
// from it on restart (adoption).
type turnSlots struct {
	mu      sync.Mutex
	free    int
	waiters []chan struct{}
}

func newTurnSlots(n int) *turnSlots { return &turnSlots{free: n} }

func (s *turnSlots) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		return true
	}
	return false
}

func (s *turnSlots) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		s.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	s.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for i, w := range s.waiters {
			if w == ch {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				s.mu.Unlock()
				return ctx.Err()
			}
		}
		s.mu.Unlock()
		// The grant raced the cancel: the slot is ours, give it back.
		s.release()
		return ctx.Err()
	}
}

func (s *turnSlots) release() {
	s.mu.Lock()
	if len(s.waiters) > 0 {
		ch := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.mu.Unlock()
		close(ch)
		return
	}
	s.free++
	s.mu.Unlock()
}
