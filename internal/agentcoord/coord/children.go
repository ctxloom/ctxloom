package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

const (
	// agentDepthCap is the BUILT-IN DEFAULT for the delegation tree's DEPTH
	// cap — the single policy knob both the "may this run spawn" guard
	// (AgentRun, below) and the runner-side leaf computation
	// (internal/cli/attachRunnerMCP, via config.Config.GetDelegationDepth)
	// derive from: a run may spawn iff its depth < the resolved cap, and it
	// is a LEAF (receives none of the coordinator-only MCP tools) iff its
	// depth >= the resolved cap. The session owner is depth 0; a spawned
	// run's depth is always its spawner's depth + 1. THIS IS A CORRECTNESS
	// SETTING, not a resource dial (contrast agentConcurrencyCap below):
	// raising it gives agent_run/roster to non-root agents, and a non-root
	// agent holding an inbox plus a child roster can infer it has children
	// and stall waiting for notifications that never arrive. Production
	// sources the live value from config.Config.GetDelegationDepth
	// (Options.Depth); <= 0 (unset) falls back to this constant. Defined in
	// terms of config.DefaultDelegationDepth, never a separate literal: a
	// spawned runner resolves the SAME cap independently, from its own
	// loaded config, with no coordinator round-trip (attachRunnerMCP,
	// internal/cli/llm_runner_common.go) — GetDelegationDepth already
	// applies this identical default, so the two can never drift apart.
	// Currently 1 — flat fan-out: the owner (depth 0) may spawn subagents
	// (depth 1), and a depth-1 subagent may not itself spawn (no
	// grandchildren). Raising config.DefaultDelegationDepth to 2 would
	// re-enable one further level with no other code change — the property
	// this design is meant to have, even though the value stays 1 today.
	agentDepthCap = config.DefaultDelegationDepth
	// agentConcurrencyCap is the BUILT-IN DEFAULT for turnSlots' cap: the
	// maximum number of delegated child turns EXECUTING at once (each a live
	// engine process). NOT a correctness gate: the coordinator's own state is
	// partitioned by child identity and safe under real concurrency by
	// construction. Production sources the live value from
	// config.Config.GetDelegationConcurrency (Options.Concurrency); <= 0
	// (unset) falls back to this constant. Renamed from agentTurnCap: "turn
	// cap" read as a per-run quota, which it never was. The cap counts
	// EXECUTING turns only — a child parked in agent_recv or idle at a turn
	// boundary yields its slot (turnSlots is a resource limiter: the slot is
	// acquired before spawner.Launch/StartEngine, never a serialization
	// primitive).
	agentConcurrencyCap = 4

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

// slotState is a childRt's three-valued relationship to the D4 execution-
// slot cap (turnSlots), replacing a single overloaded bool. The
// old `slotHeld` bool did double duty as both "I am ATTEMPTING to acquire a
// slot" (set true by claimSlotIntent BEFORE a potentially long BLOCKING
// acquire — onRoleUnpark's) and "I actually HOLD a slot" (the only fact
// releaseSlot/onRolePark may safely act on). Those are different facts with
// an unbounded gap between them, and a concurrent releaseSlot/onRolePark
// landing inside that gap could not tell which one it was looking at:
//   - reading the bit true while only "claiming" (not yet acquired) and
//     releasing anyway calls turnSlots.release() for a slot nobody holds,
//     INFLATING the cap; and
//   - clearing the bit under a claim that goes on to acquire for real
//     leaves that later-arriving slot with no bit ever set again, LEAKING
//     it forever (nothing will ever see "held" for it again).
//
// claimSlotIntent/commitSlotClaim/releaseSlotIntent/releaseSlot together
// keep the three states straight; see each one's doc.
type slotState uint8

const (
	slotFree    slotState = iota // holds nothing, wants nothing
	slotClaimed                  // won the right to attempt acquisition; a tryAcquire
	// or blocking acquire may be in flight right now — NOT yet a real slot
	slotHeld // a real turnSlots slot is actually held — the only state a
	// release may act on
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
	// parentRunID is the spawning run's run_id: empty when the caller has no
	// run of its own to be a parent of, set otherwise. A child spawned
	// directly by the plugin-hosted top-level session sees this empty (that
	// session's own credential carries no run id); a child spawned by a
	// container top-level session's owned run, or by any already-delegated
	// child, sees this set — both carry a run id of their own from the
	// moment they start. Threaded onto the outgoing StartRun request
	// (runChildViaStartRun) so RunStarted.parent_run_id carries durable
	// lineage on the log — mirrors RunRecord.ParentRunID.
	parentRunID string
	// depth is this run's own position in the delegation tree — the value
	// stamped into its runner's env (EnvRunDepth) and journaled onto
	// runEnqueued.Depth: 0 for the session owner's own run (StartOwnedRun,
	// which reuses the owner's identity rather than spawning a child), and
	// (spawning run's depth + 1) for every genuinely delegated child
	// (AgentRun/resumeChild). Set once at enqueueRun, never mutated.
	depth int
	plan  *SpawnPlan

	// slot is this childRt's relationship to the D4 execution-slot cap
	// (turnSlots) — see slotState's doc for why this is a
	// tri-state, not a bool. slotCancel is set by releaseSlot/onRolePark
	// when they find slot == slotClaimed (an acquisition is in flight and
	// must not be released yet); commitSlotClaim consults it once that
	// acquisition actually lands. Both guarded by Coordinator.mu.
	slot       slotState
	slotCancel bool
	oneshot    bool
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
	// workDir is the isolation-resolved workspace this run's engine was
	// started in (EngineSpawn.WorkDir). It exists for the liveness watchdog:
	// the worktree's newest mtime is the only activity clock that is not
	// written by the engine's own bookkeeping, so it is what distinguishes an
	// agent inside a ten-minute build from one that is hung.
	workDir     string
	wake        chan struct{}
	turnOutput  []string // result bridging: this turn's assistant output (see bridgeTurnResult)
	turnErrored bool     // result bridging: this turn's Entry.IsError was set (Result.status)
	// runFailure is the last FAILED RunCompleted's Result.Text for this run —
	// the engine's own reason for dying, which since the stderr-tail capture
	// (internal/acp) carries the adapter's dying words (a module-loader
	// SyntaxError, a JSON-RPC -32603 "Invalid API key"). A migrated child
	// that dies below the protocol emits NO final-channel output, so
	// bridgeTurnResult has nothing to deliver and the parent would otherwise
	// learn only "exited (runner-exit)" with no cause — the exact silent
	// dead-end the 49-minute incident was. terminateRun folds this into the
	// parent's terminal notice so a dead engine can say WHY. Captured on the
	// RunChannel receive path (handleAgentEvent), read once at terminal.
	runFailure string
	// stderrTail reads the runner's bounded stderr tail (the container's
	// streamed stderr, engine adapter's dying words teed in). It is the
	// FALLBACK reason when the runner dies WITHOUT emitting a FAILED
	// RunCompleted — a docker-stop / OOM-kill surfaces as runner loss, where
	// there is no engine reason to capture but the container's stderr still
	// holds why. terminateRun reads it only when runFailure is empty. Nil for
	// a policy/spawner that captures nothing (tests, host paths without a
	// ring). Set at spawn (runChildViaStartRun).
	stderrTail func() string
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
	// attempt failed (failChild). It backs awaitChildUp/armLaunch —
	// a test-facing deterministic quiesce seam over the
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
// ORCHESTRATION trait the CALLER supplies per invocation: it is set on the
// resolved plan here, never inside Resolve (agent-definition resolution
// stays pure — see spawner.go's Resolve/GAP 1).
//
// dirtyTreeHandler is the identical per-call override for what a worktree
// spawn does when the parent tree is dirty (commit|copy|stale|fail; empty =
// project default, cfg.GetDirtyTreeHandler()) — see
// operations.handleDirtyParentTree. Deliberately does NOT carry any
// acknowledgement for the "commit" handler's mutation: that is a
// per-checkout, human-only acknowledgement (dirty_tree_commit_ack — see
// config.DirtyTreeCommitAcknowledged) that this per-call parameter can never
// set: it is not even a config key any longer, precisely so no channel an
// agent can reach (config, env, argv) can grant it.
func (c *Coordinator) AgentRun(ctx context.Context, caller Identity, agentName, prompt, workspace, dirtyTreeHandler string) (*RunOutcome, error) {
	if agentName == "" {
		return nil, errors.New("agent_run: agent is required (a configured agent name; see `ctxloom agent list`)")
	}
	if prompt == "" {
		return nil, errors.New("agent_run: prompt is required (the child's briefing/first turn)")
	}
	// A OneShot caller may not spawn AT ALL, regardless of depth: its own
	// engine tears down and is resumed by native session key at every turn
	// boundary (Identity.OneShot's doc), so it cannot hold a coordination
	// relationship across turns — a child it spawned could report back to a
	// mailbox its parent's ended run will never drain again. Checked before
	// the depth guard since it is a total refusal, not a depth-conditional
	// one — a depth-0 OneShot caller is refused exactly like a depth-1 one.
	if caller.OneShot {
		return nil, errors.New("agent_run: refused: this session is a one-shot (driving: oneshot) run, which cannot hold a coordination relationship with a child across its own turn boundaries — report the work back to your coordinator (agent_send to \"parent\") instead")
	}
	// Depth derives from the CREDENTIAL, never from env: caller.Depth is
	// resolved server-side from the authenticated Identity (Identify), so a
	// child cannot spoof its own depth by forging an env var. A run may
	// spawn iff its depth is BELOW the resolved cap (config.Config.
	// GetDelegationDepth; <= 0 falls back to agentDepthCap) — the identical
	// comparison the runner-side leaf computation makes (>=) on the SAME
	// stamped depth, so raising the one config key re-enables deeper trees
	// on both sides at once, never just one.
	depthCap := c.depthCap
	if caller.Depth >= depthCap {
		return nil, fmt.Errorf("agent_run: refused: this session (depth %d) is already at the maximum delegation depth (delegation.depth = %d) — report the work back to your coordinator (agent_send to \"parent\") and let it fan out, or raise delegation.depth in config.yaml if a deeper tree is actually wanted", caller.Depth, depthCap)
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

	rt, token, err := c.enqueueRun(caller, plan, harp, prompt, false, make(chan struct{}), caller.Depth+1)
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
	queued := rt.slot != slotHeld
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
// the childRt's "settled" signal: AgentRun's own
// synchronous call passes a fresh channel; resumeChild passes the ONE
// channel its own dispatcher (armLaunch) created for THIS specific attempt
// — never looked up by harp here, so a concurrent second dispatch for the
// same harp (driveQueued's StateEnded case racing terminateRun's
// leftover-mail tail — both legitimate, "the winner delivers") can never
// cross-close another attempt's channel.
//
// depth is the EXPLICIT depth of the run being enqueued (not derived from
// caller.Depth here — the caller decides): AgentRun passes caller.Depth+1 (a
// genuine spawned child is one generation below its spawning run);
// resumeChild passes the ended run's own recorded depth (a resume keeps the
// SAME run identity, not a new generation); StartOwnedRun passes the owner's
// own depth unchanged (the owned run reuses the owner's identity — it IS the
// owner on a different transport, not a child of it). This is the single
// place runEnqueued.Depth and childRt.depth are set from, so the runner-side
// env stamp (EnvRunDepth, via runnerEnv) and the server-side recursion guard
// (AgentRun's caller.Depth, read back from this same fact via Identify) never
// diverge.
func (c *Coordinator) enqueueRun(caller Identity, plan *SpawnPlan, harp, prompt string, resume bool, attached chan struct{}, depth int) (*childRt, string, error) {
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
			Depth:       depth,
			// OneShot is read straight from the run's OWN resolved plan —
			// never threaded as a separate parameter like depth (which
			// genuinely needs per-caller asymmetry): every enqueueRun caller
			// already passes the run's own plan, and ResumeMode is exactly
			// this run's own resolved mode in every case (a genuine child's
			// own agent resolution, a resumed run's freshly re-resolved
			// plan, or an owned run's synthetic plan, which never sets it —
			// zero value ResumeModePersistent, correctly never OneShot).
			OneShot: plan.ResumeMode == ResumeModeOneShot,
			Prompt:  prompt,
			Resume:      resume,
			Ladder:      ladderToFact(plan.Ladder),
			Permission:  plan.Perm.String(),
			// Names only: an operator auditing a live delegation sees WHAT a
			// child can reach, and command/args/env never enter the journal.
			MCPServers: operations.MCPServerNames(plan.MCPServers),
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
		depth:       depth,
		plan:        plan,
		wake:        make(chan struct{}, 1),
		attached:    attached,
	}
	// Claim a free slot now when one exists so `queued` is truthful at
	// return. Set BEFORE publication — after it, rt.slot belongs to
	// the mutex (the park hooks may touch it).
	if c.slots.tryAcquire() {
		rt.slot = slotHeld
	}
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
// !found). A test replaces the FIRST
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
// back to its local mode. depth is this run's OWN delegation depth (childRt.
// depth: 0 for the session owner's own run, spawner's depth + 1 for a
// genuine child) and is stamped UNCONDITIONALLY via EnvRunDepth — unlike the
// trio, leafness must not depend on reach-back being present. Replaces the
// retired per-agent Coordinator flag/EnvAgentCoordinator: the runner
// (internal/cli/standUpRunner/attachRunnerMCP) compares this depth against
// the resolved delegation-depth cap to decide leaf-vs-not and gate the
// coordinator-only MCP tools (mcp_runner.go). oneshot is this run's own
// SpawnPlan.ResumeMode == ResumeModeOneShot, stamped via EnvRunOneShot on
// the SAME unconditional terms as depth: a one-shot run is a leaf
// regardless of depth (Identity.OneShot's doc).
func runnerEnv(harp, runID, token, url string, depth int, oneshot bool) map[string]string {
	env := map[string]string{
		"CTXLOOM_SESSION_HARP": harp,
		EnvRunDepth:            strconv.Itoa(depth),
		EnvRunOneShot:          strconv.FormatBool(oneshot),
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
// reach-back is exactly a stranding bug, so an unresolvable
// endpoint is a fatal finding (fail-loud) — degraded mode downgrades it and
// the child launches with no coordinator env (a local, message-less
// orchestrator, today's broken-but-running posture).
func (c *Coordinator) spawnReachURL(harp, runtimeAxis string) (string, error) {
	url, err := c.ReachURL(runtimeAxis)
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
	held := rt.slot == slotHeld
	c.mu.Unlock()
	if !held {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			c.failChild(rt, err)
			return
		}
		c.mu.Lock()
		rt.slot = slotHeld
		c.mu.Unlock()
	}
	c.setState(rt, StateExecuting)

	// MIGRATED (C1 landed claude; C3 extended the allowlist to codex/kiro/
	// acp — see spawner.go's viaStartRunBackends): engine control rides
	// StartRun on the runner's RunnerChannel. A degraded spawn without
	// reach-back (url == "") cannot — the runner could never dial home — so
	// it keeps the legacy dial. This is now the go-plugin Chat dial's ONE
	// intentional, documented reachable case for an allowlisted backend
	// (preserved as-is, matching what C1 already landed);
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
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url, rt.depth, rt.plan.ResumeMode == ResumeModeOneShot))
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
// awaitRunner is a plain blocking receive on a channel the runner's Hello
// closes (grpcserver.go), not a poll loop, so widening this costs a HEALTHY
// launch nothing: it still returns the instant the runner dials home.
// Backoff spacing between separate launch ATTEMPTS (launchgate.go) is a
// different budget, answering a different question ("how long between
// attempts" vs "how long do we tolerate one attempt"), and is deliberately
// left untouched — conflating the two was the original miscalibration.
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
		c.childEnv(rt.harp), runnerEnv(rt.harp, rt.runID, token, url, rt.depth, rt.plan.ResumeMode == ResumeModeOneShot))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.mu.Lock()
	rt.close = engine.Kill
	rt.stderrTail = engine.StderrTail
	rt.workDir = engine.WorkDir
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
// startRunPayloadErr refuses a StartRun that would carry no work at all.
// issueStartRun builds Input only `if first != ""`, so an empty
// lead used to go out as a StartRun with a NIL Input: it round-trips, the run
// attaches, the roster says executing, and the engine sits there having been
// told nothing. Zero payload, every signal green — ctxloom's characteristic
// silent no-op, and the same shape as the known `runtime:container`
// prompt-delivery defect.
//
// An empty lead is NOT always a defect, so this discriminates rather than
// blanket-refusing — the three legitimate sources of work a lead-less run can
// still have:
//
//   - a resume key: the engine continues its OWN recorded session (ACP
//     session/load), which needs no re-priming (see resumeChild);
//   - queued mail: issueStartRun's own standup drain pushes it as the first
//     turn moments later;
//   - an owner run: a STRUCTURED top-level session legitimately opens with no
//     lead and takes its turns via SendOwnedRunTurn. Its one-shot sibling,
//     which genuinely has only this one turn, is adjudicated by StartOwnedRun
//     where the Oneshot flag lives.
//
// Anything else has nothing to do and no way to be given anything to do.
func (c *Coordinator) startRunPayloadErr(rt *childRt, first, resumeSessionID string) error {
	if first != "" || resumeSessionID != "" || rt.ownerRun {
		return nil
	}
	if c.pendingCount(rt.harp) > 0 {
		return nil
	}
	return fmt.Errorf("StartRun for %q (%s) would carry no first turn: no composed prompt, no resume session id, and no queued mail — "+
		"the run would attach and sit idle having been told nothing (context composition or prompt delivery failed upstream)",
		rt.agentName, rt.harp)
}

func (c *Coordinator) issueStartRun(ctx context.Context, rt *childRt, credHash string, spec *agentcoordpb.HarnessSpec, first, model, resumeSessionID string) error {
	actx, acancel := context.WithTimeout(ctx, c.runnerAwaitTimeout)
	_, err := c.awaitRunner(actx, credHash)
	acancel()
	if err != nil {
		err = fmt.Errorf("runner never dialed home (StartRun path): %w", err)
		c.failChild(rt, err)
		return err
	}
	if err := c.startRunPayloadErr(rt, first, resumeSessionID); err != nil {
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
// spawn time (runChild's blocking acquire). claimSlotIntent (one-shot-
// resume plan Slice 3) makes the check-then-acquire atomic: a racing
// onRoleUnpark/onTurnStarted pair for the SAME rt can no longer both
// tryAcquire and both land a slot — exactly one wins the claim, a failed
// tryAcquire rolls the claim back via releaseSlotIntent, and a successful
// one is committed via commitSlotClaim rather than left at
// slotClaimed forever.
func (c *Coordinator) onTurnStarted(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	c.mu.Unlock()
	if rt == nil || c.runEnded(rt.runID) {
		// A frame that was already in flight when the channel was severed is
		// still dispatched: the RunChannel's receive goroutine outlives
		// RunChannel's return, and severChan/terminateRun do not synchronise
		// with it. c.byHarp keeps the ended run's childRt, so this
		// would ACQUIRE a slot for a run whose terminal has already released
		// everything it held — and nothing would ever give that slot back,
		// shrinking the execution cap for the rest of the process's life. Its
		// siblings are already guarded this way (onRoleUnpark on the fold state,
		// onRolePark/releaseSlot on rt.slot); this arm and onTurnIdle's bridge
		// were the two that were not.
		return
	}
	if c.claimSlotIntent(rt) {
		if !c.slots.tryAcquire() {
			c.releaseSlotIntent(rt)
		} else if !c.commitSlotClaim(rt) {
			// Cancelled between the claim and this (non-blocking)
			// tryAcquire landing — the same reason onRoleUnpark must
			// handle it below, just a far smaller window here.
			c.slots.release()
		}
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

// captureRunFailure records a FAILED RunCompleted's reason on the child's
// runtime so terminateRun can fold it into the parent's terminal notice. The
// reason is the engine's OWN account of its death — and since internal/acp's
// stderr-tail capture it carries the adapter's dying words (a module-loader
// SyntaxError, a JSON-RPC -32603 "Invalid API key") for a death that happens
// below the protocol, exactly the case that emits no final-channel output for
// bridgeTurnResult to deliver. Any terminal that is neither SUCCEEDED nor
// CANCELLED is captured when its text is non-empty: those two are not
// failures to explain, and an empty text carries nothing (this project's
// silent no-op — never surfaced as a reason).
func (c *Coordinator) captureRunFailure(role string, ev *agentcoordpb.AgentEvent) {
	rc, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_RunCompleted)
	if !ok {
		return
	}
	res := rc.RunCompleted.GetResult()
	// SUCCESS IS AN ALLOW-LIST. This used to test
	// `!= RUN_STATUS_FAILED`, so a run that ended on the enum's ZERO value —
	// what an engine that never set a status produces — or on TIMED_OUT /
	// BUDGET_EXCEEDED had its dying words silently dropped and the parent got
	// no reason at all. CANCELLED stays excluded deliberately: a deliberate
	// stop is not a failure to explain.
	switch res.GetStatus() {
	case agentcoordpb.Result_RUN_STATUS_SUCCEEDED, agentcoordpb.Result_RUN_STATUS_CANCELLED:
		return
	}
	text := strings.TrimSpace(res.GetText())
	if text == "" {
		return
	}
	c.mu.Lock()
	if rt := c.byHarp[role]; rt != nil {
		rt.runFailure = text
	}
	c.mu.Unlock()
}

// bridgeTurnResult is the AUTOMATIC child→parent report: at a
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
		// That warning goes to the COORDINATOR PROCESS's stderr —
		// a channel the parent (an agent whose sole input is its mailbox)
		// cannot read. Under the runtime:container prompt-delivery defect
		// this fires every turn while every cheap signal (roster state,
		// transcript existence, exit code) stays green, and the parent
		// observes an indefinitely silent, "executing" child with no
		// diagnostic at all. Best-effort: if the mailbox itself is what's
		// broken, this mail also fails, but the case it exists for (a
		// perfectly healthy mailbox, an unhealthy CHILD) is the common one.
		if _, _, err := c.queueMail(rt.harp, rt.parentHarp, "error",
			fmt.Sprintf("agent %q (run %s) turn produced no output — nothing to report", rt.harp, rt.runID)); err != nil {
			clidiag.Warn("ctxloom", "agent %s: notify parent of empty turn: %v", rt.harp, err)
		}
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
		// The accumulator was already cleared above (so a
		// concurrent append during the failed queueMail call lands cleanly
		// on top, not lost under a lock we no longer hold) — but a failed
		// delivery must not silently vanish the turn's own text. Restore it
		// AHEAD of anything accumulated since, so the next turn boundary
		// retries delivering it instead of the report existing nowhere:
		// not in rt, not in the mailbox fold, not in the parent's view.
		c.mu.Lock()
		rt.turnOutput = append(append([]string{}, out...), rt.turnOutput...)
		c.mu.Unlock()
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
	if rt == nil || c.runEnded(rt.runID) {
		// A turn boundary that lands after the run's terminal (see
		// onTurnStarted) must not bridge: the child already delivered its
		// terminal notice, and bridgeTurnResult on an empty accumulator queues
		// the parent a second, contradictory "turn produced no output" message
		// about a run that has finished. setState and releaseSlot below were
		// already inert for an ended run; the bridge was not.
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
			// Coordinator shutdown: this is the ONE
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
		// Turn accumulation for the legacy path's half of the result bridge:
		// a ONESHOT child has no reach-back at all, so every
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
		// LEGACY path's half of PREREQ A: the migrated path
		// already records this via StartRunResult/runchannel's
		// ctxloom/harness_session custom event (runChildViaStartRun above);
		// this is the only place a legacy go-plugin Chat dial's native
		// session id (any StructuredChat backend outside
		// viaStartRunBackends) reaches the journal at all — previously
		// silently dropped by this switch having no case for it.
		// recordHarnessSession is idempotent, so a repeat emission a legacy
		// backend's Chat sends when its conversation id first resolves is
		// safe.
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
	// A journal failure here is NOT "the mailbox is empty": parking
	// the child idle on it strands mail the fold still considers deliverable
	// and reports the boundary as clean. Fail the child instead — a stalled
	// child that says so beats one that silently stops consuming its mail.
	msg, ok, err := c.takeNextMail(rt.harp)
	if err != nil {
		c.failChild(rt, err)
		return
	}
	if ok {
		c.sendMailTurn(rt, msg)
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
	msg, ok, err := c.takeNextMail(rt.harp)
	if err != nil {
		// Not a spurious wake — the take FAILED. Returning quietly
		// would leave the child idle holding undelivered mail forever.
		c.failChild(rt, err)
		return
	}
	if !ok {
		return // spurious wake (a recv or boundary drain consumed it)
	}
	if err := c.slots.acquire(c.baseCtx); err != nil {
		return
	}
	c.mu.Lock()
	rt.slot = slotHeld
	c.mu.Unlock()
	c.setState(rt, StateExecuting)
	c.sendMailTurn(rt, msg)
}

// sendMailTurn writes one MAILBOX delivery to the child's input channel with the
// same provenance framing the migrated path's turn sink applies
// (frameCoordinatorDelivery). Both delivery paths frame: a turn that arrives
// unmarked is indistinguishable to the model from its own operator's
// instructions, which is a worse position than a marked frame in every case.
//
// sendTurn's other caller — the briefing — is deliberately unframed: it is the
// run's own prompt, not a delivery from somebody else.
func (c *Coordinator) sendMailTurn(rt *childRt, msg Message) {
	c.sendTurn(rt, frameCoordinatorDelivery(msg.From, msg.Kind, msg.Body))
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

// runEnded reports whether the run's fold record is terminal. An UNKNOWN run
// reports false: only a record that positively says "ended" suppresses work.
func (c *Coordinator) runEnded(runID string) bool {
	ended := false
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			ended = r.Ended
		}
	})
	return ended
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

// releaseSlot gives back a slot rt actually HOLDS. If rt is only
// slotClaimed — an acquisition (tryAcquire or a blocking acquire) is still
// in flight for it — releasing NOW would
// return a slot nobody has taken from the pool yet, INFLATING the cap.
// Instead this marks the claim cancelled; commitSlotClaim gives the slot
// back the instant it actually lands, rather than promoting to slotHeld and
// leaking it (nothing else will ever see slotHeld for this rt again).
func (c *Coordinator) releaseSlot(rt *childRt) {
	c.mu.Lock()
	switch rt.slot {
	case slotHeld:
		rt.slot = slotFree
		c.mu.Unlock()
		c.slots.release()
	case slotClaimed:
		rt.slotCancel = true
		c.mu.Unlock()
	default:
		c.mu.Unlock()
	}
}

// claimSlotIntent atomically claims rt's "this attempt owns acquiring a
// slot" right (one-shot-resume plan Slice 3): the check
// (rt.slot == slotFree) and the mutation (-> slotClaimed) happen inside the
// SAME c.mu window, so two racing callers for the SAME rt (a concurrent
// onTurnStarted/onRoleUnpark pair, or either fired twice) can never both
// decide "I need to acquire" — exactly one wins, matching releaseSlot's own
// pattern above. Returns false when rt already holds (or already owns
// claiming) a slot: the caller then does nothing further — the occupancy
// is already correctly accounted for. Deliberately NOT combined with the
// actual turnSlots acquisition (which can block for onRoleUnpark's caller,
// runtimeSlots.acquire) — holding c.mu across a blocking acquire would
// stall every OTHER coordinator operation needing c.mu for as long as the
// slot wait takes. The winner does NOT yet hold a real slot: it
// MUST call commitSlotClaim once its acquisition actually lands one, or
// releaseSlotIntent if the acquisition attempt itself failed.
func (c *Coordinator) claimSlotIntent(rt *childRt) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt.slot != slotFree {
		return false
	}
	rt.slot = slotClaimed
	rt.slotCancel = false
	return true
}

// commitSlotClaim finalizes a claimSlotIntent win once its acquisition
// (tryAcquire or a blocking acquire) has actually landed a real turnSlots
// slot. If nothing cancelled the claim while the acquisition was
// in flight, the state becomes slotHeld — the ONLY state releaseSlot/
// onRolePark may release against. If a concurrent releaseSlot/onRolePark
// fired WHILE the acquisition was still pending (slotCancel), the claim
// instead reverts to slotFree and the caller MUST immediately give the
// just-landed slot back (c.slots.release()) — it was rendered unwanted the
// moment it arrived, and nothing else will ever release it: the racing
// releaseSlot/onRolePark call already ran and deliberately did NOT call
// turnSlots.release() itself, because at that moment the slot was only
// claimed, not held (see releaseSlot's doc).
func (c *Coordinator) commitSlotClaim(rt *childRt) (keep bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt.slotCancel {
		rt.slot = slotFree
		rt.slotCancel = false
		return false
	}
	rt.slot = slotHeld
	return true
}

// releaseSlotIntent undoes a claimSlotIntent win whose acquisition attempt
// itself did not pan out (tryAcquire found no free slot, or a blocking
// acquire's ctx was cancelled) — no real slot was ever landed, so this is
// pure bookkeeping, never a turnSlots.release() call. Never leave rt.slot
// reading slotHeld/slotClaimed when no turnSlots slot is actually held
// (assertion (f), slot conservation, exists specifically to catch a
// regression here).
func (c *Coordinator) releaseSlotIntent(rt *childRt) {
	c.mu.Lock()
	rt.slot = slotFree
	rt.slotCancel = false
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
// synthesis, an explicit RunExited, agent_stop, launch failure,
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
	// The DOWN direction's outstanding requests are settled, not merely
	// dropped: their callers are blocked on an answer this run will never
	// give, and the terminal is the fact that decides it.
	c.clearDownTrack(rec.Harp)

	// D4: drain BEFORE anything below that can tear the
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
	var runFailure string
	if rt != nil {
		closeFn = rt.close
		rt.close = nil
		launchCancel = rt.launchCancel
		rt.launchCancel = nil
		// The engine's own reason for dying (captureRunFailure) — read here,
		// under the same lock that owns rt, to fold into the parent notice.
		// When the engine emitted no FAILED RunCompleted (a docker-stop / OOM
		// = runner loss, where the whole runner vanishes without a terminal
		// event), fall back to the runner's captured stderr tail — the
		// container's own dying words, streamed to us BEFORE teardown removed
		// it. Read while rt is still ours; the accessor is cheap (a mutex +
		// string copy) and nil-safe.
		runFailure = rt.runFailure
		if runFailure == "" && rt.stderrTail != nil {
			runFailure = strings.TrimSpace(rt.stderrTail())
		}
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
	// death. Kind distinguishes a launch failure (error) from
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
		// A dead engine says WHY: the FAILED RunCompleted's reason
		// (captureRunFailure) — carrying the adapter's stderr tail since the
		// internal/acp capture — is appended when the run failed and the
		// terminal cause did not already carry it. Without this a child that
		// died in its module loader reached the parent as a bare
		// "exited (runner-exit)", the 49-minute dead end. Not appended when
		// detail already IS this text (belt-and-suspenders against a future
		// path that threads it through detail too).
		if runFailure != "" && !strings.Contains(body, runFailure) {
			kind = "error"
			body += ": " + runFailure
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
// reached enqueueRun.
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
	caller := Identity{Harp: rec.ParentHarp, RunID: parentRunID, Project: c.projectDir}
	// Last check before this attempt becomes a REAL run: a stop that landed
	// while Resolve was in flight (config read, agent resolution — slow
	// enough to matter in production) must not be overtaken here.
	if c.launchStopped(harp) {
		return
	}
	// A resume keeps the SAME run identity, not a new generation: pass the
	// ended run's own recorded depth straight through rather than deriving
	// it from caller.Depth+1 (which would need caller.Depth = rec.Depth-1,
	// the exact reconstruction this explicit depth parameter replaces).
	rt, token, err := c.enqueueRun(caller, plan, harp, "", true, attached, rec.Depth)
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

	// Read under c.mu — rt is already published (enqueueRun) at
	// this point, so onRolePark/onRoleUnpark/onTurnStarted/claimSlotIntent
	// can all touch rt.slot concurrently; matches runChild's own read.
	c.mu.Lock()
	held := rt.slot == slotHeld
	c.mu.Unlock()
	if !held {
		if err := c.slots.acquire(c.baseCtx); err != nil {
			c.failChild(rt, err)
			return
		}
		c.mu.Lock()
		rt.slot = slotHeld
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

	// LEGACY resume (Slice 0): mirror the ViaStartRun branch
	// above — a captured native session id (journaled via
	// handleChildEvent's ev.Session case) resumes the backend's OWN session
	// with no rendered-transcript re-priming needed; only a prior run that
	// never reported a session id falls back to the lossy ResumeContext
	// replay.
	contextText := ""
	if !haveResumeKey {
		contextText = c.spawner.ResumeContext(lctx, plan, harp)
	}
	launch, err := c.spawner.Launch(lctx, plan, contextText, resumeSessionID,
		c.childEnv(harp), runnerEnv(harp, rt.runID, token, url, rt.depth, plan.ResumeMode == ResumeModeOneShot))
	if err != nil {
		c.failChild(rt, err)
		return
	}
	c.noteLaunchAttached(harp)
	c.attachLaunch(rt, launch)
	if msg, ok, merr := c.takeNextMail(harp); merr != nil {
		c.failChild(rt, merr)
		return
	} else if ok {
		c.sendMailTurn(rt, msg)
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
//
// onRoleUnpark's re-acquisition can be a genuinely long BLOCKING
// wait (turnSlots.acquire). If onRolePark lands while rt is only
// slotClaimed for that wait (not yet slotHeld), releasing here would give
// back a slot nobody has actually taken from the pool yet — see
// releaseSlot's doc, which this mirrors exactly (kept separate rather than
// calling releaseSlot because onRolePark alone decides whether to
// setState(StateParked), and only in the slotHeld case, matching prior
// behavior).
func (c *Coordinator) onRolePark(role string) {
	c.mu.Lock()
	rt := c.byHarp[role]
	var wasHeld bool
	if rt != nil {
		switch rt.slot {
		case slotHeld:
			wasHeld = true
			rt.slot = slotFree
		case slotClaimed:
			rt.slotCancel = true
		}
	}
	c.mu.Unlock()
	if wasHeld {
		c.setState(rt, StateParked)
		c.slots.release()
	}
}

// onRoleUnpark re-acquires the slot before a parked recv completes — the
// child resumes an EXECUTING turn, and the cap counts executing turns.
// claimSlotIntent (one-shot-resume plan Slice 3) makes "do I still need
// to acquire" atomic with claiming ownership of doing so: a duplicate/
// racing unpark signal for the SAME rt (or a race against onTurnStarted)
// finds a claim or a held slot already in place, skips the blocking
// acquire entirely, and just reasserts StateExecuting (idempotent) — never
// a second acquisition against the same rt. commitSlotClaim
// finalizes the win once the blocking acquire actually lands a real slot;
// if a concurrent onRolePark/releaseSlot cancelled the claim while that
// wait was in flight, the just-landed slot is unwanted and must be given
// straight back rather than leaked.
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
		if !c.commitSlotClaim(rt) {
			c.slots.release()
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
