package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
)

const (
	// agentDepthCap is the BUILT-IN DEFAULT for the delegation tree's DEPTH
	// cap — the single policy knob both the "may this run spawn" guard
	// (AgentRun, below) and the runner-side leaf computation
	// (internal/adapters/cli/attachRunnerMCP, via config.Config.GetDelegationDepth)
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
	// internal/adapters/cli/llm_runner_common.go) — GetDelegationDepth already
	// applies this identical default, so the two can never drift apart.
	// Currently 1 — flat fan-out: the owner (depth 0) may spawn subagents
	// (depth 1), and a depth-1 subagent may not itself spawn (no
	// grandchildren). Raising config.DefaultDelegationDepth to 2 would
	// re-enable one further level with no other code change — the property
	// this design is meant to have, even though the value stays 1 today.
	agentDepthCap = config.DefaultDelegationDepth
	// agentConcurrencyCap is the BUILT-IN DEFAULT for the execution-slot cap
	// (Coordinator.slots): the
	// maximum number of delegated child turns EXECUTING at once (each a live
	// engine process). NOT a correctness gate: the coordinator's own state is
	// partitioned by child identity and safe under real concurrency by
	// construction. Production sources the live value from
	// config.Config.GetDelegationConcurrency (Options.Concurrency); <= 0
	// (unset) falls back to this constant. Renamed from agentTurnCap: "turn
	// cap" read as a per-run quota, which it never was. The cap counts
	// EXECUTING turns only — a child parked in agent_recv or idle at a turn
	// boundary yields its slot (Coordinator.slots is a resource limiter: the slot is
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
// slot cap (Coordinator.slots), replacing a single overloaded bool. The
// old `slotHeld` bool did double duty as both "I am ATTEMPTING to acquire a
// slot" (set true by claimSlotIntent BEFORE a potentially long BLOCKING
// acquire — onRoleUnpark's) and "I actually HOLD a slot" (the only fact
// releaseSlot/onRolePark may safely act on). Those are different facts with
// an unbounded gap between them, and a concurrent releaseSlot/onRolePark
// landing inside that gap could not tell which one it was looking at:
//   - reading the bit true while only "claiming" (not yet acquired) and
//     releasing anyway releases a token nobody holds, which since the cap
//     is a semaphore.Weighted PANICS on the spot rather than INFLATING the
//     cap silently; and
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
	slotHeld // a real execution slot is actually held — the only state a
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
	// moment they start. Journaled onto runEnqueued.ParentRunID: the
	// coordinator's own record is the durable lineage.
	parentRunID string
	// depth is this run's own position in the delegation tree — journaled
	// onto runEnqueued.Depth and carried to its runner on the Launch's
	// identity: 0 for the session owner's own run (StartOwnedRun,
	// which reuses the owner's identity rather than spawning a child), and
	// (spawning run's depth + 1) for every genuinely delegated child
	// (AgentRun/resumeChild). Set once at enqueueRun, never mutated.
	depth int
	plan  *SpawnPlan

	// slot is this childRt's relationship to the D4 execution-slot cap
	// (Coordinator.slots) — see slotState's doc for why this is a
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
	// exitRequested is a bulk agent_stop's REQUEST (drain.go, requestExit):
	// set while this run's turn is in flight, it makes the turn boundary end
	// the run under that policy instead of parking it idle. Nil otherwise.
	// Guarded by Coordinator.mu. Per RUN, not per harp, so a resumed run
	// can never inherit a stale request.
	exitRequested *drainPolicy
	// ownerRun marks a top-level, OWNER-OWNED run (StartOwnedRun): the owning
	// session's own structured/oneshot container run, minted parent-less
	// with the OWNER'S HARP reused as its run role. It rides the same
	// RunChannel machinery a delegated child does, but has no distinct
	// parent to report to — the host watches it directly via WatchRuns, and
	// its runner files no automatic turn report (HomeConfig.Depth 0).
	// Everything else (turn state, slot accounting, spool-borne follow-up
	// turns) is identical to a child.
	ownerRun bool
	close    func()
	// idleSince is when this run's runner last went idle (a turn boundary);
	// zero while a turn is in flight. The idle reaper reads it.
	idleSince time.Time
	// workDir is the isolation-resolved workspace this run's engine was
	// started in (EngineSpawn.WorkDir). It exists for the liveness watchdog:
	// the worktree's newest mtime is the only activity clock that is not
	// written by the engine's own bookkeeping, so it is what distinguishes an
	// agent inside a ten-minute build from one that is hung.
	workDir string
	// runFailure is the last FAILED RunCompleted's Result.Text for this run —
	// the engine's own reason for dying, which carries the adapter's dying
	// words (a module-loader SyntaxError, a JSON-RPC -32603 "Invalid API
	// key"). A child that dies below the protocol emits NO final-channel
	// output, so its runner's turn report has nothing to say and the parent
	// would otherwise learn only "exited (runner-exit)" with no cause — the
	// exact silent dead-end the 49-minute incident was. terminateRun folds
	// this into the parent's terminal notice so a dead engine can say WHY.
	// Captured on the RunChannel receive path (HandleEvent), read once
	// at terminal.
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
	// runnerWait blocks until the runner PROCESS exits, reporting why. It is
	// the DEATH half of the standup race issueStartRun runs: dial-home
	// readiness is a push (awaitRunner parks on a channel the runner's Hello
	// closes) and carries no "…and it is still alive" signal, so this is the
	// only thing that can tell a dead runner from a slow one before the
	// dial-home budget expires. Nil when the spawner captures no process
	// (test doubles, the owner-run path), which degrades to the timeout.
	// Set at spawn (runChildViaStartRun), alongside stderrTail.
	runnerWait func() error

	// attached closes once THIS attempt's launch decision is final: the
	// engine is up (the StartRun round-trip completed) or the attempt failed
	// (failChild). It backs awaitChildUp/armLaunch —
	// a test-facing deterministic quiesce seam over the
	// launch/resume pipeline, replacing a wall-clock Eventually poll with a
	// wait keyed on the actual goroutine's own progress. Set once at
	// creation (enqueueRun); markAttached closes it exactly once.
	attached chan struct{}
}

// newRunID mints a run attempt id (UUID-shaped; retries get a fresh one).
func newRunID() string { return RandID("run-", 16) }

// RunOutcome is agent_run's return payload, fixed at enqueue.
type RunOutcome struct {
	Harp     string
	RunID    string
	Engine   string
	Profiles []string
	Runtime  launch.RuntimeAxis
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
// spawn does when the parent tree is dirty (the zero value = project default,
// cfg.GetDirtyTreeHandler()). It is the TYPED value: callers parse the
// caller-supplied spelling at their own edge, so an unrecognized one is
// refused where the caller can see it rather than resolved here — see
// operations.handleDirtyParentTree. Deliberately does NOT carry any
// acknowledgement for the "commit" handler's mutation: that is a
// per-checkout, human-only acknowledgement (dirty_tree_commit_ack — see
// config.DirtyTreeCommitAcknowledged) that this per-call parameter can never
// set: it is not even a config key any longer, precisely so no channel an
// agent can reach (config, env, argv) can grant it.
func (c *Coordinator) AgentRun(ctx context.Context, caller Identity, agentName, prompt string, workspace launch.WorkspaceAxis, dirtyTreeHandler launch.DirtyTreeHandler) (*RunOutcome, error) {
	if c.Draining() {
		return nil, fmt.Errorf("agent_run: %w", ErrDraining)
	}
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

	// EVERYTHING FROM HERE TO enqueueRun IS THE TRACELESS SPAN. The run has
	// no id yet, so nothing can be journaled against it, nothing appears in
	// the roster, and no log line says a spawn is in progress — while three
	// separate steps below can block for an unbounded time (agent
	// resolution, the session-index flock inside AssignSession, the
	// reach-back endpoint). Measured once at 6m59s, against a caller budget
	// of DefaultRequestTimeout: the caller times out, sees nothing anywhere,
	// concludes the spawn never happened, and retries — which is how one
	// brief came to be executed by three concurrent children in one checkout
	// (task affected-yearly).
	//
	// Two things close that. The audit fact below is the DURABLE record that
	// a spawn was accepted, written before the span rather than after it, so
	// "accepted but not yet registered" is a state an operator can read back
	// instead of infer. notePendingSpawn is the LIVE one: a span that
	// outlives its notice budget says so out loud, naming the caller and the
	// agent, rather than being silent for as long as it takes.
	c.audit("agent_run.accepted", caller.Harp, map[string]string{"agent": agentName})
	defer c.notePendingSpawn(caller, agentName)()

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
		c.releaseAssignedHarp(harp, err)
		return nil, err
	}

	rt, token, err := c.enqueueRun(caller, plan, harp, prompt, false, make(chan struct{}), caller.Depth+1)
	if err != nil {
		c.releaseAssignedHarp(harp, err)
		return nil, err
	}
	c.audit("agent_run", caller.Harp, map[string]string{"agent": agentName, "harp": harp, "run_id": rt.runID})

	// The engine-version probe execs the vendor's own CLI, which can hang;
	// engineversion.DefaultProbeTimeout bounds that, but a bound is not
	// speed. It runs HERE — after run.enqueued is durable and the run is in
	// the roster — and deliberately not inside AssignSession, where it used
	// to sit between minting the child's address and registering the child
	// anywhere visible. Nothing on the launch path reads what it records:
	// the recorded version is consumed much later, when this session's
	// transcript is parsed back (sessions.Entry.EngineVersion).
	// The QUEUED answer is read HERE, before any goroutine that can change
	// it exists. rt.slot is the ENQUEUE's own claim — enqueueRun tryAcquires
	// a free slot precisely so this answer can be truthful — but the moment
	// runChild is dispatched that field belongs to it: the acquire promotes
	// a claim to slotHeld, and any terminal releases it back to slotFree.
	// Read after the dispatch, the disposition reported whatever the child
	// goroutine happened to have reached by then, so a child admitted
	// immediately and then failed to launch was answered with "queued behind
	// the execution cap" — a statement about a different run, and the one
	// state the caller is most likely to wait on rather than investigate.
	c.mu.Lock()
	queued := rt.slot != slotHeld
	c.mu.Unlock()

	backend := plan.Backend
	c.goTracked(func() { c.spawner.RecordEngineVersion(c.baseCtx, harp, backend) })

	c.goTracked(func() { c.runChild(rt, prompt, token, url) })
	if hook := c.spawnDispatchedHook; hook != nil {
		hook(harp)
	}

	runtime := plan.Runtime
	if runtime == "" {
		runtime = "host"
	}
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

// releaseAssignedHarp gives back a harp AssignSession has already COMMITTED
// to persistent session accounting, for a spawn that aborted before the run
// was ever registered. Both of AgentRun's post-assignment refusals reach it:
// an unresolvable reach-back endpoint, and an enqueue whose journal failed.
//
// Without it the verb reports failure while the accounting says a session
// started — a child that will never exist, holding an address and (in
// production, via operations.EndSession's other half) a per-session engine
// home with a credential copy in the project tree. There is no narrower
// release primitive: MarkSessionEnded is the Spawner's only one, and it is
// the same call every terminal path already makes, so this adds no surface.
//
// It is best-effort and never changes what the caller is told: the refusal
// the caller already earned is the answer, and a failed release is the
// implementation's own diagnostic (MarkSessionEnded warns for itself).
func (c *Coordinator) releaseAssignedHarp(harp string, cause error) {
	c.rep.Warnf("agent_run: releasing session %s — the spawn was refused before the run was registered: %v", harp, cause)
	c.spawner.MarkSessionEnded(harp)
}

// defaultSpawnNoticeAfter is how long agent_run's pre-registration span may
// run before it reports itself. Well inside DefaultRequestTimeout, the
// caller's own budget: the point is that a notice exists BEFORE the caller
// gives up and starts deciding whether to retry, not after.
const defaultSpawnNoticeAfter = 15 * time.Second

// logSpawnPending is the structured-log message a spawn parked past its
// notice budget leaves behind.
const logSpawnPending = "agent_run_spawn_pending"

// notePendingSpawn arms the watchdog over agent_run's pre-registration span
// and returns the function that stands it down. The caller defers that
// function, so every exit from the span — success, refusal, panic — disarms
// it.
//
// It only REPORTS; it never cancels. Whether a spawn that has been preparing
// for a quarter of a minute should be abandoned is a policy question with a
// real cost on the wrong side (a slow-but-working spawn killed at the finish
// line), and it is not the question this defect is about: the harm measured
// was a caller with no way to tell a slow spawn from a dead one. A notice
// answers that; a cancellation does not.
func (c *Coordinator) notePendingSpawn(caller Identity, agentName string) (settled func()) {
	after := c.spawnNoticeAfter
	if after <= 0 {
		return func() {}
	}
	timer := time.AfterFunc(after, func() {
		c.audit("agent_run.pending", caller.Harp, map[string]string{"agent": agentName, "after": after.String()})
		zap.L().Warn(logSpawnPending, zap.String("caller", caller.Harp), zap.String("agent", agentName), zap.Duration("after", after))
		c.rep.Warnf("agent_run: %s's spawn of agent %q has been preparing for over %s and is not registered yet, "+
			"so it is not in the roster and has no run id; it is still starting, NOT lost — do not spawn a second one",
			callerLabel(caller), agentName, after)
	})
	return func() { timer.Stop() }
}

// callerLabel names a caller for a human-facing line. A depth-0 owner may
// have no harp (nothing minted one), and "'s spawn" with an empty subject
// reads as a bug in the message rather than a fact about the caller.
func callerLabel(caller Identity) string {
	if caller.Harp == "" {
		return "this session"
	}
	return caller.Harp
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
// place runEnqueued.Depth and childRt.depth are set from, so the identity
// the runner receives on its Launch and the server-side recursion guard
// (AgentRun's caller.Depth, read back from this same fact via Identify) never
// diverge.
func (c *Coordinator) enqueueRun(caller Identity, plan *SpawnPlan, harp, prompt string, resume bool, attached chan struct{}, depth int) (*childRt, string, error) {
	runID := newRunID()
	token, credHash, err := mintToken()
	if err != nil {
		return nil, "", err
	}
	won := true
	var mcpServerNames []string
	for _, srv := range plan.MCPServers {
		mcpServerNames = append(mcpServerNames, srv.Name)
	}
	slices.Sort(mcpServerNames)
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
			OneShot:    plan.ResumeMode == ResumeModeOneShot,
			Prompt:     prompt,
			Resume:     resume,
			Permission: plan.Permission,
			// Names only, sorted: an operator auditing a live delegation sees
			// WHAT a child can reach; command, args and env — any of which can
			// carry a credential — never enter the journal, and the journaled
			// value is stable across runs.
			MCPServers: mcpServerNames,
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
		attached:    attached,
	}
	// Claim a free slot now when one exists so `queued` is truthful at
	// return. Set BEFORE publication — after it, rt.slot belongs to
	// the mutex (the park hooks may touch it).
	if c.slots.TryAcquire(1) {
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

// runnerEnv builds the per-spawn env stamped onto the RUNNER process (host:
// cmd.Env on the runner subprocess; container: bare-name `-e` forms with the
// values on the run-process env — never `-e KEY=VAL` argv, never the
// process-global launcher env, which is racy across concurrent spawns): the
// reach-back trio — the coordinator URL, the per-run credential and the run
// id — and nothing else. The run's identity (its harp, its depth, whether it
// is one-shot) arrives ONCE, typed, on the Launch that rides StartRun; no
// reader takes it from the environment. The session owner's own runner is
// stamped on the same terms (StartOwnedRun). url may be empty (a degraded
// launch without reach-back); the trio is then omitted whole.
func runnerEnv(runID, token, url string) map[string]string {
	env := map[string]string{}
	if url != "" {
		for k, v := range sessions.EncodeReach(sessions.Endpoint{URL: url, Credential: token}, runID) {
			env[k] = v
		}
	}
	return env
}

// spawnReachURL resolves the coordinator URL a child on runtimeAxis can dial,
// widening the listeners for a container child.
//
// A child without reach-back is refused in EVERY strictness, --degraded
// included. Its mail is a file spool, but the spool is swept by the child's
// own runner, and that runner learns which spool is its own and rings its
// doorbells over the run channel it dials home on: no reach-back means no
// runner sweeping the child's in/ and no route for anything it writes to
// out/. The child would run, spend real quota, and produce work nobody ever
// receives. That is lost work, so the spawn is refused and the message names
// the way out.
func (c *Coordinator) spawnReachURL(harp string, runtimeAxis launch.RuntimeAxis) (string, error) {
	url, err := c.ReachURL(runtimeAxis)
	if err == nil {
		return url, nil
	}
	return "", fmt.Errorf("agent_run: no coordinator endpoint reachable from runtime %q: %v — this child could not dial home, so nothing it sends could be routed and its work would be lost; check the container runtime's bridge network", runtimeAxis, err)
}

// runChild is a spawned child's driver goroutine: wait for an execution slot
// (D4), then spawn the runner and issue StartRun with the briefing as the
// first turn; the runner drives every later turn from its own spool.
func (c *Coordinator) runChild(rt *childRt, prompt, token, url string) {
	if err := c.acquireRunSlot(rt); err != nil {
		// A terminal that landed while this spawn was parked on the cap has
		// already ended the run: do NOT go on to launch an engine for it.
		if !errors.Is(err, errSlotClaimCancelled) {
			c.failChild(rt, err)
		}
		return
	}
	c.setState(rt, StateExecuting)

	// The launch runs under a CANCELLABLE per-harp context, not baseCtx, so
	// agent_stop reaches a launch that is in flight (container prepare, image
	// pull, fs probe) and not merely the process a completed launch produced
	// — see launchgate.go.
	lctx, lcancel, deregister := c.launchContext(rt.harp)
	defer deregister()
	c.mu.Lock()
	rt.launchCancel = lcancel
	c.mu.Unlock()

	c.runChildViaStartRun(lctx, rt, prompt, token, url, SpawnStart{})
}

// defaultRunnerAwaitTimeout is the package default for the wait for a
// just-spawned runner process to dial home before the spawn is declared
// failed (spawn + handshake + dial; container image pulls are NOT in this
// window — image staging happens in StartEngine's isolation prepare, before
// this clock starts). Overridable per-coordinator via Options.
// RunnerAwaitTimeout (coordinator.go), which is what issueStartRun actually
// reads (c.runnerAwaitTimeout).
//
// Widened from an original 60s:
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

// runChildViaStartRun is the spawn tail: resolve the child's launch and
// spawn its runner process (the coordinator trio on the runner's env, never
// the engine's), await its RunnerChannel dial-home, and issue StartRun with
// the Launch — the one typed message the runner redeems, decodes, delivers
// and drives. The first turn's lead is settled HERE, before the launch
// resolves, and rides Launch.Prompt: the caller's prompt on a fresh spawn;
// on a resume with a native key NOTHING (the engine continues its own
// recorded session); on a resume without one the rendered history ahead of
// the prompt. The runner leads with the package's context. ctx is the
// caller's CANCELLABLE launch context (launchgate.go), not baseCtx:
// agent_stop cancels it to abort a spawn that is still in flight.
func (c *Coordinator) runChildViaStartRun(ctx context.Context, rt *childRt, prompt, token, url string, start SpawnStart) {
	start.Identity = Identity{Harp: rt.harp, RunID: rt.runID, Depth: rt.depth, OneShot: rt.plan.ResumeMode == ResumeModeOneShot, Project: c.projectDir}
	start.Identity.Leaf = start.Identity.IsLeaf(c.depthCap)
	start.Orchestrator = c.ownerHarp
	start.Prompt = prompt
	if start.Resumed && start.ResumeKey == "" {
		start.Prompt = textblocks.Join(c.spawner.ResumeHistory(ctx, rt.harp), prompt)
	}
	resolved, err := c.spawner.ResolveLaunch(ctx, rt.plan, start)
	if err != nil {
		c.failChild(rt, err)
		return
	}
	engine, err := c.spawner.Start(ctx, resolved.Launch, sessions.Endpoint{URL: url, Credential: token})
	if err != nil {
		c.failChild(rt, err)
		return
	}
	l := resolved.Launch
	c.recordCell(rt.runID, l)
	c.mu.Lock()
	// A terminal that landed while Start was in flight found no close to
	// call (terminateRun takes rt out of c.attach and nils rt.close). The
	// runner it just stood up is then this launch's to kill, right here:
	// stored on an rt nothing will ever revisit, it outlives the run.
	if c.attach[rt.runID] != rt {
		c.mu.Unlock()
		engine.Kill()
		return
	}
	rt.close = engine.Kill
	rt.stderrTail = engine.StderrTail
	rt.runnerWait = engine.Wait
	rt.workDir = l.Cell.Workspace
	c.mu.Unlock()

	err = c.issueStartRun(ctx, rt, hashToken(token), resolved.Launch, l.Prompt, l.Label.Model, start.ResumeKey, true)
	if !errors.Is(err, errEndpointUnavailable) {
		return
	}
	// THE REBIND: the runner is up and dialed home but could not bind the
	// session's recorded endpoint — another process took the port between
	// two incarnations. Re-resolve with a rebind (a new address is minted and
	// bound on the session record; the static plan is re-delivered, which a
	// resume does anyway) and re-issue StartRun to the SAME runner. Once: a
	// runner that cannot bind a freshly minted address has a problem no
	// second mint fixes.
	start.Rebind = true
	rebound, rerr := c.spawner.ResolveLaunch(ctx, rt.plan, start)
	if rerr != nil {
		c.failChild(rt, fmt.Errorf("rebind the session endpoint: %w", rerr))
		return
	}
	c.audit("endpoint_rebind", rt.harp, map[string]string{"run_id": rt.runID})
	_ = c.issueStartRun(ctx, rt, hashToken(token), rebound.Launch, rebound.Launch.Prompt, rebound.Launch.Label.Model, start.ResumeKey, false)
}

// errEndpointUnavailable is issueStartRun's report that the runner refused
// the launch because its recorded endpoint could not be bound — returned
// WITHOUT failing the child when the caller may still rebind.
var errEndpointUnavailable = errors.New("coord: the runner could not bind the session's recorded endpoint")

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
// startRunPayloadErr refuses a StartRun that would carry no work at all: an
// empty Launch.Prompt with nothing else to drive round-trips, the run
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

func (c *Coordinator) issueStartRun(ctx context.Context, rt *childRt, credHash string, l launch.Launch, first, model, resumeSessionID string, mayRebind bool) error {
	actx, acancel := context.WithTimeout(ctx, c.runnerAwaitTimeout)
	// The standup RACE: readiness (awaitRunner, a push the runner's Hello
	// closes) against DEATH (the runner process exiting). Without the second
	// arm the wait can only ever end on the clock, so a runner that died at
	// standup — an unknown backend name, a refused config, a missing binary,
	// a fail-loud startup finding — held the parent in TOTAL SILENCE for the
	// whole runnerAwaitTimeout: no agent_send, no bridged turn, and not even
	// the terminal notice failChild would eventually queue. An observer
	// watching for less than that budget sees a child that simply never
	// reports anything, which is indistinguishable from an engine that hung.
	exited := watchRunnerExit(rt.runnerWait, acancel)
	_, err := c.awaitRunner(actx, credHash)
	acancel()
	if err != nil {
		// Attribute before blaming the clock: a runner that is GONE gets the
		// death (with its dying words), not "never dialed home".
		if reason, dead := runnerExitReason(exited); dead {
			err = fmt.Errorf("runner exited before dialing home (StartRun path): %s", reason)
		} else {
			err = fmt.Errorf("runner never dialed home (StartRun path): %w", err)
		}
		c.failChild(rt, err)
		return err
	}
	if err := c.startRunPayloadErr(rt, first, resumeSessionID); err != nil {
		c.failChild(rt, err)
		return err
	}
	// The round trip hangs off the LAUNCH context, not c.baseCtx. ctx is the
	// cancellable per-harp context agent_stop cancels (launchgate.go); baseCtx
	// dies only on coordinator shutdown, so binding the request to it left a
	// stop issued after the dial-home wait — the runner is up, StartRun is on
	// the wire, the engine has not answered — with nothing to cancel: the
	// coordinator stayed parked for the whole DefaultRequestTimeout while the
	// operator's stop reported success.
	rctx, rcancel := context.WithTimeout(ctx, DefaultRequestTimeout)
	resp, err := c.requestRunner(rctx, credHash, RunnerRequest{Kind: StartRun{RunID: rt.runID, Launch: l}})
	rcancel()
	if err != nil {
		err = fmt.Errorf("StartRun never completed: %w", err)
		c.failChild(rt, err)
		return err
	}
	if resp.Err != nil {
		if mayRebind && errors.Is(resp.Err, ErrRunnerUnavailable) {
			// The runner could not bind the recorded endpoint: the caller
			// answers with ONE rebind on this same runner, so the child is
			// not failed here.
			return fmt.Errorf("%w: %s", errEndpointUnavailable, resp.Err.Error())
		}
		err = fmt.Errorf("StartRun refused: %s", resp.Err.Error())
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
	if res, ok := resp.Kind.(StartRunResult); ok && res.HarnessSessionID != "" {
		c.recordHarnessSession(rt.runID, res.HarnessSessionID)
	}
	// Mail written while the engine was coming up is the runner's own
	// startup sweep's to deliver, as turns.
	c.noteLaunchAttached(rt.harp) // a launch that came up resets the retry budget
	c.markAttached(rt)            // StartRun round-tripped: the migrated run is up
	return nil
}

// watchRunnerExit starts the DEATH arm of issueStartRun's standup race: it
// reaps the runner process in the background and, the moment it exits, both
// publishes why and cancels the dial-home wait so the caller stops waiting for
// a runner that no longer exists.
//
// ORDER MATTERS: the exit reason is published to the buffered channel BEFORE
// cancel fires. The waiter is woken by that cancel, so by the time it looks,
// the reason is already there — the reverse order would race the waiter into
// reporting a bare timeout about a runner it had just been told was dead.
//
// A nil wait (a spawner that captures no process) returns a nil channel: the
// receive below simply never yields, and detection degrades to the timeout
// that was the only mechanism before this existed.
//
// The goroutine does not leak on the HEALTHY path. It blocks in wait() for the
// runner's whole lifetime, which is the point — when the child eventually dies
// it sends into a BUFFERED channel (never blocking on the absent reader),
// cancels an already-cancelled context (a no-op), and returns.
func watchRunnerExit(wait func() error, cancel context.CancelFunc) <-chan error {
	if wait == nil {
		return nil
	}
	exited := make(chan error, 1)
	go func() {
		err := wait()
		exited <- err
		cancel()
	}()
	return exited
}

// runnerExitReason reports whether the runner process is already known to have
// exited, and why. The read is NON-BLOCKING: it is called on a wait that has
// just ended and must answer "is this runner dead?" from what is already
// known, never wait around to find out.
//
// A CLEAN exit still counts as death. A runner that exits 0 without dialing
// home has failed just as completely as one that crashed — `ctxloom` printing
// help and exiting 0 is the documented shape of this (see
// pb.StartHostRunner's refusal of a bare self-exec) — and reporting only
// non-zero exits would let the quietest failure keep the old silent timeout.
func runnerExitReason(exited <-chan error) (string, bool) {
	select {
	case err := <-exited:
		if err != nil {
			return err.Error(), true
		}
		return "runner process exited cleanly (status 0) without ever dialing home", true
	default:
		return "", false
	}
}

// recordCell journals the run's resolved cell — the workspace, the engine and
// the binding's engine-home policy — so a coordinator that re-adopts the run
// after a restart can re-bind its engine home without the Launch.
func (c *Coordinator) recordCell(runID string, l launch.Launch) {
	homeMode := ""
	if len(l.Home) > 0 {
		homeMode = "session"
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		if c.runsF.run(runID) == nil {
			return nil, nil
		}
		return []Fact{factAt(factRunCell, c.now(), runCell{RunID: runID, WorkDir: l.Cell.Workspace, Engine: string(l.Engine), HomeMode: homeMode})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator: record run cell: %v", err)
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
		c.rep.Warnf("coordinator: record harness session id: %v", err)
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
		c.rep.Warnf("coordinator: record run resumable: %v", err)
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
	if rt != nil {
		rt.idleSince = time.Time{}
	}
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
		if !c.slots.TryAcquire(1) {
			c.releaseSlotIntent(rt)
		} else if !c.commitSlotClaim(rt) {
			// Cancelled between the claim and this (non-blocking)
			// tryAcquire landing — the same reason onRoleUnpark must
			// handle it below, just a far smaller window here.
			c.slots.Release(1)
		}
	}
	c.setState(rt, StateExecuting)
}

// captureRunFailure records a FAILED RunCompleted's reason on the child's
// runtime so terminateRun can fold it into the parent's terminal notice. The
// reason is the engine's OWN account of its death — via the stderr-tail
// capture it carries the adapter's dying words (a module-loader
// SyntaxError, a JSON-RPC -32603 "Invalid API key") for a death that happens
// below the protocol, exactly the case in which its runner's turn report has
// nothing to say. Any terminal that is neither SUCCEEDED nor
// CANCELLED is captured when its text is non-empty: those two are not
// failures to explain, and an empty text carries nothing (this project's
// silent no-op — never surfaced as a reason).
func (c *Coordinator) captureRunFailure(role string, ev Event) {
	rc, ok := ev.Payload.(RunCompleted)
	if !ok || rc.Result == nil {
		return
	}
	res := rc.Result
	// SUCCESS IS AN ALLOW-LIST. This used to test
	// `!= RUN_STATUS_FAILED`, so a run that ended on the enum's ZERO value —
	// what an engine that never set a status produces — or on TIMED_OUT /
	// BUDGET_EXCEEDED had its dying words silently dropped and the parent got
	// no reason at all. CANCELLED stays excluded deliberately: a deliberate
	// stop is not a failure to explain.
	switch res.Status {
	case RunStatusSucceeded, RunStatusCancelled:
		return
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return
	}
	c.mu.Lock()
	if rt := c.byHarp[role]; rt != nil {
		rt.runFailure = text
	}
	c.mu.Unlock()
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
		// onTurnStarted) changes nothing: setState and releaseSlot below are
		// inert for an ended run.
		return
	}
	// DRAINING: the boundary is where a drain's exit request is honoured
	// (drain.go). Checked before the one-shot teardown so the terminal says
	// drained (or stopped) rather than resumable — nothing resumes it here.
	if p := c.exitRequested(rt); p != nil {
		c.drainAtBoundary(rt, p)
		return
	}
	// A one-shot child's ENGINE process ended at this boundary, on the runner
	// — the runner itself stays, parked, its endpoint bound; the run is idle
	// like any other and the next mail (or a Turn frame) starts a fresh
	// engine process resumed by key. The idle reaper is what ends a runner
	// nobody writes to.
	c.mu.Lock()
	rt.idleSince = c.now()
	c.mu.Unlock()
	c.setState(rt, StateIdle)
	c.releaseSlot(rt)
}

// failChild reports a launch failure to the parent's mailbox — the spawn verb
// already returned (async), so the mailbox is where the coordinator learns.
func (c *Coordinator) failChild(rt *childRt, err error) {
	c.rep.Warnf("agent_run: child %s (%s) failed to launch: %v", rt.harp, rt.agentName, err)
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
		c.rep.Warnf("agent %s: journal state %s: %v", rt.harp, state, err)
		return
	}
	c.sampleExecGauge()
	c.drainWake() // a park or unpark moves a child between the drain's lists
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

// errSlotClaimCancelled reports that the run's TERMINAL (terminateRun's
// releaseSlot, or onRolePark) cancelled a slot claim while its BLOCKING
// acquisition was still parked. The slot that eventually landed was handed
// straight back, and the caller must abandon the work it was acquiring for:
// the run it belongs to has already ended.
var errSlotClaimCancelled = errors.New("execution slot claim cancelled by the run's terminal")

// acquireRunSlot is the run-start BLOCKING execution-slot acquisition, done
// under the same claimSlotIntent/commitSlotClaim guard onTurnStarted and
// onRoleUnpark use. Its three callers (runChild, resumeChild, wakeChild) each
// used to do a bare c.slots.Acquire followed by an unconditional
// rt.slot = slotHeld, which is precisely the window releaseSlot's doc
// describes: a terminateRun landing while the acquirer is parked found
// slotFree, released nothing, and — factRunEnded being exactly-once — never
// ran again, so the slot the acquire went on to land was held forever by a
// run that had already ended. That is a PERMANENT cap shrink, and at the
// default cap of four such races deadlock the coordinator with no diagnostic.
// The window is widest exactly when the cap binds, which is when it matters.
//
// Returns nil when the caller may proceed (the claim landed a real slot, or
// rt already held/was claiming one and the occupancy is accounted for);
// the acquire's own error when the acquisition itself failed (baseCtx died,
// nothing was landed, nothing to give back); and errSlotClaimCancelled when
// the run terminated mid-wait — the landed slot has already been released
// here and the caller must simply return.
func (c *Coordinator) acquireRunSlot(rt *childRt) error {
	if !c.claimSlotIntent(rt) {
		return nil
	}
	if err := c.slots.Acquire(c.baseCtx, 1); err != nil {
		c.releaseSlotIntent(rt)
		return err
	}
	if !c.commitSlotClaim(rt) {
		c.slots.Release(1)
		return errSlotClaimCancelled
	}
	return nil
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
		c.slots.Release(1)
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
// actual semaphore acquisition (which can block for onRoleUnpark's caller,
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
// (TryAcquire or a blocking Acquire) has actually landed a real execution
// slot. If nothing cancelled the claim while the acquisition was
// in flight, the state becomes slotHeld — the ONLY state releaseSlot/
// onRolePark may release against. If a concurrent releaseSlot/onRolePark
// fired WHILE the acquisition was still pending (slotCancel), the claim
// instead reverts to slotFree and the caller MUST immediately give the
// just-landed slot back (c.slots.Release(1)) — it was rendered unwanted the
// moment it arrived, and nothing else will ever release it: the racing
// releaseSlot/onRolePark call already ran and deliberately did NOT call
// the release itself, because at that moment the slot was only
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
// pure bookkeeping, never a c.slots.Release(1) call. Never leave rt.slot
// reading slotHeld/slotClaimed when no execution slot is actually held
// (assertion (f), slot conservation, exists specifically to catch a
// regression here).
func (c *Coordinator) releaseSlotIntent(rt *childRt) {
	c.mu.Lock()
	rt.slot = slotFree
	rt.slotCancel = false
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
		c.rep.Warnf("agent run %s: journal terminal: %v", runID, err)
		return
	}
	if !won {
		return
	}
	c.sampleExecGauge() // the terminal is also a (possible) StateExecuting exit — see setState's sibling call
	c.audit("run_terminal", rec.Harp, map[string]string{"run_id": runID, "cause": cause})
	// Plane-2 request idempotency records are role-scoped and reconnect-
	// surviving (runchannel.go); drop this harp's at terminal so they don't
	// accumulate across the process's lifetime.
	c.clearReqTrack(rec.Harp)

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
	c.inbox.sever(rec.Harp, ErrRevoked)
	c.severChan(rec.Harp)

	// The synthesized terminal notice: the parent ALWAYS learns of a child
	// death. Kind distinguishes a launch failure (error) from a lifecycle
	// end (exited). The idle reaper is the exception — it is a NON-death,
	// EXPECTED terminal: the child's every turn was already reported, and
	// the next mail resumes the harp, so no notice is due.
	if rec.ParentHarp != "" && cause != CauseIdleReaped {
		kind, body := KindExited, fmt.Sprintf("agent %q (session %s) exited (%s)", rec.Agent, rec.Harp, cause)
		if cause == CauseLaunchFailed {
			kind, body = "error", fmt.Sprintf("agent %q (session %s) failed to launch: %s", rec.Agent, rec.Harp, detail)
		} else if detail != "" {
			body += ": " + detail
		}
		// A dead engine says WHY: the FAILED RunCompleted's reason
		// (captureRunFailure) — carrying the adapter's stderr tail — is
		// appended when the run failed and the
		// terminal cause did not already carry it. Without this a child that
		// died in its module loader reached the parent as a bare
		// "exited (runner-exit)", the 49-minute dead end. Not appended when
		// detail already IS this text (belt-and-suspenders against a future
		// path that threads it through detail too).
		if runFailure != "" && !strings.Contains(body, runFailure) {
			kind = "error"
			body += ": " + runFailure
		}
		if _, err := c.queueMail(rec.Harp, rec.ParentHarp, kind, body); err != nil {
			// The spool write is what just failed, so the invariant above
			// ("the parent ALWAYS learns of a child death") does not hold for
			// this death. Said loudly: there is nothing behind the file.
			c.rep.Warnf("agent %s: the terminal notice could not be written to parent %s's spool (%v) — the parent will not learn of this death", rec.Harp, rec.ParentHarp, err)
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
	// Last, so a drain that settles on this terminal settles after its
	// consequences (the parent's notice above included) have landed.
	c.drainWake()
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
		c.rep.Warnf("coordinator: reap ended runs: %v", err)
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
// exactly how an unbounded relaunch loop can outlive every stop issued
// against it.
func (c *Coordinator) resumeChild(harp, forRun string, attached chan struct{}, delay time.Duration) {
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
	if c.Draining() {
		// A relaunch is a fresh run, and a draining coordinator mints none —
		// this is the backstop for an attempt armed before the drain began
		// (relaunchForLeftoverMail and driveQueued refuse to arm one after).
		return
	}
	// THE CLAIM. An attempt is armed FOR the run that had ended (forRun) and
	// the mail queued behind it. "The harp is Ended" alone is not enough:
	// nextRelaunch can charge the leftover-mail tail a backoff long enough
	// for an explicit send to resume the harp AND for that newer run to end
	// too — at which point the harp is Ended again, but the mail this attempt
	// was armed for was answered in between. Launching then hands the engine
	// nothing and it idles forever. If a different run is current, the harp
	// has moved on and whatever is queued now belongs to that run's own
	// terminate tail (or the send that queued it).
	var rec RunRecord
	found := false
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil && r.Ended && r.RunID == forRun {
			rec = *r
			found = true
		}
	})
	if !found {
		return
	}
	plan, err := c.spawner.Resolve(lctx, rec.Agent)
	if err != nil {
		c.rep.Warnf("agent resume %s: %v", harp, err)
		if _, qerr := c.queueMail(harp, rec.ParentHarp, "error", fmt.Sprintf("agent %q (session %s) could not be resumed: %v", rec.Agent, harp, err)); qerr != nil {
			c.rep.Warnf("agent %s: queue resume failure: %v", harp, qerr)
		}
		return
	}
	// GAP 2 deferral: the ORIGINAL agent_run's workspace override is not
	// journaled on runEnqueued, so a resumed harp always falls back to
	// the spawner's normal resolution (per-call empty → project
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
	// Resolved BEFORE enqueue for the same reason AgentRun resolves it there:
	// the reach-back decides whether the resumed run is migrated, and that
	// class must be known from the moment the fresh run is addressable.
	url, uerr := c.spawnReachURL(harp, plan.Runtime)
	if uerr != nil {
		c.rep.Warnf("agent resume %s: %v", harp, uerr)
		if _, qerr := c.queueMail(harp, rec.ParentHarp, "error", fmt.Sprintf("agent %q (session %s) could not be resumed: %v", rec.Agent, harp, uerr)); qerr != nil {
			c.rep.Warnf("agent %s: queue resume failure: %v", harp, qerr)
		}
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
		c.rep.Warnf("agent resume %s: %v", harp, err)
		return
	}
	settled = true // rt now owns `attached`'s AND the launch context's lifecycle
	c.mu.Lock()
	rt.launchCancel = lcancel
	c.mu.Unlock()
	c.audit("agent_resume", rec.ParentHarp, map[string]string{"harp": harp, "run_id": rt.runID})

	// rt is already published (enqueueRun) at this point, so
	// onRolePark/onRoleUnpark/onTurnStarted/claimSlotIntent can all touch
	// rt.slot concurrently; acquireRunSlot owns the whole check-acquire-commit
	// under c.mu, exactly as runChild does.
	if err := c.acquireRunSlot(rt); err != nil {
		if !errors.Is(err, errSlotClaimCancelled) {
			c.failChild(rt, err)
		}
		return
	}
	c.setState(rt, StateExecuting)

	// resumeKeyFor is NOT used here: it reads the harp's CURRENT run, but
	// enqueueRun (above) already minted the fresh one for this very resume,
	// whose HarnessSessionID is necessarily still empty (not yet reported).
	// The key this resume must thread is the JUST-ENDED run's — exactly what
	// `rec` (captured before enqueueRun) already holds.
	resumeSessionID := rec.HarnessSessionID

	// Respawn via StartRun with the JOURNALED harness-native session id —
	// the engine continues its own recorded session; no transcript
	// re-priming needed. A prior run that never reported a session id falls
	// back to the rendered-history context prime, still over StartRun.
	// Queued mail is pushed as turns once the engine attaches
	// (runChildViaStartRun's drain).
	c.runChildViaStartRun(lctx, rt, "", token, url, SpawnStart{Resumed: true, ResumeKey: resumeSessionID})
}

// deliveryEndedDraining is driveQueued's observation for an ENDED recipient
// while the coordinator is draining: the §6a resume is refused (a resume is a
// fresh run, and a draining coordinator mints none), so the message stays
// queued. Not a fold state — the run is still StateEnded — but a distinct
// delivery outcome deliveryDisposition names instead of promising a resume
// that will not happen.
const deliveryEndedDraining = "ended-draining"

// observeRecipient reads the state a delivery to harp is judged by: the
// harp's current run and its fold state. It is read BEFORE the write, and the
// disposition the sender is told is THIS observation — a read after the write
// races the recipient itself, which may already have taken the file and
// started its turn, and would then describe an idle child it woke as
// "queued mid-turn".
func (c *Coordinator) observeRecipient(harp string) (state, runID string) {
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			state, runID = r.State, r.RunID
		}
	})
	return state, runID
}

// driveObserved acts on the state observeRecipient saw, once the message is
// on disk (§6a delivery-by-state): resume an ended child; an idle one is
// woken by the doorbell that rang at the write, and executing/parked/queued
// children reach the message at their own boundary. Returns the state the
// delivery is described by (deliveryEndedDraining for a resume refused under
// drain).
func (c *Coordinator) driveObserved(harp, state, runID string) string {
	switch state {
	case StateEnded:
		// Under drain a resume is new work, and the message is not lost by
		// refusing it: it is already queued, and the next run of this harp
		// finds it. The disposition must say so (deliveryEndedDraining)
		// rather than promise the resume below.
		if c.Draining() {
			return deliveryEndedDraining
		}
		// An EXPLICIT delivery (agent_send / inject) is a fresh ask from a
		// parent or an operator, so it lifts a prior agent_stop and resets
		// the consecutive-failure budget — the documented way a stopped or
		// given-up child comes back. Only the AUTOMATIC relaunch
		// (terminateRun's leftover-mail tail) is bounded; if this reset
		// leaked into that path the bound would not be a bound.
		c.clearLaunchGate(harp)
		attached := c.armLaunch(harp)
		c.goTracked(func() { c.resumeChild(harp, runID, attached, 0) })
	case StateIdle:
		// The doorbell that rang at the write wakes the runner; ITS driver
		// starts the new turn (§6a decided runner-side).
	}
	return state
}

// deliverMailID is the one delivery for mail whose sender is told what
// happened: observe the recipient, write the file (queueMailPayloadID), then
// act on the observation. The observed state is what deliveryDisposition
// classifies.
func (c *Coordinator) deliverMailID(msgID, from, to, kind, body string, structured json.RawMessage, inReplyTo string) (observed string, err error) {
	state, runID := c.observeRecipient(to)
	if _, err := c.queueMailPayloadID(msgID, from, to, kind, body, structured, inReplyTo); err != nil {
		return "", err
	}
	return c.driveObserved(to, state, runID), nil
}

// onRolePark ties recv parking to the execution-slot accounting (§6a slot
// yield): a child parked in agent_recv consumes no compute, so its slot is
// released while it waits. Roles without a child attachment (the parent
// session) park without slot bookkeeping.
//
// onRoleUnpark's re-acquisition can be a genuinely long BLOCKING
// wait (c.slots.Acquire). If onRolePark lands while rt is only
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
		c.slots.Release(1)
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
		if err := c.slots.Acquire(c.baseCtx, 1); err != nil {
			c.releaseSlotIntent(rt)
			return
		}
		if !c.commitSlotClaim(rt) {
			c.slots.Release(1)
			return
		}
	}
	c.setState(rt, StateExecuting)
}
