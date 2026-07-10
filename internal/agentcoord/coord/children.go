package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
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

	slotHeld bool
	oneshot  bool
	// viaStartRun marks a MIGRATED child (Wave C1): its engine control
	// rides StartRun on the runner's RunnerChannel — no go-plugin Chat
	// dial, no driveChild loop; turn delivery is push-down (§6a by child
	// state, decided runner-side), and its terminal is RunExited/runner
	// loss ONLY (lifecycle unification — the chat-close path never fires).
	viaStartRun bool
	in          chan<- agent.ChatMessage
	close       func()
	wake        chan struct{}
	turnOutput  []string // oneshot bridging: this turn's entries
	turnErrored bool     // oneshot bridging: this turn's Entry.IsError was set (Result.status)
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
			Ladder:     ladderToFact(plan.Ladder),
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

	// MIGRATED (C1 landed claude; C3 extended the allowlist to codex/kiro/
	// acp — see spawner.go's viaStartRunBackends): engine control rides
	// StartRun on the runner's RunnerChannel. A degraded spawn without
	// reach-back (url == "") cannot — the runner could never dial home — so
	// it keeps the legacy dial. This is now the go-plugin Chat dial's ONE
	// intentional, documented reachable case for an allowlisted backend
	// (Wave C4 kill-list verification: preserved as-is, per the plan's
	// explicit "check what C1 landed and preserve its documented behavior");
	// the dial's other reachable case is a StructuredChat backend outside
	// the allowlist (today: none in production, only test doubles).
	if rt.plan.ViaStartRun && url != "" {
		c.mu.Lock()
		rt.viaStartRun = true
		c.mu.Unlock()
		c.runChildViaStartRun(rt, prompt, token, url, "", rt.plan.Context)
		return
	}

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

// runnerAwaitTimeout bounds the wait for a just-spawned runner process to
// dial home before the spawn is declared failed (spawn + handshake + dial;
// container image pulls are NOT in this window — image staging happens in
// StartEngine's isolation prepare, before the clock starts).
const runnerAwaitTimeout = 60 * time.Second

// runChildViaStartRun is the MIGRATED spawn tail (C1): spawn the runner
// process (go-plugin handshake = process control only), await its
// RunnerChannel dial-home, and issue StartRun with the HarnessSpec built
// from the resolved plan — model through the resolveChatModel gate (it ran
// inside StartEngine's PrepareAgentChat), typed permission_mode, env + MCP +
// session harp in config, resume_session_id from the journal on a resume.
// prompt=="" (resume) sends no initial turn: queued mail arrives as turns.
func (c *Coordinator) runChildViaStartRun(rt *childRt, prompt, token, url, resumeSessionID, contextText string) {
	engine, err := c.spawner.StartEngine(c.baseCtx, rt.plan,
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.mu.Lock()
	rt.close = engine.Kill
	c.mu.Unlock()

	credHash := hashToken(token)
	actx, acancel := context.WithTimeout(c.baseCtx, runnerAwaitTimeout)
	_, err = c.awaitRunner(actx, credHash)
	acancel()
	if err != nil {
		c.failChild(rt, fmt.Errorf("child runner never dialed home (StartRun path): %w", err))
		return
	}

	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:         rt.plan.Backend,
		Model:           engine.Model,
		Workspace:       engine.WorkDir,
		Env:             engine.Env,
		MCPServers:      engine.MCPServers,
		SessionHarp:     rt.harp,
		Permission:      rt.plan.Perm,
		ResumeSessionID: resumeSessionID,
	})
	if err != nil {
		c.failChild(rt, err)
		return
	}
	// The composed context leads the first turn — the same join the legacy
	// path's leadContextIn performed, done once here (the runner writes
	// input.prompt verbatim as the first turn).
	first := operations.JoinLeadBlocks(contextText, prompt)
	var input *structpb.Struct
	if first != "" {
		if input, err = structpb.NewStruct(map[string]any{"prompt": first}); err != nil {
			c.failChild(rt, err)
			return
		}
	}
	rctx, rcancel := context.WithTimeout(c.baseCtx, defaultRequestTimeout)
	resp, err := c.requestRunner(rctx, credHash, &agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: &agentcoordpb.StartRun{
			RunId:   rt.runID,
			Harness: spec,
			Input:   input,
			Role:    rt.agentName,
		}},
	})
	rcancel()
	if err != nil {
		c.failChild(rt, fmt.Errorf("StartRun never completed: %w", err))
		return
	}
	if code := resp.GetStatus().GetCode(); code != 0 {
		c.failChild(rt, fmt.Errorf("StartRun refused: %s", resp.GetStatus().GetMessage()))
		return
	}
	// The journal proof (acceptance: no go-plugin Chat dial for a migrated
	// child): the interaction journal records start_run for this run — and
	// the legacy path's chat-close cause can never appear for it.
	c.audit("start_run", rt.harp, map[string]string{
		"run_id": rt.runID, "harness": rt.plan.Backend, "model": engine.Model,
		"resume_session_id": resumeSessionID,
	})
	if sid := resp.GetStartRun().GetHarnessSessionId(); sid != "" {
		c.recordHarnessSession(rt.runID, sid)
	}
	// Mail queued while the engine was coming up drains now, as turns.
	if c.pendingCount(rt.harp) > 0 {
		c.pushMail(rt.harp)
	}
}

// recordHarnessSession journals the run's harness-native session id (the
// resume handle) — idempotent on the same id. Sources: StartRunResult, the
// engine host's ctxloom/harness_session event, and RunExited.
func (c *Coordinator) recordHarnessSession(runID, sessionID string) {
	if sessionID == "" {
		return
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(runID)
		if r == nil || r.HarnessSessionID == sessionID {
			return nil, nil
		}
		return []Fact{factAt(factRunHarness, c.now(), runHarness{RunID: runID, HarnessSessionID: sessionID})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: record harness session id: %v", err)
	}
}

// onTurnStarted folds a migrated child's engine-reported turn start into the
// §6a roster state and the D4 slot accounting. The acquire is BEST-EFFORT
// (tryAcquire): this runs on the RunChannel's receive path, which must not
// block behind another child's turn — the strict queue discipline lives at
// spawn time (runChild's blocking acquire); a mid-life race can transiently
// exceed the cap by one, which the C1 window accepts and documents.
func (c *Coordinator) onTurnStarted(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	need := rt != nil && !rt.slotHeld
	c.mu.Unlock()
	if rt == nil {
		return
	}
	if need && c.slots.tryAcquire() {
		c.mu.Lock()
		rt.slotHeld = true
		c.mu.Unlock()
	}
	c.setState(rt, StateExecuting)
}

// onTurnIdle folds the turn-boundary: state idle, slot yielded, and any mail
// that queued mid-turn pushes now (§6a "queued mid-turn → deliver at the
// next boundary" — the runner-side driver also queues internally; this push
// covers mail that arrived while no channel push was possible).
func (c *Coordinator) onTurnIdle(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	c.setState(rt, StateIdle)
	c.releaseSlot(rt)
	if c.pendingCount(role) > 0 {
		c.pushMail(role)
	}
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
			if ev.Entry.IsError {
				rt.turnErrored = true
			}
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
	errored := rt.turnErrored
	rt.turnOutput = nil
	rt.turnErrored = false
	oneshot := rt.oneshot
	c.mu.Unlock()
	if oneshot && len(out) > 0 {
		text := strings.Join(out, "\n")
		// Durable event-log record (Wave C4, manly-grant (7)) ALONGSIDE the
		// existing parent-mailbox bridge below — not a replacement for it;
		// PublishEvents is event-plane only and carries no delivery
		// semantics of its own.
		c.publishOneshotResult(rt, text, errored)
		if _, _, err := c.queueMail(rt.harp, rt.parentHarp, "result", text); err != nil {
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

// publishOneshotResult journals ONE oneshot turn's stdout-bridged output as a
// self-contained, FRESH-run_id sub-run over the unary PublishEvents fallback
// (Wave C4 deliverable 1, closing manly-grant (7): "Oneshot-fallback
// children → unary PublishEvents is a natural fit"). A oneshot backend has no
// persistent engine or RunChannel of its own — startOneshot's own doc says
// "no session continuity between turns beyond the composed context" — so each
// completed turn genuinely IS its own independent run; that is what a fresh
// run_id per turn captures, and it is what keeps RunCompleted's contract
// invariant true ("terminal event; nothing may follow it for this run_id").
// The persistent child's OWN identity (rt.runID/rt.harp — roster, queue slot,
// mailbox) is untouched by this: it is purely an additional durable
// event-log record, never a substitute for the mailbox delivery above.
func (c *Coordinator) publishOneshotResult(rt *childRt, text string, errored bool) {
	runStatus := agentcoordpb.Result_RUN_STATUS_SUCCEEDED
	var errStatus *rpcstatus.Status
	if errored {
		runStatus = agentcoordpb.Result_RUN_STATUS_FAILED
		errStatus = &rpcstatus.Status{Code: int32(codes.Unknown), Message: text}
	}
	ev := &agentcoordpb.AgentEvent{
		RunId:      newRunID(),
		Seq:        1,
		OccurredAt: timestamppb.Now(),
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: runStatus, Text: text, Error: errStatus},
		}},
	}
	resp := c.PublishEvents([]*agentcoordpb.AgentEvent{ev})
	for _, rej := range resp.GetRejected() {
		clidiag.Warn("ctxloom", "agent %s: oneshot result publish rejected: %s", rt.harp, rej.GetReason().GetMessage())
	}
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
	// The ACCEPT_FOR_SESSION cache (C2) is run-scoped: it must not outlive
	// the run it was granted for (a resumed harp gets a fresh run_id and
	// starts its ladder clean).
	c.clearSessionAccepts(runID)

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

	// MIGRATED resume (C1): respawn via StartRun with the JOURNALED
	// harness-native session id — the engine continues its own recorded
	// session (ACP session/load); no transcript re-priming needed. A prior
	// run that never reported a session id falls back to the rendered-
	// history context prime, still over StartRun. Queued mail is pushed as
	// turns once the engine attaches (runChildViaStartRun's drain).
	if plan.ViaStartRun && url != "" {
		c.mu.Lock()
		rt.viaStartRun = true
		c.mu.Unlock()
		contextText := ""
		if rec.HarnessSessionID == "" {
			contextText = c.spawner.ResumeContext(c.baseCtx, plan, harp)
		}
		c.runChildViaStartRun(rt, "", token, url, rec.HarnessSessionID, contextText)
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
		migrated := rt != nil && rt.viaStartRun
		c.mu.Unlock()
		switch {
		case migrated:
			// Push-down delivers to the runner; ITS driver starts the new
			// turn (§6a decided runner-side for migrated children).
			c.pushMail(harp)
		case rt != nil:
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
