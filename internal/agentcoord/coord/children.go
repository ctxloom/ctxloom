package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
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
	// maxAgentDepth is the recursion guard: depth 0 (the session owner)
	// spawns depth 1; depth 1 spawns depth 2 (D5, manly-grant (4): the
	// B-window grandchild refusal is lifted — flat-hub semantics, each hop
	// addresses only its own direct parent via peerSend's ParentHarp lookup,
	// generic at any depth). depth 2 may not spawn depth 3 — the guard still
	// bottoms out somewhere; raising it further is a future, undecided
	// change, not implied by this one.
	maxAgentDepth = 2
	// agentTurnCap is the BUILT-IN DEFAULT for turnSlots' cap — a
	// configurable RESOURCE ceiling on concurrently EXECUTING child turns
	// (live engine processes), NOT a correctness gate: the coordinator's own
	// state is partitioned by child identity (maps keyed by harp/runID/
	// role/credHash/msgID, decisive steps atomic inside one journal Exec or
	// one c.mu window) and is safe under real concurrency by construction —
	// proven by TestCoordinator_ConcurrentTurnsInvariants
	// (turncap_concurrent_test.go), which runs children genuinely
	// overlapping (a shared barrier forces it) under -race -count=20 and
	// asserts run/mail/credential/slot invariants hold. Was D4's serial-1:
	// "children execute serially until the isolation concurrency defects
	// land" — that landed (fix/turncap-to-resource-ceiling); the cap now
	// exists purely to bound concurrent process/resource load (this
	// project's own history hit load 10.12 at ~200 concurrent procs), not
	// to protect coordinator correctness. Production sources the live value
	// from config.Config.GetAgentTurnCap (Options.TurnCap); <= 0 (unset)
	// falls back to this constant. The cap counts EXECUTING turns only — a
	// child parked in agent_recv or idle at a turn boundary yields its slot
	// (turnSlots is a resource limiter: the slot is acquired before
	// spawner.Launch/StartEngine, never a serialization primitive).
	agentTurnCap = 4

	// defaultEndedRunTail / defaultEndedRunMaxAge are the one-shot retention
	// reap bounds (one-shot-resume plan, Slice 4 / Fork 2.3), overridable via
	// Options. The reap keeps every harp's CURRENT run (the resume key lives
	// there) plus the newest defaultEndedRunTail ended runs across all harps,
	// and drops any ended, non-current run beyond the tail OR older than
	// defaultEndedRunMaxAge. Chosen so a normal session keeps a generous audit
	// tail while a long-running one-shot session's per-turn ended records stop
	// accumulating without bound. On-disk journal truncation stays deferred
	// (Wave E); this bounds the LIVE fold maps. Values ESCALATED for a nod.
	defaultEndedRunTail   = 64
	defaultEndedRunMaxAge = 30 * time.Minute
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
	// parentRunID (D5) is the spawning run's run_id — empty for a depth-1
	// child (the depth-0 session owner has no run_id of its own); set for
	// a depth-2+ grandchild. Threaded onto the outgoing StartRun request
	// (runChildViaStartRun) so RunStarted.parent_run_id carries durable
	// lineage on the log (manly-grant (5)) — mirrors RunRecord.ParentRunID.
	parentRunID string
	plan        *SpawnPlan

	slotHeld bool
	oneshot  bool
	// launchCancel cancels the context this run's engine was LAUNCHED under
	// (launchgate.go). It is the run's, not the launch call's: a spawner may
	// tie the engine's lifetime to that context, so it is fired exactly once,
	// at the run terminal (terminateRun), never when the launch call returns.
	// agent_stop reaches an attempt through the per-harp registration
	// instead, which also covers an attempt that has no run yet.
	launchCancel context.CancelFunc
	// ownerRun marks a top-level, OWNER-OWNED run (Phase 2a-B, StartOwnedRun):
	// the owning session's own structured/oneshot container run, minted
	// parent-less with the OWNER'S HARP reused as its run role. It rides the
	// SAME migrated RunChannel machinery a delegated child does (viaStartRun),
	// but has no distinct parent to report to — the host watches it directly
	// via WatchRuns — so the automatic child→parent result bridge
	// (bridgeTurnResult) MUST be suppressed for it: bridging to the owner's
	// own mailbox (parentHarp == harp) would re-deliver the run's output as its
	// own next turn, an infinite self-loop. Everything else (turn state, slot
	// accounting, mailbox-borne follow-up turns) is identical to a migrated
	// child.
	ownerRun bool
	// viaStartRun marks a MIGRATED child (Wave C1): its engine control
	// rides StartRun on the runner's RunnerChannel — no go-plugin Chat
	// dial, no driveChild loop; turn delivery is push-down (§6a by child
	// state, decided runner-side), and its terminal is RunExited/runner
	// loss ONLY (lifecycle unification — the chat-close path never fires).
	viaStartRun bool
	in          chan<- agent.ChatMessage
	close       func()
	wake        chan struct{}
	turnOutput  []string // result bridging: this turn's assistant output (see bridgeTurnResult)
	turnErrored bool     // result bridging: this turn's Entry.IsError was set (Result.status)
	// selfReported records that the CHILD ITSELF sent mail to its parent
	// during the current turn (peerSend, caller.IsChild()). It is the
	// no-double-delivery discriminator for bridgeTurnResult: a child that
	// reported in its own words is not re-reported in ours. Cleared at the
	// TURN BOUNDARY (bridgeTurnResult), never at turn start: a child may
	// call agent_send before it says anything at all, and a start-of-turn
	// reset would race that report away.
	selfReported bool
	// finalMsgs is the MIGRATED path's turn accumulator index: the plane-1
	// message ids whose MessageStarted declared MESSAGE_CHANNEL_FINAL, so
	// their deltas (and only theirs — never REASONING or LOG) join
	// turnOutput. Cleared at the turn boundary alongside it.
	finalMsgs map[string]bool

	// attached closes once THIS attempt's launch decision is final: the
	// engine is up (legacy: attachLaunch ran, right before the driveChild
	// loop starts; migrated: the StartRun round-trip completed) or the
	// attempt failed (failChild). It backs awaitChildUp/armLaunch (S3,
	// flaky-agentcoord) — a test-facing deterministic quiesce seam over the
	// launch/resume pipeline, replacing a wall-clock Eventually poll with a
	// wait keyed on the actual goroutine's own progress. Set once at
	// creation (enqueueRun); markAttached closes it exactly once.
	attached chan struct{}
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
//
// workspace is GAP 2's per-call workspace-axis override (none|worktree;
// empty = project default, cfg.Workspace). Unlike the runtime axis — an
// AGENT trait Resolve already carries on the plan — the workspace axis is an
// ORCHESTRATION trait the CALLER supplies per invocation, mirroring
// operations.memberAxes' map/weave --workspace shape: it is set on the
// resolved plan here, never inside Resolve (agent-definition resolution
// stays pure — see spawner.go's Resolve/GAP 1).
//
// dirtyTreeHandler is the identical per-call override for what a worktree
// spawn does when the parent tree is dirty (commit|copy|stale|fail; empty =
// project default, cfg.GetDirtyTreeHandler()) — see
// operations.handleDirtyParentTree. Deliberately does NOT carry any
// acknowledgement for the "commit" handler's mutation: that is a
// per-project, human-only config flag (dirty_tree_commit_ack) that this
// per-call parameter can never set — see config.Config.dirtyTreeCommitAck's
// doc for why.
func (c *Coordinator) AgentRun(ctx context.Context, caller Identity, agentName, prompt, workspace, dirtyTreeHandler string) (*RunOutcome, error) {
	if agentName == "" {
		return nil, errors.New("agent_run: agent is required (a configured agent name; see `ctxloom agent list`)")
	}
	if prompt == "" {
		return nil, errors.New("agent_run: prompt is required (the child's briefing/first turn)")
	}
	// Depth derives from the CREDENTIAL, never from env (review R11). D5
	// lifts the B-window grandchild refusal (maxAgentDepth=2): a depth-1
	// child may now spawn a depth-2 grandchild; the guard still bottoms out
	// at depth 2 spawning depth 3.
	if caller.Depth >= maxAgentDepth {
		return nil, fmt.Errorf("agent_run: refused: this session is already at the maximum delegation depth (%d) — report the work back to your coordinator (agent_send to \"parent\") and let it fan out", caller.Depth)
	}

	plan, err := c.spawner.Resolve(ctx, agentName)
	if err != nil {
		return nil, err
	}
	plan.Workspace = workspace
	plan.DirtyTreeHandler = dirtyTreeHandler

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

	rt, token, err := c.enqueueRun(caller, plan, harp, prompt, false, make(chan struct{}))
	if err != nil {
		return nil, err
	}
	c.audit("agent_run", caller.Harp, map[string]string{"agent": agentName, "harp": harp, "run_id": rt.runID})

	c.goTracked(func() { c.runChild(rt, prompt, token, url) })

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
// serialized window so concurrent resumes cannot double-launch. attached is
// the childRt's "settled" signal (S3, flaky-agentcoord): AgentRun's own
// synchronous call passes a fresh channel; resumeChild passes the ONE
// channel its own dispatcher (armLaunch) created for THIS specific attempt
// — never looked up by harp here, so a concurrent second dispatch for the
// same harp (driveQueued's StateEnded case racing terminateRun's
// leftover-mail tail — both legitimate, "the winner delivers") can never
// cross-close another attempt's channel.
func (c *Coordinator) enqueueRun(caller Identity, plan *SpawnPlan, harp, prompt string, resume bool, attached chan struct{}) (*childRt, string, error) {
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
			RunID:       runID,
			Harp:        harp,
			Agent:       plan.AgentName,
			ParentHarp:  caller.Harp,
			ParentRunID: caller.RunID,
			Runtime:     plan.Runtime,
			CredHash:    credHash,
			Depth:       caller.Depth + 1,
			Prompt:      prompt,
			Resume:      resume,
			Ladder:      ladderToFact(plan.Ladder),
			Permission:  plan.Perm.String(),
			MCPServers:  mcpServerNames(plan.MCPServers),
		})}, nil
	}); err != nil {
		return nil, "", err
	}
	if !won {
		return nil, "", errResumeLost
	}

	rt := &childRt{
		runID:       runID,
		harp:        harp,
		agentName:   plan.AgentName,
		parentHarp:  caller.Harp,
		parentRunID: caller.RunID,
		plan:        plan,
		wake:        make(chan struct{}, 1),
		attached:    attached,
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

// armLaunch synchronously mints and registers a FRESH "attached" channel for
// harp's next launch attempt — called by a dispatcher (driveQueued's
// StateEnded case, terminateRun's leftover-mail resume tail) in ITS OWN
// goroutine, BEFORE spawning the async resumeChild goroutine that owns the
// returned channel end-to-end (passed straight through to enqueueRun, never
// looked up again by harp). This ordering is what makes awaitChildUp
// race-free: a caller synchronously after the dispatch (AgentSend, in a
// test) is guaranteed c.launchArmed already reflects the attempt it just
// triggered.
//
// APPENDS, never overwrites: two dispatchers can legitimately race to arm
// the SAME harp (driveQueued's own StateEnded case racing terminateRun's
// leftover-mail tail for the SAME queued message — both correct, "the
// winner delivers" per errResumeLost's doc). An earlier overwrite-slot
// design lost track of an in-flight attempt entirely the moment a SECOND
// dispatch armed the same harp: awaitChildUp then watched only the newest
// one, and if THAT one happened to be the loser (settling near-instantly
// via errResumeLost) while the FIRST was the actual winner (still working
// through StartEngine), it returned before the real outcome even existed.
// Keeping every not-yet-settled channel in the slice is what lets
// awaitChildUp wait on all of them at once (waitAnyClosed) instead of
// assuming "whichever armed last" is the one that will win enqueueRun.
func (c *Coordinator) armLaunch(harp string) chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	c.launchArmed[harp] = append(c.launchArmed[harp], ch)
	c.mu.Unlock()
	return ch
}

// markAttached closes rt's attached signal exactly once (idempotent: a
// launch failure path and a success path never both fire for the same
// attempt, but the nil-out makes a stray double-call harmless regardless).
func (c *Coordinator) markAttached(rt *childRt) {
	c.mu.Lock()
	ch := rt.attached
	rt.attached = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// awaitChildUp blocks until harp's launch/resume activity has settled to a
// REAL outcome — attached (legacy: engine attached and about to start
// driving turns; migrated: StartRun round-tripped) or failed (failChild) —
// never merely a benign "lost the race" no-op (errResumeLost / resumeChild's
// !found). It is the flaky-agentcoord S3 seam: a test replaces the FIRST
// require.Eventually poll in a spawn/resume-gated chain with this, keying
// the wait on the tracked goroutine's own progress instead of a wall-clock
// guess — any Eventually further down the SAME chain (e.g. a durable-fold
// fsync) now resolves in µs-ms, not seconds, because its precondition is
// already true.
//
// Two independent signals matter, and NEITHER alone is reliable:
//   - c.byHarp[harp].attached is authoritative once it exists (enqueueRun
//     only ever sets c.byHarp[harp] on a WINNING attempt) but does not exist
//     yet during the window between a dispatch and its resumeChild goroutine
//     actually reaching enqueueRun.
//   - c.launchArmed[harp] covers that window (armLaunch appends to it
//     SYNCHRONOUSLY in the dispatcher, before the async goroutine starts).
//     Two dispatchers can race to arm the SAME harp — armLaunch's doc — so
//     this call waits on EVERY currently-armed, not-yet-settled channel at
//     once (waitAnyClosed), pruning settled ones as it notices them; it
//     never assumes "whichever armed most recently" is the one that will
//     actually win enqueueRun.
//
// Loop: prefer c.byHarp[harp].attached whenever it is live (covers AgentRun's
// own fresh, never-armed channel, and is simply the fastest path once a
// resume's rt exists too); otherwise prune c.launchArmed[harp] to the
// channels still open and wait for the first of them to close, then
// re-evaluate — a resume's rt may now exist, or more may have been armed.
// Bounded by ctx throughout — a genuinely pathological retry storm still
// respects the caller's deadline. A harp with no history at all (or nothing
// currently pending) returns immediately.
func (c *Coordinator) awaitChildUp(ctx context.Context, harp string) error {
	for {
		c.mu.Lock()
		rt := c.byHarp[harp]
		if rt != nil && rt.attached != nil {
			ch := rt.attached
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var pending []chan struct{}
		for _, ch := range c.launchArmed[harp] {
			select {
			case <-ch:
				// Already closed (a settled no-op, or a real outcome we
				// simply have not visited yet via rt.attached) — drop it.
			default:
				pending = append(pending, ch)
			}
		}
		c.launchArmed[harp] = pending
		if len(pending) == 0 {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		if err := waitAnyClosed(ctx, pending); err != nil {
			return err
		}
	}
}

// waitAnyClosed blocks until any channel in chs closes, or ctx ends.
// Test-only fan-in over a dynamic channel count (reflect.Select is the
// standard idiom for this — no production path ever calls awaitChildUp, so
// the reflection cost is immaterial).
func waitAnyClosed(ctx context.Context, chs []chan struct{}) error {
	cases := make([]reflect.SelectCase, 0, len(chs)+1)
	for _, ch := range chs {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)})
	}
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	chosen, _, _ := reflect.Select(cases)
	if chosen == len(cases)-1 {
		return ctx.Err()
	}
	return nil
}

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
// back to its local mode. coordinatorCapable threads the spawned child's
// resolved Coordinator flag (SpawnPlan.Coordinator) via EnvAgentCoordinator —
// the trust-boundary gate's plumbing seam, deliberately NOT the wire (untyped
// env map, no proto change) — so the runner can compute leaf-vs-coordinator
// and gate the coordinator-only MCP tools (internal/cli/llm_serve.go,
// mcp_runner.go).
func runnerEnv(harp, runID, token, url string, coordinatorCapable bool) map[string]string {
	env := map[string]string{
		"CTXLOOM_SESSION_HARP": harp,
	}
	if url != "" {
		env[EnvCoordURL] = url
		env[EnvCoordCred] = token
		env[EnvRunID] = runID
	}
	if coordinatorCapable {
		env[EnvAgentCoordinator] = "1"
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
	// The launch runs under a CANCELLABLE per-harp context, not baseCtx, so
	// agent_stop reaches a launch that is in flight (container prepare, image
	// pull, fs probe) and not merely the process a completed launch produced
	// — see launchgate.go.
	lctx, lcancel, deregister := c.launchContext(rt.harp)
	defer deregister()
	c.mu.Lock()
	rt.launchCancel = lcancel
	c.mu.Unlock()

	if rt.plan.ViaStartRun && url != "" {
		c.mu.Lock()
		rt.viaStartRun = true
		c.mu.Unlock()
		c.runChildViaStartRun(lctx, rt, prompt, token, url, "", rt.plan.Context)
		return
	}

	launch, err := c.spawner.Launch(lctx, rt.plan, rt.plan.Context, "",
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url, rt.plan.Coordinator))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.noteLaunchAttached(rt.harp)
	c.attachLaunch(rt, launch)
	c.sendTurn(rt, prompt)
	c.markAttached(rt) // the engine is up; driveChild below drives its whole lifetime, not just the launch
	c.driveChild(rt, launch)
}

// defaultRunnerAwaitTimeout is the package default for the wait for a
// just-spawned runner process to dial home before the spawn is declared
// failed (spawn + handshake + dial; container image pulls are NOT in this
// window — image staging happens in StartEngine's isolation prepare, before
// this clock starts). Overridable per-coordinator via Options.
// RunnerAwaitTimeout (coordinator.go), which is what issueStartRun actually
// reads (c.runnerAwaitTimeout).
//
// Widened from an original 60s (2026-07-24 retune, fix/launch-retry-budget):
// 60s was tight enough that a genuinely slow-but-successful container start
// under host contention (loaded Docker daemon, DinD nesting, a busy bridge
// network) could be declared a launch FAILURE while the runner was still on
// its way up — indistinguishable, from here, between "broken" and "slow".
// Backoff spacing between separate launch ATTEMPTS (launchgate.go) is a
// different budget and is deliberately left small: these two knobs answer
// different questions ("how long do we tolerate one attempt" vs "how long
// between attempts") and conflating them was the original miscalibration.
const defaultRunnerAwaitTimeout = 5 * time.Minute

// runChildViaStartRun is the MIGRATED spawn tail (C1): spawn the runner
// process (go-plugin handshake = process control only), await its
// RunnerChannel dial-home, and issue StartRun with the HarnessSpec built
// from the resolved plan — model through the resolveChatModel gate (it ran
// inside StartEngine's PrepareAgentChat), typed permission_mode, env + MCP +
// session harp in config, resume_session_id from the journal on a resume.
// prompt=="" (resume) sends no initial turn: queued mail arrives as turns.
// ctx is the caller's CANCELLABLE launch context (launchgate.go), not
// baseCtx: agent_stop cancels it to abort a spawn that is still in flight.
func (c *Coordinator) runChildViaStartRun(ctx context.Context, rt *childRt, prompt, token, url, resumeSessionID, contextText string) {
	engine, err := c.spawner.StartEngine(ctx, rt.plan,
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url, rt.plan.Coordinator))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.mu.Lock()
	rt.close = engine.Kill
	c.mu.Unlock()

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
	_ = c.issueStartRun(ctx, rt, hashToken(token), spec, first, engine.Model, resumeSessionID)
}

// issueStartRun is the shared StartRun-issuing tail (Phase 2a-B factored this
// out of runChildViaStartRun so the owner-owned run, StartOwnedRun, reuses the
// identical wire crossing): await the runner's dial-home for credHash, issue
// StartRun with spec + the composed first-turn input on the RunnerChannel,
// check the result, journal the start_run audit + any harness session id, drain
// mail queued during standup, and mark rt attached. On any failure it routes
// through failChild (exactly-once terminal) and returns the error. role is
// rt.agentName (a delegated child's agent name, or — for an owner-owned run —
// the owner's own harp, §5.B2).
// ctx is the launch context: cancelling it (agent_stop) aborts the
// dial-home wait instead of holding the harp for the full
// c.runnerAwaitTimeout.
func (c *Coordinator) issueStartRun(ctx context.Context, rt *childRt, credHash string, spec *agentcoordpb.HarnessSpec, first, model, resumeSessionID string) error {
	actx, acancel := context.WithTimeout(ctx, c.runnerAwaitTimeout)
	_, err := c.awaitRunner(actx, credHash)
	acancel()
	if err != nil {
		err = fmt.Errorf("runner never dialed home (StartRun path): %w", err)
		c.failChild(rt, err)
		return err
	}
	var input *structpb.Struct
	if first != "" {
		if input, err = structpb.NewStruct(map[string]any{"prompt": first}); err != nil {
			c.failChild(rt, err)
			return err
		}
	}
	rctx, rcancel := context.WithTimeout(c.baseCtx, defaultRequestTimeout)
	resp, err := c.requestRunner(rctx, credHash, &agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: &agentcoordpb.StartRun{
			RunId:       rt.runID,
			Harness:     spec,
			Input:       input,
			Role:        rt.agentName,
			ParentRunId: rt.parentRunID, // D5: durable lineage on the log (manly-grant (5)) — enginehost.go echoes this into RunStarted
		}},
	})
	rcancel()
	if err != nil {
		err = fmt.Errorf("StartRun never completed: %w", err)
		c.failChild(rt, err)
		return err
	}
	if code := resp.GetStatus().GetCode(); code != 0 {
		err = fmt.Errorf("StartRun refused: %s", resp.GetStatus().GetMessage())
		c.failChild(rt, err)
		return err
	}
	// The journal proof (acceptance: no go-plugin Chat dial for a migrated
	// child): the interaction journal records start_run for this run — and
	// the legacy path's chat-close cause can never appear for it.
	c.audit("start_run", rt.harp, map[string]string{
		"run_id": rt.runID, "harness": rt.plan.Backend, "model": model,
		"resume_session_id": resumeSessionID,
	})
	if sid := resp.GetStartRun().GetHarnessSessionId(); sid != "" {
		c.recordHarnessSession(rt.runID, sid)
	}
	// Mail queued while the engine was coming up drains now, as turns.
	if c.pendingCount(rt.harp) > 0 {
		c.pushMail(rt.harp)
	}
	c.noteLaunchAttached(rt.harp) // a launch that came up resets the retry budget
	c.markAttached(rt)            // StartRun round-tripped: the migrated run is up
	return nil
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

// recordResumable journals the run engine's LIVE resume capability
// (ChatSessionInfo.Resumable — ACP's initialize-time loadSession bit) — the
// one-shot resume gate's live half (one-shot-resume plan, Slice 4 / Fork 3).
// Idempotent on an unchanged value; only ever recorded true (a false is the
// zero value already, and a later true must not be silently ignored).
func (c *Coordinator) recordResumable(runID string, resumable bool) {
	if !resumable {
		return
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(runID)
		if r == nil || r.Resumable == resumable {
			return nil, nil
		}
		return []Fact{factAt(factRunResumable, c.now(), runResumable{RunID: runID, Resumable: resumable})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: record run resumable: %v", err)
	}
}

// resumeKeyFor is the resume key formalized as a single accessor (one-shot-
// resume plan Slice 1): (harp, HarnessSessionID) on the harp's CURRENT run —
// the handle a resume threads back into the backend (StartRun's
// ResumeSessionID / Spawner.Launch's resumeSessionID) so the engine
// continues its OWN session instead of a rendered-transcript replay. ok is
// false when harp has no run yet, or its current run never reported a
// native session id.
//
// NOT used by resumeChild itself: by the time a resume wants this key, it
// means the JUST-ENDED run's handle (captured in its own local rec before
// enqueueRun mints the fresh one), and "harp's CURRENT run" would instead
// resolve to that brand-new run — necessarily keyless, since it has not
// reported a session id yet. This accessor is for callers who want the
// latest committed resume handle for a harp from outside that narrow
// window (tests, and Slice 2's per-engine gating).
func (c *Coordinator) resumeKeyFor(harp string) (sessionID string, ok bool) {
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			sessionID = r.HarnessSessionID
		}
	})
	return sessionID, sessionID != ""
}

// onTurnStarted folds a migrated child's engine-reported turn start into the
// §6a roster state and the D4 slot accounting. The acquire is BEST-EFFORT
// (tryAcquire): this runs on the RunChannel's receive path, which must not
// block behind another child's turn — the strict queue discipline lives at
// spawn time (runChild's blocking acquire). claimSlotIntent (R1, one-shot-
// resume plan Slice 3) makes the check-then-acquire atomic: a racing
// onRoleUnpark/onTurnStarted pair for the SAME rt can no longer both
// tryAcquire and both set slotHeld — exactly one wins the claim, and a
// failed tryAcquire rolls the claim back rather than leaking a
// permanently-stuck-true slotHeld bit against no actually-held slot.
func (c *Coordinator) onTurnStarted(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	if c.claimSlotIntent(rt) && !c.slots.tryAcquire() {
		c.releaseSlotIntent(rt)
	}
	c.setState(rt, StateExecuting)
}

// noteChildReported marks that a child sent mail to its parent itself —
// peerSend's hook into the no-double-delivery rule (see bridgeTurnResult).
func (c *Coordinator) noteChildReported(harp string) {
	c.mu.Lock()
	if rt := c.byHarp[harp]; rt != nil {
		rt.selfReported = true
	}
	c.mu.Unlock()
}

// accumulateFinalText folds one MIGRATED child's plane-1 message events into
// its turn accumulator: MessageStarted on MESSAGE_CHANNEL_FINAL opens an
// accumulating message id; that id's deltas append. REASONING (thinking)
// and LOG (system chatter) are deliberately excluded — a coordinator wants
// the child's ANSWER, not its scratchpad.
func (c *Coordinator) accumulateFinalText(role string, ev *agentcoordpb.AgentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.byHarp[role]
	if rt == nil || rt.ownerRun {
		// An owner-owned run's answer is rendered host-side from the WatchRuns
		// stream (watch.broadcast, above the switch in handleAgentEvent), never
		// bridged to a parent — so there is nothing to accumulate here, and
		// accumulating without a bridge that clears it would leak per turn.
		return
	}
	switch p := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_MessageStarted:
		if p.MessageStarted.GetChannel() != agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL {
			return
		}
		if rt.finalMsgs == nil {
			rt.finalMsgs = make(map[string]bool)
		}
		rt.finalMsgs[p.MessageStarted.GetMessageId()] = true
	case *agentcoordpb.AgentEvent_MessageDelta:
		if !rt.finalMsgs[p.MessageDelta.GetMessageId()] {
			return
		}
		if text := p.MessageDelta.GetText(); text != "" {
			rt.turnOutput = append(rt.turnOutput, text)
		}
	}
}

// bridgeTurnResult is the AUTOMATIC child→parent report (blunt-whiff): at a
// child's turn boundary, whatever the child said this turn lands in its
// parent's mailbox as `kind: "result"` — WITHOUT the child's model having to
// decide to call agent_send. Receiving a delegated child's result must not
// depend on model cooperation; that is the property a coordinator needs, and
// the reason this no longer keys off whether the backend implements
// agent.StructuredChat (it fired only for backends that DON'T, and every
// registered backend does — so in production it never fired at all).
//
// NO DOUBLE DELIVERY. The bridge is a FALLBACK, not a duplicate: a child that
// called agent_send to its parent during this turn has already reported, in
// its own words, and rt.selfReported (set by peerSend) suppresses the bridge
// for that turn. So a parent sees a child's turn exactly once — the child's
// own message when it wrote one, ours when it didn't.
//
// A turn that ends with NEITHER a self-report NOR any FINAL-channel output
// produced nothing to deliver; that is warned rather than queued as an empty
// message (an empty body is this project's signature silent no-op, not a
// report).
func (c *Coordinator) bridgeTurnResult(rt *childRt) {
	c.mu.Lock()
	out := rt.turnOutput
	errored := rt.turnErrored
	reported := rt.selfReported
	rt.turnOutput = nil
	rt.turnErrored = false
	rt.selfReported = false
	rt.finalMsgs = nil
	oneshot := rt.oneshot
	// The MIGRATED path accumulates plane-1 message DELTAS (fragments of one
	// message, concatenated); the legacy path accumulates whole entries
	// (newline-separated, as the pre-existing oneshot bridge joined them).
	sep := "\n"
	if rt.viaStartRun {
		sep = ""
	}
	c.mu.Unlock()

	if reported {
		return // the child reported itself; never deliver the same turn twice
	}
	text := strings.TrimSpace(strings.Join(out, sep))
	if text == "" {
		clidiag.Warn("ctxloom", "agent %s: turn ended with no report and no output — nothing to bridge to %s", rt.harp, rt.parentHarp)
		return
	}
	if oneshot {
		// Durable event-log record (Wave C4, manly-grant (7)) ALONGSIDE the
		// mailbox bridge below — not a replacement for it; PublishEvents is
		// event-plane only and carries no delivery semantics of its own. A
		// MIGRATED child already emits its own RunCompleted on the
		// RunChannel, so this synthetic sub-run is oneshot-only.
		c.publishOneshotResult(rt, text, errored)
	}
	if _, _, err := c.queueMail(rt.harp, rt.parentHarp, "result", text); err != nil {
		clidiag.Warn("ctxloom", "agent %s: bridge turn result: %v", rt.harp, err)
	}
}

// oneShotReady reports whether rt's NEXT turn boundary must end the engine
// process and resume-by-key rather than park it warm (one-shot-resume plan,
// Slice 4). All three conditions are required — any missing one falls back to
// the persistent warm-engine model (no regression, no stranding):
//   - the resolved agent asked for it (SpawnPlan.ResumeMode == ResumeModeOneShot,
//     the STATIC per-engine gate from Slice 2);
//   - the LIVE engine advertised a resume-by-key capability this run
//     (RunRecord.Resumable — the loadSession live-confirm from piece 1); a
//     statically-capable engine whose adapter did not actually advertise it
//     would fail loud at session/load AFTER we tore it down — the exact
//     stranding this gate prevents;
//   - a native session key was actually captured (HarnessSessionID) — without
//     one the resume would silently degrade to a lossy transcript replay,
//     which is not one-shot at all.
func (c *Coordinator) oneShotReady(rt *childRt) bool {
	if rt == nil || rt.plan == nil || rt.plan.ResumeMode != ResumeModeOneShot {
		return false
	}
	ready := false
	c.runs.View(func() {
		if r := c.runsF.run(rt.runID); r != nil {
			ready = r.Resumable && r.HarnessSessionID != ""
		}
	})
	return ready
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
	// The MIGRATED path's turn boundary: bridge this turn's result to the
	// parent BEFORE anything below, so the parent's mailbox carries the
	// child's answer whether or not the child's model chose to send one. An
	// OWNER-OWNED run (Phase 2a-B) has no distinct parent — the host watches it
	// directly — and bridging to its own harp would self-loop, so it is
	// suppressed (childRt.ownerRun's doc).
	if !rt.ownerRun {
		c.bridgeTurnResult(rt)
	}
	// ONE-SHOT (Slice 4): a driving:oneshot child that is live-confirmed
	// resumable tears its engine down at the clean turn boundary instead of
	// parking it warm. terminateRun (exactly-once) releases the slot, kills
	// the engine, and — because CauseOneShotBoundary != CauseStopped — leaves
	// the harp resumable: pending mail resumes it immediately (terminateRun's
	// own tail), an empty mailbox waits for the next agent_send (driveQueued's
	// StateEnded → resumeChild), either way by native session key.
	if c.oneShotReady(rt) {
		c.terminateRun(rt.runID, CauseOneShotBoundary, "")
		return
	}
	c.setState(rt, StateIdle)
	c.releaseSlot(rt)
	if c.pendingCount(role) > 0 {
		c.pushMail(role)
	}
}

// driveChild consumes the child's event stream, handling turn boundaries and
// idle wakes, until the stream closes. This is the LEGACY go-plugin Chat
// path only (a degraded no-reach-back spawn, or a StructuredChat backend
// outside the viaStartRun allowlist — spawner.go's viaStartRunBackends);
// D2 retired its agentbus TapHub tee along with the rest of the bus package.
// A legacy child is therefore not LIVE-observable via ConsumerService (D1's
// watchHub only covers RunChannel item events, which this path never emits)
// — an accepted, documented gap on an already-degraded path; its transcript
// still tails via the store fallback (operations.WatchSessionFeed) like any
// session.
func (c *Coordinator) driveChild(rt *childRt, launch *operations.AgentChatLaunch) {
	for {
		select {
		case ev, ok := <-launch.Events:
			if !ok {
				c.endChild(rt, launch)
				return
			}
			c.handleChildEvent(rt, ev)
		case <-rt.wake:
			c.wakeChild(rt)
		case <-c.baseCtx.Done():
			// Coordinator shutdown (flaky-agentcoord S1): this is the ONE
			// loop in the package with no baseCtx case — normally Close()'s
			// attachment-snapshot loop already closed the launch (rt.close),
			// which makes Events close and the branch above fire, but a
			// shutdown that races attachLaunch (closeFn still nil at that
			// snapshot) would otherwise leave this goroutine parked forever.
			// Force the launch down directly rather than routing through
			// endChild, which drains launch.Errs to completion — that only
			// resolves once Events/Errs close on their own, exactly what we
			// cannot wait for mid-shutdown.
			c.mu.Lock()
			in := rt.in
			rt.in = nil
			c.mu.Unlock()
			if in != nil {
				close(in)
			}
			launch.Close()
			c.terminateRun(rt.runID, CauseChatClose, "coordinator shutdown")
			return
		}
	}
}

func (c *Coordinator) handleChildEvent(rt *childRt, ev agent.ChatEvent) {
	switch {
	case ev.Entry != nil:
		// Turn accumulation for the legacy path's half of the result bridge
		// (blunt-whiff): a ONESHOT child has no reach-back at all, so every
		// entry it emits IS its output; a legacy CHAT child's answer is its
		// assistant entries only (thinking/tool chatter is the engine
		// transcript's job, §6b). Both feed bridgeTurnResult at the
		// boundary — the bridge no longer fires for oneshot alone.
		if ev.Entry.Content != "" && (rt.oneshot || ev.Entry.Type == agent.EntryTypeAssistant) {
			c.mu.Lock()
			rt.turnOutput = append(rt.turnOutput, ev.Entry.Content)
			if ev.Entry.IsError {
				rt.turnErrored = true
			}
			c.mu.Unlock()
		}
	case ev.Session != nil:
		// LEGACY path's half of PREREQ A (wooly-stove): the migrated path
		// already records this via StartRunResult/runchannel's
		// ctxloom/harness_session custom event (runChildViaStartRun above);
		// this is the only place a legacy go-plugin Chat dial's native
		// session id (antigravity's agy conversation id, any future
		// StructuredChat backend outside viaStartRunBackends) reaches the
		// journal at all — previously silently dropped by this switch
		// having no case for it. recordHarnessSession is idempotent, so the
		// repeat emission antigravity's Chat sends when its conversation id
		// first resolves (chat.go's post-first-turn Session event) is safe.
		if ev.Session.SessionID != "" {
			c.recordHarnessSession(rt.runID, ev.Session.SessionID)
			c.recordResumable(rt.runID, ev.Session.Resumable)
		}
	case ev.Complete != nil:
		c.onTurnBoundary(rt)
	}
}

// onTurnBoundary applies the §6a delivery rule "queued mid-turn → deliver at
// the next boundary": pending mail starts the next turn (the slot is kept);
// an empty mailbox parks the child idle and yields the slot.
func (c *Coordinator) onTurnBoundary(rt *childRt) {
	c.bridgeTurnResult(rt)
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
	// Count it BEFORE the terminal: terminateRun's leftover-mail tail reads
	// this count to decide whether another relaunch is warranted at all.
	c.noteLaunchFailure(rt.harp)
	c.terminateRun(rt.runID, CauseLaunchFailed, err.Error())
	c.markAttached(rt) // the attempt settled (failed): unblock any awaitChildUp
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
		return
	}
	c.sampleExecGauge()
}

// sampleExecGauge reports the current fold-authoritative count of runs in
// StateExecuting to the test-only execGaugeHook (coordinator.go) — a no-op
// when unset (production path, zero cost beyond the nil check).
func (c *Coordinator) sampleExecGauge() {
	if c.execGaugeHook == nil {
		return
	}
	var n int
	c.runs.View(func() { n = c.queueF.executing })
	c.execGaugeHook(n)
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

// claimSlotIntent atomically claims rt's "this attempt owns acquiring a
// slot" bit — the R1 fix (one-shot-resume plan Slice 3): the check
// (!rt.slotHeld) and the mutation (rt.slotHeld = true) happen inside the
// SAME c.mu window, so two racing callers for the SAME rt (a concurrent
// onTurnStarted/onRoleUnpark pair, or either fired twice) can never both
// decide "I need to acquire" — exactly one wins, matching releaseSlot's own
// pattern above. Returns false when rt already holds (or already owns
// claiming) a slot: the caller then does nothing further — the occupancy
// is already correctly accounted for. Deliberately NOT combined with the
// actual turnSlots acquisition (which can block for onRoleUnpark's caller,
// runtimeSlots.acquire) — holding c.mu across a blocking acquire would
// stall every OTHER coordinator operation needing c.mu for as long as the
// slot wait takes. A winner whose subsequent acquisition fails/is
// cancelled MUST call releaseSlotIntent to undo the claim.
func (c *Coordinator) claimSlotIntent(rt *childRt) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt.slotHeld {
		return false
	}
	rt.slotHeld = true
	return true
}

// releaseSlotIntent undoes a claimSlotIntent win whose actual turnSlots
// acquisition did not pan out (tryAcquire found no free slot, or a
// blocking acquire's ctx was cancelled) — never leave slotHeld reading true
// when no turnSlots slot is actually held (assertion (f), slot
// conservation, exists specifically to catch a regression here).
func (c *Coordinator) releaseSlotIntent(rt *childRt) {
	c.mu.Lock()
	rt.slotHeld = false
	c.mu.Unlock()
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
	c.sampleExecGauge() // the terminal is also a (possible) StateExecuting exit — see setState's sibling call
	c.audit("run_terminal", rec.Harp, map[string]string{"run_id": runID, "cause": cause})
	// The ACCEPT_FOR_SESSION cache (C2) is HARP-scoped (fix/accept-session-
	// scope, one-shot-resume plan Slice 1): it must outlive an ordinary run
	// terminal — a resumed harp keeps its grants, because "for-session"
	// means the harp's whole delegated session, not the turn that happened
	// to be live when it was granted. Only an explicit, deliberate
	// agent_stop (CauseStopped) clears it here; every other cause leaves the
	// harp resumable (factRunEnded's own doc) and must not wipe grants —
	// see clearSessionAccepts's doc for why this distinction matters under
	// one-shot.
	if cause == CauseStopped {
		c.clearSessionAccepts(rec.Harp)
	}
	// Plane-2 request idempotency records are role-scoped and reconnect-
	// surviving (runchannel.go); drop this harp's at terminal so they don't
	// accumulate across the process's lifetime.
	c.clearReqTrack(rec.Harp)

	// D4 (damp-pupil 1): drain BEFORE anything below that can tear the
	// RunChannel's underlying connection down — closeFn (engine.Kill) closes
	// the runner's WHOLE gRPC ClientConn, which multiplexes RunChannel too,
	// so calling it first can win the very race this drain exists to close.
	// An explicit RunExited (CauseRunnerExit) is the ONLY cause whose
	// production emitter is contractually guaranteed to have just attempted
	// a run_completed item on that channel — see drainTerminalTail's doc
	// for why CauseStopped/CauseRunnerLoss must not pay this wait.
	if cause == CauseRunnerExit {
		c.drainTerminalTail(rec.Harp)
	}

	c.mu.Lock()
	rt := c.attach[runID]
	delete(c.attach, runID)
	var closeFn func()
	var launchCancel context.CancelFunc
	if rt != nil {
		closeFn = rt.close
		rt.close = nil
		launchCancel = rt.launchCancel
		rt.launchCancel = nil
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
	// The run is over, so the context it was launched under is too — this is
	// the ONE place it is cancelled (see childRt.launchCancel).
	if launchCancel != nil {
		launchCancel()
	}
	// Revocation severs the credential's parked long-poll AND its live run
	// channel (the channel teardown un-reserves tentative deliveries so the
	// leftover-mail check below sees them).
	c.severPoll(rec.Harp, ErrRevoked)
	c.severChan(rec.Harp)

	// The synthesized terminal notice: the parent ALWAYS learns of a child
	// death (blue-paper). Kind distinguishes a launch failure (error) from
	// a lifecycle end (exited). A one-shot turn boundary is the exception —
	// it is a NON-death, EXPECTED terminal that fires every single turn, so
	// notifying the parent would spam its mailbox with an "exited" per turn;
	// the turn's actual result was already bridged (bridgeTurnResult, before
	// this terminate), and the harp is about to resume, so no notice is due.
	if rec.ParentHarp != "" && cause != CauseOneShotBoundary {
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
	// must not strand: the ended-child delivery rule is resume (§6a). That
	// tail is ALSO the launch-retry loop — see relaunchForLeftoverMail.
	c.relaunchForLeftoverMail(rec, cause, detail)

	// Retention (Slice 4 / Fork 2.3): bound the ended-run records the live
	// folds keep. Runs after MarkSessionEnded (every terminal, one-shot or
	// not) — the pending-mail resume above is async, so the just-ended run is
	// still this harp's CURRENT run here and is never in the reap set.
	c.reapEndedRuns()
}

// reapEndedRuns bounds the live folds' ended-run records (one-shot-resume
// plan, Slice 4 / Fork 2.3). One-shot mints one ended run per turn per harp;
// without a bound runsFold.runs / queueFold.state / rosterFold.byRun grow
// unbounded over a long session. It keeps every harp's CURRENT run (the
// resume key lives there, so it is NEVER reaped) plus the newest
// endedRunTail ended runs across all harps, dropping any ended, non-current
// run beyond the tail OR older than endedRunMaxAge. The reap is a durable
// fact (factRunReaped) so a replay/reconciliation reaches the SAME bounded
// projection; on-disk journal truncation stays deferred (Wave E).
//
// The candidate set is chosen in a View but the final decision is RE-CHECKED
// inside the journal's single-writer window: a run that became current
// between the View and the write (a concurrent resume) is dropped from the
// reap set there, so a live resume key can never be reaped out from under a
// harp.
func (c *Coordinator) reapEndedRuns() {
	type cand struct {
		id string
		at time.Time
	}
	var ended []cand
	cutoff := c.now().Add(-c.endedRunMaxAge)
	c.runs.View(func() {
		for id, r := range c.runsF.runs {
			if !r.Ended || c.runsF.byHarp[r.Harp] == id {
				continue // live, or the harp's current (resume-key) run
			}
			ended = append(ended, cand{id: id, at: r.LastActivity})
		}
	})
	if len(ended) == 0 {
		return
	}
	// Newest first: keep the tail's worth of most-recent ended runs.
	sort.Slice(ended, func(i, j int) bool { return ended[i].at.After(ended[j].at) })
	var reap []string
	for i, e := range ended {
		if i >= c.endedRunTail || e.at.Before(cutoff) {
			reap = append(reap, e.id)
		}
	}
	if len(reap) == 0 {
		return
	}
	sort.Strings(reap) // deterministic fact payload
	if err := c.runs.Exec(func() ([]Fact, error) {
		safe := reap[:0:0]
		for _, id := range reap {
			// Re-assert ended + non-current under the write lock — a resume
			// may have made this id current since the View.
			if r := c.runsF.run(id); r != nil && r.Ended && c.runsF.byHarp[r.Harp] != id {
				safe = append(safe, id)
			}
		}
		if len(safe) == 0 {
			return nil, nil
		}
		return []Fact{factAt(factRunReaped, c.now(), runReaped{RunIDs: safe})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: reap ended runs: %v", err)
	}
}

// resumeChild relaunches an ended harp as a FRESH run attempt (new run id,
// new credential) so queued mail can be delivered as its next turn. The
// session-load machinery primes the fresh engine with the recorded history
// when the transcript is bound. attached is THIS call's own "settled"
// signal, minted by its dispatcher's armLaunch — resumeChild owns closing it
// end-to-end: every early-return path below (before enqueueRun hands
// ownership to the new childRt) closes it directly via the defer/settled
// guard, so awaitChildUp never hangs on an attempt that silently never
// reached enqueueRun (S3, flaky-agentcoord).
//
// delay is the bounded-retry backoff this attempt must wait out before doing
// anything (zero for an operator-driven resume; exponential for an automatic
// relaunch after a launch failure — see launchgate.go). The wait, and every
// step after it, runs under the harp's CANCELLABLE launch context, and the
// stop flag is re-checked at each point the attempt can still turn back: an
// attempt armed BEFORE an agent_stop must not carry on behind it, which is
// exactly how the 2026-07-24 loop outlived every stop issued against it.
func (c *Coordinator) resumeChild(harp string, attached chan struct{}, delay time.Duration) {
	settled := false
	defer func() {
		// Only close here if enqueueRun never ran (found=false, Resolve
		// failed, or a concurrent resume already won the claim —
		// errResumeLost). Once enqueueRun succeeds, `attached` becomes
		// rt.attached and every downstream path (failChild, the legacy/
		// StartRun attach-success points) closes it exactly once via
		// markAttached — closing it again here would panic.
		if !settled {
			close(attached)
		}
	}()
	// This attempt's cancellable launch context — agent_stop cancels it.
	// Ownership passes to the run once enqueueRun wins (settled); until then
	// this attempt owns it and must not leave it dangling.
	lctx, lcancel, deregister := c.launchContext(harp)
	defer deregister()
	defer func() {
		if !settled {
			lcancel()
		}
	}()
	// Back off before retrying (bounded-retry, defect 3): a launch that has
	// just failed does not become launchable microseconds later, and the
	// backoff-free version of this loop ran ~2 container launches/second for
	// 49 minutes.
	if !sleepLaunchBackoff(lctx, delay) {
		return
	}
	if c.launchStopped(harp) {
		return // an agent_stop landed while this attempt was armed/backing off
	}
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
	plan, err := c.spawner.Resolve(lctx, rec.Agent)
	if err != nil {
		clidiag.Warn("ctxloom", "agent resume %s: %v", harp, err)
		if _, _, qerr := c.queueMail(harp, rec.ParentHarp, "error", fmt.Sprintf("agent %q (session %s) could not be resumed: %v", rec.Agent, harp, err)); qerr != nil {
			clidiag.Warn("ctxloom", "agent %s: queue resume failure: %v", harp, qerr)
		}
		return
	}
	// GAP 2 deferral: the ORIGINAL agent_run's workspace override is not
	// journaled on runEnqueued, so a resumed harp always falls back to
	// PrepareAgentChat's normal resolution (per-call empty → project
	// cfg.Workspace if explicit → else the delegated-child worktree
	// default) rather than reusing its prior workspace choice. A resume can
	// therefore land in a DIFFERENT (fresh) worktree than the original run
	// used, or flip from none to worktree or vice versa, depending on what
	// changed. Persisting it is a durable-fact/fold change outside this
	// fix's scope (agent_run/spawner/delegate/mcp-input surface only);
	// tracked as deferred work.
	// D5: the resumed run's own parent_run_id must reflect the PARENT'S
	// CURRENT live run (not the stale run_id recorded at the ORIGINAL
	// enqueue) — the parent may itself have been resumed since. Empty when
	// the parent has no live run of its own (a depth-0 session owner, or
	// the parent has also ended).
	parentRunID := ""
	if rec.ParentHarp != "" {
		c.runs.View(func() {
			if p := c.runsF.currentRun(rec.ParentHarp); p != nil && !p.Ended {
				parentRunID = p.RunID
			}
		})
	}
	caller := Identity{Harp: rec.ParentHarp, RunID: parentRunID, Depth: rec.Depth - 1, Project: c.projectDir}
	// Last check before this attempt becomes a REAL run: a stop that landed
	// while Resolve was in flight (config read, agent resolution — slow
	// enough to matter in production) must not be overtaken here.
	if c.launchStopped(harp) {
		return
	}
	rt, token, err := c.enqueueRun(caller, plan, harp, "", true, attached)
	if errors.Is(err, errResumeLost) {
		return // a concurrent resume claimed it; the winner delivers
	}
	if err != nil {
		clidiag.Warn("ctxloom", "agent resume %s: %v", harp, err)
		return
	}
	settled = true // rt now owns `attached`'s AND the launch context's lifecycle
	c.mu.Lock()
	rt.launchCancel = lcancel
	c.mu.Unlock()
	c.audit("agent_resume", rec.ParentHarp, map[string]string{"harp": harp, "run_id": rt.runID})

	if !rt.slotHeld {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			c.failChild(rt, err)
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

	// resumeKeyFor is NOT used here: it reads the harp's CURRENT run, but
	// enqueueRun (above) already minted the fresh one for this very resume,
	// whose HarnessSessionID is necessarily still empty (not yet reported).
	// The key this resume must thread is the JUST-ENDED run's — exactly what
	// `rec` (captured before enqueueRun) already holds.
	resumeSessionID := rec.HarnessSessionID
	haveResumeKey := resumeSessionID != ""

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
		if !haveResumeKey {
			contextText = c.spawner.ResumeContext(lctx, plan, harp)
		}
		c.runChildViaStartRun(lctx, rt, "", token, url, resumeSessionID, contextText)
		return
	}

	// LEGACY resume (Slice 0, wooly-stove): mirror the ViaStartRun branch
	// above — a captured native session id (antigravity's agy conversation
	// id, journaled via handleChildEvent's ev.Session case) resumes the
	// backend's OWN session (agy --conversation <id> --continue) with no
	// rendered-transcript re-priming needed; only a prior run that never
	// reported a session id falls back to the lossy ResumeContext replay.
	contextText := ""
	if !haveResumeKey {
		contextText = c.spawner.ResumeContext(lctx, plan, harp)
	}
	launch, err := c.spawner.Launch(lctx, plan, contextText, resumeSessionID,
		c.childEnv(harp), runnerEnv(harp, rt.runID, token, url, plan.Coordinator))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.noteLaunchAttached(harp)
	c.attachLaunch(rt, launch)
	if msg, ok := c.takeNextMail(harp); ok {
		c.sendTurn(rt, msg.Body)
	}
	c.markAttached(rt) // the engine is up; driveChild below drives its whole lifetime, not just the relaunch
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
		// An EXPLICIT delivery (agent_send / inject) is a fresh ask from a
		// parent or an operator, so it lifts a prior agent_stop and resets
		// the consecutive-failure budget — the documented way a stopped or
		// given-up child comes back. Only the AUTOMATIC relaunch
		// (terminateRun's leftover-mail tail) is bounded; if this reset
		// leaked into that path the bound would not be a bound.
		c.clearLaunchGate(harp)
		attached := c.armLaunch(harp)
		c.goTracked(func() { c.resumeChild(harp, attached, 0) })
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
// claimSlotIntent (R1, one-shot-resume plan Slice 3) makes "do I still need
// to acquire" atomic with claiming ownership of doing so: a duplicate/
// racing unpark signal for the SAME rt (or a race against onTurnStarted)
// finds slotHeld already true, skips the blocking acquire entirely, and
// just reasserts StateExecuting (idempotent) — never a second acquisition
// against one slotHeld bit.
func (c *Coordinator) onRoleUnpark(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil || c.runState(rt.runID) != StateParked {
		return
	}
	if c.claimSlotIntent(rt) {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			c.releaseSlotIntent(rt)
			return
		}
	}
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
