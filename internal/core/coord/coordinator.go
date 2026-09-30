package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	livenesspkg "github.com/ctxloom/ctxloom/internal/shared/liveness"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// ErrNotInjectable rejects a control action whose target the coordinator does
// not hold and cannot resume (an unknown harp, or a foreign process's session
// — there is no delivery channel into another process's terminal, by
// design).
var ErrNotInjectable = errors.New("inject: target is not a child this coordinator holds or can resume")

// ErrDraining is returned by every admission site (AgentRun, StartOwnedRun,
// RunnerHello for a runner with nothing already in flight, BindTransport)
// once BeginDrain has been called: the coordinator refuses new work, though
// already-admitted runs continue to completion. The wire adapter maps it to
// codes.Unavailable (coordgrpc.StatusFromErr) — the refusal is recoverable
// (retry against a coordinator that isn't draining), never a permanent
// failure.
var ErrDraining = errors.New("coordinator is draining: refusing new work (already-admitted runs continue to completion)")

// ErrClosed refuses a Serve that lands after Close has begun: the listeners
// it would have bound have no owner left to close them.
var ErrClosed = errors.New("coordinator is closed")

// Delivery modes a steer reports back (SteerOutcome.Delivery): which §6a
// delivery-by-state rule the coordinator applied to the instruction.
const (
	DeliveryNewTurn = "new-turn" // woke an idle child into a new turn
	DeliveryQueued  = "queued"   // queued for the child's next turn boundary
	DeliveryResumed = "resumed"  // relaunched an ended session, the text as its next turn
)

// Options configures a Coordinator.
type Options struct {
	// ProjectDir is the project working directory the coordinator serves.
	ProjectDir string
	// ProjectID is the project's resolved id (the project registry's, the
	// one CTXLOOM_PROJECT_ID carries). It is the Project of every identity
	// this coordinator mints and keys the durable state dir. "" means it did
	// not resolve: identities carry no project id, and the state dir falls
	// back to a path-derived key — which is a directory NAME, not a project
	// id, and never stands in for one.
	ProjectID string
	// StateDir overrides the state dir entirely (tests).
	StateDir string
	// Spawner is the launch seam: adapters/spawn in production, composed at
	// cmd/*; a fake in tests. Required.
	Spawner Spawner
	// Host is the application service every host-relayed tool is dispatched
	// to (Verbs.Host), under the caller's identity. Nil refuses every relayed
	// tool (ErrNoHostApp).
	Host HostApp
	// Reporter receives every diagnostic this coordinator, its spawner and
	// its spool couriers raise — the composition root chooses the sink (the
	// terminal renderer in production, a report.Collector in tests). Nil
	// discards. Long-lived goroutines report to the Reporter they were
	// constructed with, never to a process-wide channel.
	Reporter report.Sink
	// Clock overrides command time (tests). Nil = time.Now.
	Clock func() time.Time
	// ConcurrencyCap overrides the number of concurrently EXECUTING child
	// turns the coordinator admits (Coordinator.slots' cap). <= 0 keeps the package
	// default (agentConcurrencyCap, children.go). This is a RESOURCE
	// ceiling — it bounds how many live engine processes run at once, not
	// how many turns a run may take — and is not a correctness gate: the
	// coordinator's own state is safe under concurrency by construction
	// (partitioned by child identity). Production sources this from
	// coordinator config (config.Config.GetDelegationConcurrency); tests
	// raise it directly to exercise real overlap. Renamed from TurnCap.
	ConcurrencyCap int
	// Depth overrides the maximum nesting depth of the delegation tree
	// (children.go's AgentRun guard and the runner-side leaf computation
	// both read it). <= 0 keeps the package default (agentDepthCap,
	// children.go). Unlike ConcurrencyCap this IS a correctness setting —
	// see agentDepthCap's doc for what raising it changes. Production
	// sources this from coordinator config (config.Config.GetDelegationDepth).
	Depth int
	// EndedRunTail bounds how many ENDED, non-current run records the live
	// folds retain across all harps — the one-shot retention reap (Slice 4 /
	// Fork 2.3). One-shot mints one ended run per turn per harp, so without a
	// bound the in-memory run/state maps grow unbounded over a long session.
	// Every harp's CURRENT run is ALWAYS kept, outside this
	// count. <= 0 keeps the package default (defaultEndedRunTail). Tests set a
	// tiny value to exercise reaping; production keeps the default.
	EndedRunTail int
	// EndedRunMaxAge reaps an ended, non-current run once it is older than
	// this, independent of EndedRunTail. <= 0 keeps the package default
	// (defaultEndedRunMaxAge).
	EndedRunMaxAge time.Duration
	// IdleTimeout is delegation.idle_timeout: the idle reaper ends a run
	// whose runner has had no turn for this long. <= 0 keeps the package
	// default (defaultIdleTimeout). Production sources it from coordinator
	// config (config.Config.GetDelegationIdleTimeout).
	IdleTimeout time.Duration
	// RunnerAwaitTimeout overrides how long issueStartRun waits for a
	// just-spawned runner to dial home before declaring the launch attempt
	// failed (children.go's dial-home barrier, awaitRunner). <= 0 keeps the
	// package default (defaultRunnerAwaitTimeout). Production leaves this
	// unset; tests lower it to exercise the timeout path without waiting out
	// the real budget, or raise/lower it to prove a slow-but-successful
	// dial-home survives (or doesn't) at a given budget.
	RunnerAwaitTimeout time.Duration
	// OwnerHarp is the SESSION OWNER's harp: the one recipient whose inbox is
	// drained IN THIS PROCESS (AgentRecv) rather than by a runner. The owner
	// is a spool recipient like any migrated child, and it is identified by
	// this declaration alone — never by holding a run record, because a
	// host/stdio owner has none. Required: a coordinator that did not know
	// whose inbox it drains would write every child->parent message into a
	// directory nothing reads.
	OwnerHarp string
	// OwnerMode is how the session owner runs, stamped beside the owner lock
	// for the next claimant: only an INTERACTIVE owner can be proven an
	// abandoned orphan and reclaimed (claimOwner). The zero value is never
	// reclaimed.
	OwnerMode OwnerMode
	// Mapper resolves spool references to paths — the ONE mapper every spool
	// read and write this coordinator performs goes through. Nil is the
	// home-relative mapper (spool.NewHomeMapper).
	Mapper spool.PathMapper
	// SpoolSweepInterval overrides the spool reconciliation cadence (0 = the
	// built-in spoolSweepInterval). Exposed for tests, which must be able to
	// prove that a DROPPED doorbell is still delivered by the sweep without
	// waiting out the production interval.
	SpoolSweepInterval time.Duration
}

// Coordinator is the runtime coordinator: durable CQRS stores + credential
// registry + the delegation orchestration, served over gRPC (RunnerChannel)
// and streamable-HTTP MCP by the listeners in httpserver.go.
type Coordinator struct {
	// rep is the Reporter the composition root handed in (Options.Reporter);
	// every diagnostic this coordinator and the types it builds raise goes
	// through it.
	rep        report.Reporter
	projectDir string
	projectID  string
	stateDir   string
	now        func() time.Time

	baseCtx context.Context
	cancel  context.CancelFunc

	releaseOwner func()

	runs     *Store
	runsF    *runsFold
	queueF   *queueFold
	rosterF  *rosterFold
	reportsF *reportsFold
	items    *Store
	itemsF   *itemsFold
	auditJ   *Store
	// artifacts (E1b) is the content-addressed blob store backing
	// ArtifactTransferService — NOT a journal (see artifactstore.go for why
	// it needs no single-writer serialization); it lives alongside the
	// journals in the same per-project state dir.
	artifacts *artifactStore

	spawner Spawner
	// slots is the execution-slot cap: at most concurrencyCap child turns may
	// be EXECUTING at once, one token each. Acquisition is FIFO (a waiter
	// parked on a full semaphore is served before a later TryAcquire), so
	// enqueued children start in spawn order. It is a runtime scheduling
	// PRIMITIVE — the authoritative queue is the queueFold; waiters are
	// rebuilt from it on restart (adoption). Release PANICS on an
	// over-release rather than handing back a token nobody took: a silently
	// inflated cap admits more live engine processes than configured and is
	// invisible until the box runs out of memory, so the crash is the wanted
	// behaviour (see slotState's doc for the two defects being guarded).
	slots *semaphore.Weighted
	// depthCap is the resolved maximum nesting depth of the delegation tree
	// (Options.Depth, <= 0 falls back to agentDepthCap) — AgentRun's "may
	// this run spawn" guard reads it per call, unlike concurrencyCap (which
	// is consumed once into slots at construction and needs no persisted
	// field).
	depthCap int
	// spawnNoticeAfter is how long AgentRun's pre-registration span may run
	// before notePendingSpawn reports it (defaultSpawnNoticeAfter,
	// children.go). A field rather than a constant so a test can shrink it;
	// no Options field, because it is a diagnostic threshold, not a
	// behaviour an embedder chooses.
	spawnNoticeAfter time.Duration
	// endedRunTail / endedRunMaxAge are the one-shot retention reap bounds
	// (Slice 4 / Fork 2.3) — see Options.EndedRunTail/EndedRunMaxAge.
	endedRunTail   int
	endedRunMaxAge time.Duration
	// runnerAwaitTimeout is issueStartRun's dial-home budget — see
	// Options.RunnerAwaitTimeout / defaultRunnerAwaitTimeout (children.go).
	runnerAwaitTimeout time.Duration
	// idleTimeout is the idle reaper's bound — see Options.IdleTimeout.
	idleTimeout time.Duration
	// maxLaunchAttempts / launchBackoffBase / launchBackoffMax are the
	// launch-retry budget (launchgate.go) resolved ONCE here at
	// construction — the built-in defaultMaxLaunchAttempts/
	// defaultLaunchBackoffBase/defaultLaunchBackoffMax, or an operator's
	// EnvLaunchMaxAttempts/EnvLaunchBackoffBase/EnvLaunchBackoffMax
	// override (resolveLaunchTunables). Every per-attempt read
	// (launchBackoff, nextRelaunch, giveUpLaunching) consults these fields,
	// never the environment directly, so the retry loop's hot path is a
	// plain field read, not a per-attempt env lookup.
	maxLaunchAttempts int
	launchBackoffBase time.Duration
	launchBackoffMax  time.Duration
	// watch is the D1 consumer broadcast hub: every AgentEvent processed on
	// any RunChannel is teed here, live, for ConsumerService.WatchRuns
	// subscribers (consumer.go). D2 retired the legacy per-harp agentbus
	// TapHub this superseded — watch is now the ONLY live-tap mechanism.
	watch *watchHub
	// consumerCreds is the D1 read-only credential class (consumer.go):
	// minted fresh per process at Serve(), never journaled.
	consumerCreds *consumerCreds
	// host serves the Host verb; composed at New, never re-set.
	host HostApp
	// spoolDoorbell counts what the spool doorbell deliberately does not
	// retry (spooldoorbell.go). Atomics, not mu-guarded: a counter that
	// needed the coordinator lock would put contention on the exact path
	// whose whole point is to cost nothing when it fails.
	spoolDoorbell SpoolDoorbellCounters
	// ownerHarp is Options.OwnerHarp: the recipient class "the owner, drained
	// in-process" (spoolDeliverTo). Read-only after New.
	ownerHarp string
	// mapper is Options.Mapper: the one spool path mapper. Read-only after New.
	mapper spool.PathMapper
	// streams holds the RunnerChannel/RunChannel handlers in flight. Their
	// deferred teardown is where a dropped runner becomes a terminal
	// (runnerLost -> terminateRun -> the session index, the parent's notice),
	// and grpc.Server.Stop returns without joining it — so Close seals this
	// group first (a handler arriving later is refused at enter) and joins
	// it after the server is down, or a shutdown races its own last
	// terminals against whatever removes the state dir next.
	streams TrackedGroup
	// spoolIn lends the per-child in/ writers. The writers themselves are
	// lazy (one per child, on its first message), so a run that never sends
	// never gets a spool directory.
	spoolIn *SpoolWriterCache
	// spoolReactor serialises the coordinator's own spool reading (out/ and
	// in/consumed sweeps) — see its type.
	spoolReactor *SpoolReactor
	// spoolSweepInterval overrides the reconciliation cadence
	// (Options.SpoolSweepInterval; 0 = spoolSweepInterval). Test seam: a
	// missed-doorbell test has to prove the sweep RECOVERS delivery, and the
	// only honest way to do that is to let the sweep actually run.
	spoolSweepInterval time.Duration
	spoolDeliveryCount SpoolDeliveryCounters
	// spoolSeen remembers which in/consumed entries have already been credited
	// as progress, per role. consumed/ is an audit trail nothing prunes yet, so
	// without this every sweep would re-credit the whole history.
	spoolSeenMu sync.Mutex
	spoolSeen   map[string]map[string]bool

	mu     sync.Mutex
	attach map[string]*childRt // runID → runtime attachment
	byHarp map[string]*childRt // harp → current attachment
	// inbox is the owner's ONE inbox: the parked receive and the spool
	// reader behind agent_recv (spoolinbox.go).
	inbox   *spoolInbox
	runners map[string]*RunnerSession // credHash → connected runner
	// graceExpire holds the runner-loss grace windows adopt armed for the
	// runs it found live at startup, keyed by run id, until each fires. A key
	// with no attachment is a run whose runner has not dialed back: the
	// shutdown drain has no process to wait on for it (BeginDrain). Firing
	// them all ahead of their clock is the test seam expireRunnerGrace; each
	// closure is idempotent with its timer.
	graceExpire map[string]func()
	runnerReady map[string]chan struct{} // credHash → closed on Hello registration (awaitRunner)
	chans       map[string]*RunChannel   // role harp → live RunChannel
	// reqTrack is plane-2 request idempotency that SURVIVES a RunChannel
	// reconnect, keyed by (role, request_id). It replaces the per-connection
	// RunChannel.reqCache/inflight (reset to empty on every dial): a request the
	// runner reissues with the same request_id on a fresh channel (home.go)
	// must reuse the in-flight dispatch, never start a second one — a
	// duplicate dispatch would run the request's effect twice, and the
	// answer to one copy would be lost with the channel it arrived on.
	// Cleaned per-role at terminal (clearReqTrack); lazily initialized.
	reqTrack map[reqKey]*inflightReq
	// asks holds the outstanding correlated asks (spoolcontrol.go) — question
	// and summarize — keyed by the id their request file carries as origin_id,
	// which is what a reply quotes in in_reply_to. Registered BEFORE the file
	// is published, so that an answer arriving the instant the file becomes
	// observable still resolves instead of racing its own registration.
	// Lazily initialized.
	asks map[string]*pendingAsk
	// pendingStops holds, per run id, the terminal detail of an agent_stop
	// whose runner is closing the run: the runner's own RunExited can reach
	// RunnerExited before the stop's terminal, and must be recorded as the
	// stop it is. Guarded by mu.
	pendingStops map[string]string
	// pausedRuns holds the run ids this coordinator's ControlPause holds at
	// their runner's gate, keyed by run id so a relaunch (a new run) is never
	// described as paused. Guarded by mu; lazily initialized.
	pausedRuns map[string]struct{}
	// onAskPublished, when set, is called by controlAsk between REGISTERING the
	// waiter and PUBLISHING the ask — the register-before-publish test seam,
	// It fires on that side
	// of the publish deliberately: a hook fired after it cannot distinguish a
	// correct implementation from one that registers between the write and the
	// hook, since both have registered by then. Nil in production.
	onAskPublished func(askID string)
	// afterMailWritten, when set, is called by the coordinator's mail courier
	// once a message is on disk and rung, before the sender learns its
	// disposition — the seam a test uses to make the recipient act on the
	// file at exactly that moment. Nil in production. Guarded by mu: the
	// courier runs on the spool reactor's goroutine too.
	afterMailWritten func(to string)
	// afterRecipientObserved, when set, is called by deliverMailID between
	// observing the recipient and writing the file — the seam a test uses to
	// land the recipient's terminal inside that window. Nil in production.
	// Guarded by mu.
	afterRecipientObserved func(to string)
	// spoolHandler is THE consumer for validated inbound spool doorbells
	// (SetSpoolDoorbellHandler), registered by startSpoolReactor whenever
	// delivery is on. Nil only when delivery is off, and then an arriving
	// doorbell is validated, counted and dropped.
	spoolHandler SpoolDoorbellHandler
	// launchArmed pre-registers the "attached" signal for a harp's NEXT
	// (re)launch attempt(s), synchronously, before the caller dispatches the
	// async runChild/resumeChild goroutine (see armLaunch, children.go).
	// This is the flaky-agentcoord S3 seam: a caller synchronously after the
	// dispatch (a test, via awaitChildUp) can look the current attempt(s)
	// up race-free instead of polling a wall-clock Eventually over the
	// pipeline it fronts. A slice, not a single channel: two dispatchers can
	// race to arm the SAME harp (armLaunch's doc), and awaitChildUp must be
	// able to wait on every not-yet-settled attempt, not just the latest.
	launchArmed map[string][]chan struct{}
	// launches is per-harp launch/retry bookkeeping (launchgate.go): the
	// cancel for the attempt currently in flight, the consecutive-failure
	// count the bounded retry reads, and the agent_stop flag that stops an
	// already-armed relaunch from carrying on behind the stop. Lazily
	// initialized (launchGateLocked).
	launches map[string]*launchState
	// liveness is the PROGRESS monitor (liveness.go) — lazily built, one per
	// coordinator because it retains per-harp CPU samples between polls (a
	// single absolute CPU reading says nothing; only the difference does). It
	// REPORTS ONLY: nothing it produces terminates, cancels, or reaps a run.
	liveness *livenesspkg.Monitor

	// tracked owns every goroutine this coordinator dispatches beyond its
	// spawning call's own return (delegation launches/resumes, the runner
	// watchdog, adopt's runner-loss grace timers, and the RunChannel/
	// RunnerChannel pumps). Close() joins it BEFORE closing the journals —
	// see trackedGroup for why an unjoined goroutine racing that teardown is
	// this package's worst flake class.
	tracked TrackedGroup

	// transport is the bound wire (Transport), nil until the adapter serves.
	// Its own lock, not mu: BindTransport publishes it while the spawn path
	// (ReachURL, building a child's env) and Close read it from other
	// goroutines, and those readers must not have to take the coordinator's
	// big lock.
	transportMu sync.Mutex
	transport   Transport

	// admissionClosed is the application-layer DRAIN flag (task
	// definite-phoniness): BeginDrain sets it once, and every admission
	// site (AgentRun, StartOwnedRun, RunnerChannel's Hello for a runner
	// with nothing already in flight, Serve) checks it and returns
	// ErrDraining instead of admitting new work. It does NOT touch
	// c.baseCtx, the transport, or any live attachment — an already-admitted run
	// keeps running exactly as it would without a drain in progress — and
	// it is deliberately not consulted by Close(), which is the hard,
	// immediate teardown (see its own doc): the caller flips this first,
	// waits for nothing to be left in flight (Roster/WatchRuns), and only
	// then calls Close(). ATOMIC, not mu-guarded: every admission site must
	// be able to check it without taking c.mu.
	admissionClosed atomic.Bool
	// drainBound is how long BeginDrain waits on a child's PROCESS before
	// forcing it — resolved at construction (tunables.drainBound) from
	// agent_recv's own maximum wait, RecvWaitMax, which is the one
	// declaration of that number. A field so a test can shrink it; no
	// Options field, because the bound is the policy, not a knob.
	drainBound time.Duration
	// drain is the SHUTDOWN drain BeginDrain started, nil until then; drains
	// is every drain still running — that one and any bulk agent_stop sweep
	// (StopChildren) — which is the set drainWake pokes. Both guarded by
	// drainMu (not c.mu: BeginDrain must not need the big lock, and the
	// drain runner takes c.mu itself).
	drainMu sync.Mutex
	drain   *Drain
	drains  map[*Drain]struct{}

	// execGaugeHook, if set (tests only), is sampled synchronously every time
	// a run's §6a state durably transitions (setState, terminateRun): it
	// receives the CURRENT count of runs in StateExecuting, read off
	// queueFold.executing — the same idempotent counter (folds.go's
	// transition: entering StateExecuting increments exactly once per real
	// transition, leaving it decrements exactly once) the coordinator's own
	// admission logic trusts, not a duplicate a test could disagree with.
	// This is the concurrency-gauge seam for the invariant test
	// (turncap_concurrent_test.go) to observe true peak overlap. Nil in
	// production (zero cost).
	execGaugeHook func(executing int)
	// drainHook, if set (tests only, same package), runs synchronously at
	// the start of drainTerminalTail's wait (runchannel.go) — the
	// deterministic seam for reproducing the terminal-tail drop race
	// (a RunCompleted item flushed exactly during this window
	// must survive to the items fold) without depending on real scheduler
	// timing.
	drainHook func(role string)
	// stopCancelledLaunchHook, if set (tests only, same package), runs
	// synchronously in stopRun right after it cancels the harp's launch in
	// flight: the seam that lets the cancelled launch settle (fail) before
	// anything else the stop does, which is the interleaving a stop landing
	// mid-StartRun meets under load.
	stopCancelledLaunchHook func(harp string)
	// spawnDispatchedHook, if set (tests only, same package), runs
	// synchronously in AgentRun immediately after the child's driver
	// goroutine has been dispatched, with the child's harp. It is the
	// deterministic seam for the disposition-truthfulness test: the point of
	// the test is that the dispatched goroutine can reach its terminal — and
	// so give back the slot the enqueue claimed — before agent_run composes
	// its answer, and parking here is what makes that ordering a fact rather
	// than a scheduler coin flip. Nil in production (zero cost).
	spawnDispatchedHook func(harp string)
	// drainRequestHook, if set (tests only, same package), runs
	// synchronously in runDrain's REQUEST phase between classifying a run as
	// executing and marking it for exit (requestExit). It is the seam that
	// lets a test land the child's turn boundary inside that window, which
	// is the ordering the WAIT loop's idle arm exists to survive. Nil in
	// production (zero cost).
	drainRequestHook func(runID string)
	// stopAnsweredHook, if set (tests only, same package), runs synchronously
	// in stopRun once the runner has answered its StopRun and BEFORE the
	// stop's own terminal: the seam that lets the runner's RunExited land
	// first, the interleaving pendingStops exists for. Nil in production.
	stopAnsweredHook func(runID string)
	// attachRunHook, if set (tests only, same package), runs synchronously in
	// AttachRun once the Hello is verified and BEFORE the channel is
	// registered in c.chans. The run channel dials on its own goroutine,
	// unordered against the runner channel StartRun rides, so a child can be
	// "up" with its run channel not yet attached; parking here makes that
	// window a fact a test can hold open. Nil in production.
	attachRunHook func(harp string)

	closeOnce sync.Once
	// closed is set at the START of Close, before it looks for listeners to
	// take down. Serve checks it after publishing its own: whichever of the
	// two ran second sees the other's mark, so a Serve racing a Close can
	// never leave a bound listener nothing will close.
	closed atomic.Bool
}

// New opens (or adopts) the project's coordinator state and starts the
// orchestration core. Listeners come up separately via Serve (httpserver.go)
// so tests can run the core without ports.
func New(opts Options) (*Coordinator, error) {
	// Refused before any state exists: an options contradiction, not a
	// standup failure.
	if opts.OwnerHarp == "" {
		return nil, ErrNeedsOwner
	}
	claim, err := acquireStateDir(opts)
	if err != nil {
		return nil, err
	}
	t := resolveTunables(opts)

	rep := report.To(opts.Reporter)
	mapper := opts.Mapper
	if mapper == nil {
		mapper = spool.NewHomeMapper()
	}
	c := &Coordinator{
		rep:                rep,
		tracked:            TrackedGroup{rep: rep},
		streams:            TrackedGroup{rep: rep},
		projectDir:         opts.ProjectDir,
		projectID:          opts.ProjectID,
		stateDir:           claim.dir,
		now:                t.now,
		releaseOwner:       claim.release,
		spawner:            opts.Spawner,
		host:               opts.Host,
		slots:              semaphore.NewWeighted(int64(t.concurrencyCap)),
		depthCap:           t.depthCap,
		spawnNoticeAfter:   defaultSpawnNoticeAfter,
		endedRunTail:       t.endedRunTail,
		endedRunMaxAge:     t.endedRunMaxAge,
		runnerAwaitTimeout: t.runnerAwaitTimeout,
		idleTimeout:        t.idleTimeout,
		maxLaunchAttempts:  t.maxLaunchAttempts,
		launchBackoffBase:  t.launchBackoffBase,
		launchBackoffMax:   t.launchBackoffMax,
		drainBound:         t.drainBound,
		drains:             make(map[*Drain]struct{}),
		watch:              newWatchHub(report.To(opts.Reporter)),
		consumerCreds:      &consumerCreds{},
		attach:             make(map[string]*childRt),
		graceExpire:        make(map[string]func()),
		byHarp:             make(map[string]*childRt),
		runners:            make(map[string]*RunnerSession),
		runnerReady:        make(map[string]chan struct{}),
		chans:              make(map[string]*RunChannel),
		launchArmed:        make(map[string][]chan struct{}),
		launches:           make(map[string]*launchState),
		ownerHarp:          opts.OwnerHarp,
		mapper:             mapper,
		spoolSweepInterval: opts.SpoolSweepInterval,
		spoolIn:            NewSpoolWriterCache(mapper, spool.DirIn, spoolWriterIDCoordinator),
	}
	c.inbox = newSpoolInbox(rep, mapper, &c.spoolDeliveryCount, c.onRolePark, c.onRoleUnpark, c.liveRun)
	c.baseCtx, c.cancel = context.WithCancel(context.Background())
	if c.spawner == nil {
		return nil, c.abortNew(errors.New("coord: Options.Spawner is required (adapters/spawn, composed at cmd/*)"))
	}
	if err := c.openJournals(); err != nil {
		return nil, c.abortNew(err)
	}

	c.adopt()
	// THE STARTUP SWEEP IS A FIRST-CLASS DELIVERY PATH, not doorbell-miss
	// recovery: a coordinator coming up cold drains every known run's spool
	// before any channel exists to ring it. It starts after adopt() because
	// adopt is what makes the run records readable, and the sweep enumerates
	// runs.
	c.startSpoolReactor()
	c.goTracked(c.runnerWatchdog)
	c.goTracked(c.idleReaper)
	// The PROGRESS watchdog (liveness.go), alongside the runner-liveness one
	// above. They answer different questions and neither subsumes the other:
	// runnerWatchdog catches a runtime that DIED (heartbeat silence) and acts
	// on it; this one catches a runtime that is very much alive and making no
	// progress, and only ever warns.
	c.goTracked(c.livenessWatchdog)
	return c, nil
}

// abortNew unwinds a coordinator New never returned: cancel the base context,
// release the journals and the owner lock (closePartial). Returns err
// unchanged so every failure path reads `return nil, c.abortNew(err)`.
func (c *Coordinator) abortNew(err error) error {
	c.cancel()
	c.closePartial()
	return err
}

// tunables is New's resolved configuration: every Options fallback applied
// ONCE, at construction, so no hot path re-derives one.
type tunables struct {
	now                func() time.Time
	concurrencyCap     int
	depthCap           int
	endedRunTail       int
	endedRunMaxAge     time.Duration
	runnerAwaitTimeout time.Duration
	idleTimeout        time.Duration
	maxLaunchAttempts  int
	launchBackoffBase  time.Duration
	launchBackoffMax   time.Duration
	drainBound         time.Duration
}

// resolveTunables applies each Options field's documented fallback.
func resolveTunables(opts Options) tunables {
	t := tunables{
		now:                opts.Clock,
		concurrencyCap:     opts.ConcurrencyCap,
		depthCap:           opts.Depth,
		endedRunTail:       opts.EndedRunTail,
		endedRunMaxAge:     opts.EndedRunMaxAge,
		runnerAwaitTimeout: opts.RunnerAwaitTimeout,
		idleTimeout:        opts.IdleTimeout,
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.concurrencyCap <= 0 {
		t.concurrencyCap = agentConcurrencyCap
	}
	if t.depthCap <= 0 {
		t.depthCap = agentDepthCap
	}
	if t.endedRunTail <= 0 {
		t.endedRunTail = defaultEndedRunTail
	}
	if t.endedRunMaxAge <= 0 {
		t.endedRunMaxAge = defaultEndedRunMaxAge
	}
	if t.runnerAwaitTimeout <= 0 {
		t.runnerAwaitTimeout = defaultRunnerAwaitTimeout
	}
	if t.idleTimeout <= 0 {
		t.idleTimeout = defaultIdleTimeout
	}
	// The launch-retry budget has no Options field (deliberately — it is an
	// operator/env tunable, not a per-call test seam): resolved once, here,
	// from the environment (resolveLaunchTunables), never per attempt.
	t.maxLaunchAttempts, t.launchBackoffBase, t.launchBackoffMax = resolveLaunchTunables(report.To(opts.Reporter))
	// ONE POLICY, TWO BOUNDS: a wait on a process is bounded at the longest
	// wait agent_recv itself will park for; a wait on a human (a parked
	// child) is not bounded at all. The drain bound is therefore agent_recv's
	// maximum, read by name — never a second number that could drift from
	// it.
	t.drainBound = RecvWaitMax
	return t
}

// stateDirClaim is an acquired state dir plus its release, which frees the
// exclusive-owner lock (nil when there is none).
type stateDirClaim struct {
	dir     string
	release func()
}

// acquireStateDir resolves and claims the coordinator's state dir: an explicit
// Options.StateDir verbatim (tests), otherwise the project's durable dir under
// its exclusive-owner lock. A project has ONE coordinator: when another live
// session-owning process holds the lock, this one is refused (ErrStateOwned)
// rather than run on state of its own — a second coordinator on the same
// project is the rival-coordinator class the owner lock exists to make
// impossible, and its cost (a doubled spool reactor over one owner inbox,
// consume races on the owner's mail) is paid by the session that DID win.
func acquireStateDir(opts Options) (stateDirClaim, error) {
	if opts.StateDir != "" {
		return stateDirClaim{dir: opts.StateDir}, nil
	}
	dir, err := stateDirForProject(projectKey(opts.ProjectID, opts.ProjectDir))
	if err != nil {
		return stateDirClaim{}, err
	}
	release, err := claimOwner(report.To(opts.Reporter), dir, newOwnerStamp(opts.OwnerHarp, opts.OwnerMode))
	if err != nil {
		return stateDirClaim{}, err
	}
	return stateDirClaim{dir: dir, release: release}, nil
}

// openJournals builds the folds and opens every journal (plus the artifact
// blob store) in the claimed state dir. On failure the caller aborts; each
// store opened so far is closed by closePartial.
func (c *Coordinator) openJournals() error {
	c.runsF, c.queueF, c.rosterF, c.reportsF = newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold(c.rep)
	runs, err := openStore(filepath.Join(c.stateDir, "runs.jsonl"), c.runsF, c.queueF, c.rosterF, c.reportsF)
	if err != nil {
		return err
	}
	c.runs = runs
	c.itemsF = newItemsFold()
	// D4 CHECKPOINT compaction: a prior snapshot (if one exists — the
	// common case is none, a fresh project) seeds the fold and replay
	// starts at its offset instead of byte 0 — openStoreFromOffset falls
	// back to a full replay by itself if the offset is stale (journal.go).
	itemsOffset := int64(0)
	if snap, ok := loadItemsSnapshot(c.rep, c.stateDir); ok {
		c.itemsF.restore(snap)
		itemsOffset = snap.Offset
	}
	items, err := openStoreFromOffset(filepath.Join(c.stateDir, "items.jsonl"), itemsOffset, c.itemsF)
	if err != nil {
		return err
	}
	c.items = items
	auditJ, err := openStore(filepath.Join(c.stateDir, "interactions.jsonl"))
	if err != nil {
		return err
	}
	c.auditJ = auditJ
	artifacts, err := newArtifactStore(c.stateDir)
	if err != nil {
		return err
	}
	c.artifacts = artifacts
	return nil
}

// goTracked runs fn on a new goroutine Close() joins (waitTracked) before
// tearing the journals and state dir down. EVERY bare `go` in this package whose
// goroutine can outlive its spawning call must ride its owner's equivalent — see
// trackedGroup.
func (c *Coordinator) goTracked(fn func()) { c.tracked.Dispatch(fn) }

// every runs sweep on a ticker until the coordinator's base context ends —
// the one loop shape under the runner watchdog and the idle reaper.
func (c *Coordinator) every(interval time.Duration, sweep func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			sweep()
		case <-c.baseCtx.Done():
			return
		}
	}
}

// closeJoinBudget bounds Close's wait for tracked goroutines: generous headroom
// above the ctx-aware waits every tracked loop selects on (slot acquisition,
// runner awaits, request round-trips all key off c.baseCtx, already cancelled by
// the time waitTracked runs), short enough that one wedged handler cannot hang
// shutdown forever. The most generous of the package's four, because the
// journals' close waits behind it.
const closeJoinBudget = 5 * time.Second

// waitTracked joins every c.goTracked goroutine, with a bounded escape.
func (c *Coordinator) waitTracked() {
	c.tracked.Wait(closeJoinBudget, "coordinator close", "a leaked goroutine may still touch the state dir")
}

// adopt reconciles state read from disk with the fresh process: queued mail
// is preserved as-is (drainable); every non-ended run gets ONE runner-loss
// grace window in which its runner — a container's foreground process, or a
// host runner that is its own session leader — may dial back naming the run
// (RunnerHello.active_run_ids), whereupon readopt gives it its attachment
// and its cell owner back; a run whose runner never returns ends as runner
// loss. Live-child engine-stream continuity is Wave C.
func (c *Coordinator) adopt() {
	type pending struct {
		runID    string
		credHash string
	}
	var stale []pending
	c.runs.View(func() {
		for id, r := range c.runsF.runs {
			if r.Ended {
				continue
			}
			stale = append(stale, pending{runID: id, credHash: r.CredHash})
		}
	})
	for _, p := range stale {
		runID, credHash := p.runID, p.credHash
		fired := make(chan struct{})
		var once sync.Once
		fire := func() {
			once.Do(func() {
				close(fired)
				c.mu.Lock()
				delete(c.graceExpire, runID)
				_, connected := c.runners[credHash]
				c.mu.Unlock()
				if !connected {
					c.terminateRun(runID, CauseRunnerLoss, "no runner re-Hello after coordinator relaunch")
				}
			})
		}
		c.mu.Lock()
		c.graceExpire[runID] = fire
		c.mu.Unlock()
		c.goTracked(func() {
			select {
			case <-time.After(runnerLossTimeout):
				fire()
			case <-fired:
			case <-c.baseCtx.Done():
			}
		})
	}
}

// BeginDrain flips the coordinator into the application-layer DRAINING
// state and starts the BOUNDED drain of its children, returning the handle
// that settles when the drain has.
//
// Admission closes first: every admission site (AgentRun, StartOwnedRun,
// RunnerChannel's Hello for a runner with nothing already in flight, Serve)
// starts returning ErrDraining instead of admitting new work, and the
// relaunch paths (launchgate.go, driveQueued) stop resuming ended children.
// This is a one-way, idempotent flip — no corresponding "undrain": nothing in
// this codebase resumes accepting work after announcing it will not — and a
// second call returns the drain already in progress.
//
// Then ONE POLICY, TWO BOUNDS (see runDrain): every live child is either a
// PROCESS wait, bounded at c.drainBound — exit REQUESTED now (no new turn is
// handed out; the child ends at its next turn boundary, or immediately when
// it is between turns or never started) and FORCED when the bound elapses —
// or a PARK on a human (StateParked), which is not waited on at all: the
// child keeps its turn, its slot yield and its session lock, and the outcome
// lists it so the park cannot be forgotten.
//
// This is deliberately an APPLICATION-layer drain, not a transport-level
// one: the wire adapter's Transport.Close explains why grpc-go's
// GracefulStop cannot be used on this server — its only transport (h2c via ServeHTTP) wraps
// every connection in a serverHandlerTransport whose Drain() is an
// unconditional panic, and RunChannel/RunnerChannel are perpetual streams
// a transport-level drain would never resolve against even without that
// panic. BeginDrain instead closes admission here, at the verbs that mint
// new work, and leaves the transport alone. Close() is still the caller's:
// it is the hard teardown, and a caller that holds parked children decides
// for itself whether the morning is worth waiting for.
func (c *Coordinator) BeginDrain() *Drain {
	c.admissionClosed.Store(true)
	c.drainMu.Lock()
	d := c.drain
	fresh := d == nil
	if fresh {
		// Which children the drain accounts for is decided HERE, before this
		// returns: a caller that ends a run right after BeginDrain must find
		// it in the outcome, not lose it to a snapshot that ran later.
		d = newDrain(shutdownPolicy(), c.drainTracked(c.hasProcess()))
		c.drain = d
	}
	c.drainMu.Unlock()
	if fresh {
		c.startDrain(d)
	}
	return d
}

// hasProcess is the shutdown drain's selection: every live run except one
// adopted at startup whose runner has not dialed back. That run has no
// process here to ask to exit and none that could reach a turn boundary, so
// waiting on it would only wait out its adoption grace — the whole
// runnerLossTimeout, on every exit of a session that ends inside it. It is
// left as adoption found it; the next coordinator's grace decides it.
func (c *Coordinator) hasProcess() func(*RunRecord) bool {
	c.mu.Lock()
	orphaned := make(map[string]bool, len(c.graceExpire))
	for runID := range c.graceExpire {
		if c.attach[runID] == nil {
			orphaned[runID] = true
		}
	}
	c.mu.Unlock()
	return func(r *RunRecord) bool { return !orphaned[r.RunID] }
}

// Draining reports whether BeginDrain has been called.
func (c *Coordinator) Draining() bool {
	return c.admissionClosed.Load()
}

// Close tears the coordinator down: listeners, journals, owner lock. Live
// children are killed via their launch close (the run process is their
// lifetime).
//
// Order: seal the tracked group and the stream group (goTracked stops
// Add()ing and a late stream handler is refused at enter, so nothing can
// race the joins below) → cancel baseCtx (every ctx-aware
// tracked goroutine starts unwinding) → kill live attachments (best-effort;
// a goroutine still mid-launch may not have published rt.close yet — that is
// exactly what the wg join below catches) → srv.close (Stop the gRPC server
// — this is what actually unblocks the RunChannel/RunnerChannel pump
// goroutines, which key off the STREAM's own context, not baseCtx) → close
// the spool writers → wg.Wait (bounded escape) → close journals. This
// guarantees no tracked goroutine touches the state dir after Close()
// returns (barring the logged bounded-escape case).
//
// The spool writers close BEFORE the join and the journals AFTER it, and the
// asymmetry is the point: the join is BOUNDED, so "no writes after Close" is
// only guaranteed for what is closed before it. The spool is the one store
// whose path is resolved from the ambient $HOME at WRITE time, so a write that
// escapes the bound does not land in this run's own tree — it lands wherever
// $HOME points by then. That store therefore refuses first; the journals, whose
// paths were fixed at construction, can wait for the join.
func (c *Coordinator) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.tracked.Seal()
		c.streams.Seal()
		c.cancel()
		c.mu.Lock()
		attachments := make([]*childRt, 0, len(c.attach))
		for _, rt := range c.attach {
			attachments = append(attachments, rt)
		}
		c.mu.Unlock()
		for _, rt := range attachments {
			c.mu.Lock()
			closeFn := rt.close
			c.mu.Unlock()
			if closeFn != nil {
				closeFn()
			}
		}
		if t := c.takeTransport(); t != nil {
			t.Close()
		}
		// The stream handlers' deferred terminals run AFTER Stop returns;
		// join them before the writers close so a runner dropped by the
		// shutdown still gets its terminal recorded, not raced.
		c.streams.Wait(closeJoinBudget, "coordinator close: stream handlers", "a late terminal may still touch the state dir")
		// The spool writers close BEFORE the join, not after it. waitTracked is
		// BOUNDED (closeJoinBudget) and says so when it gives up — "a leaked
		// goroutine may still touch the state dir" — so a child teardown that
		// outruns the budget is expected, not exceptional. Closing the writers
		// first makes such a write REFUSE (errSpoolClosed) instead of landing:
		// the spool root is resolved from the ambient $HOME at write time, so a
		// write that escapes teardown does not land harmlessly in this run's own
		// tree, it lands in whatever $HOME names by then.
		//
		// Refusing an in-flight terminal notice is the DESIGNED fallback, not a
		// new loss: queueMail's caller already handles a failed durable queue by
		// completing a parked parent's poll directly.
		//
		// Raising closeJoinBudget instead would be tuning a threshold until a
		// gate goes quiet, which measures nothing.
		c.spoolIn.Close()
		c.waitTracked()
		c.closePartial()
	})
}

// closePartial releases what New acquired so far (also Close's tail).
//
// A journal that fails to CLOSE is warned about, naming it: Store.Close closes
// the file handle, so this is the last moment an ENOSPC/EIO on the final flush
// can be observed at all — and such a failure retracts the durability the
// coordinator already claimed to every command it answered. Teardown proceeds
// regardless (there is nothing left to retry) and Close reports no error to
// its caller, so the warning is the whole signal.
func (c *Coordinator) closePartial() {
	var errs []error
	shut := func(name string, s *Store) {
		if s == nil {
			return
		}
		if err := s.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	shut("runs.jsonl", c.runs)
	shut("items.jsonl", c.items)
	shut("interactions.jsonl", c.auditJ)
	c.spoolIn.Close() // idempotent: Close already closed it before the join
	if len(errs) > 0 {
		c.rep.Warnf("coordinator: closing journals under %s: %v", c.stateDir, errors.Join(errs...))
	}
	if c.releaseOwner != nil {
		c.releaseOwner()
		c.releaseOwner = nil
	}
}

// audit appends one interaction record (best-effort; the audit journal has no
// projection and never gates a verb).
func (c *Coordinator) audit(kind, actor string, detail map[string]string) {
	if err := c.auditJ.Exec(func() ([]Fact, error) {
		return []Fact{factAt("interaction", c.now(), interaction{Kind: kind, Actor: actor, Detail: detail})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator audit (%s): %v", kind, err)
	}
}

// RegisterSessionOwner mints the depth-0 credential identifying a session
// owner (the parent harness `ctxloom run` launches). The token
// is returned exactly once for the env seam; only its hash is journaled.
func (c *Coordinator) RegisterSessionOwner(harp string) (token string, err error) {
	token, credHash, err := mintToken()
	if err != nil {
		return "", err
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factSessionCred, c.now(), sessionCred{Harp: harp, CredHash: credHash})}, nil
	}); err != nil {
		return "", err
	}
	return token, nil
}

// RevokeSessionOwner revokes a session-owner credential (session teardown).
func (c *Coordinator) RevokeSessionOwner(token string) {
	credHash := hashToken(token)
	if err := c.runs.Exec(func() ([]Fact, error) {
		if _, ok := c.runsF.identityFor(credHash); !ok {
			return nil, nil
		}
		return []Fact{factAt(factSessionCredRevoked, c.now(), sessionCred{CredHash: credHash})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator: revoke session credential: %v", err)
	}
}

// Identify resolves a presented bearer token to its identity, constant-time
// per candidate. Used per-request by the MCP auth middleware and
// per-stream-establishment + per-request by the gRPC interceptors. The D1
// consumer credential is checked first (a small, separate class — see
// consumer.go); a match short-circuits before the run-registry lookup.
func (c *Coordinator) Identify(token string) (Identity, bool) {
	if c.consumerCreds.verify(token) {
		return c.inProject(Identity{Consumer: true}), true
	}
	var (
		id Identity
		ok bool
	)
	c.runs.View(func() {
		id, ok = verifyToken(token, c.runsF.creds)
	})
	return c.inProject(id), ok
}

// inProject stamps id with the ONE project this coordinator serves: its
// resolved id (exported to engines as CTXLOOM_PROJECT_ID) and its directory
// (what a host-relayed handler answers for). Stamped here rather than
// journaled with each credential, so no identity can carry another value.
func (c *Coordinator) inProject(id Identity) Identity {
	id.Project = c.projectID
	id.ProjectDir = c.projectDir
	return id
}

// Owner is the identity of the session this coordinator drains for
// (Options.OwnerHarp): the caller the owner's own process speaks as.
func (c *Coordinator) Owner() Identity { return Identity{Harp: c.ownerHarp} }

// Roster lists the coordinator's children — the single state behind every
// transport. Only the coordinating session (the owner) sees its children: a
// child caller is answered with nothing, the same refusal the wire gives it
// (a child holds no lineage below itself here).
func (c *Coordinator) Roster(caller Identity) []RosterEntry {
	if caller.IsChild() {
		return nil
	}
	var out []RosterEntry
	c.runs.View(func() { out = c.rosterF.snapshot() })
	return out
}

// AgentSend delivers a message per §6a delivery-by-state. Children address
// only their parent (hub-and-spoke); the session owner addresses its
// children by harp. inReplyTo carries a correlation — see peerSend for the
// ask-reply interception it enables.
func (c *Coordinator) AgentSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, error) {
	_, disposition, err := c.peerSend(caller, to, kind, body, structured, inReplyTo)
	return disposition, err
}

// peerSend is the shared send verb behind AgentSend (bare-mcp local path)
// and the plane-2 PeerSendRequest handler: routing policy, durable queue,
// delivery-by-state. delivered reports a completed waiting receive (a local
// parked poll, or a tentative push into the recipient runner's parked recv).
//
// inReplyTo is checked FIRST against outstanding asks (resolveAskReply): a
// match resolves the parked ask and returns immediately WITHOUT queuing
// ordinary mail. An UNKNOWN id falls through to ordinary delivery, so a
// stale/duplicate in_reply_to degrades gracefully rather than erroring the
// send.
func (c *Coordinator) peerSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (msgID string, disposition string, err error) {
	// THE COLLISION, and where it is resolved (spoolturnresult.go).
	//
	// A child's AUTOMATIC turn report quotes the id of the message
	// that started the turn — the correlation a parent wants, and a ruling.
	// But correlation is AUTHORITY here: an in_reply_to that names an
	// outstanding ask answers it. An automatic report must not, or the
	// cooperative-reply ruling ("the answer is what the child CHOSE to send")
	// is defeated by whatever the model happened to say that turn, arriving
	// with exactly the right correlation.
	//
	// The discriminator is AUTHORSHIP, not kind. Kind cannot draw this line: a
	// child deliberately answering "here are my findings" naturally sends
	// KindResult, which is also what an automatic report is, so a resolver
	// that refused KindResult would silently strand the most natural
	// cooperative reply there is — this project's characteristic defect, newly
	// installed. Only the writer knows whether the agent chose to send, so the
	// writer marks it and this chokepoint reads the mark.
	if inReplyTo != "" && !IsAutoReport(structured) {
		// The CORRELATED ASK's answer (spoolcontrol.go): a reply to a
		// coordinator question/summarize resolves the parked ask and does NOT
		// also become mail — the asker is this coordinator, not a mailbox, and
		// delivering the answer onward would give the target's parent a message
		// it never asked for. A miss falls through, so a stale correlation
		// degrades to ordinary mail rather than failing the send.
		if disposition, matched, err := c.resolveAskReply(caller, inReplyTo, body, structured); matched {
			return inReplyTo, disposition, err
		}
	}
	// The closed-vocabulary ingress guard, at the ONE point both sender surfaces
	// funnel through (agent_send's bare-MCP handler and the plane-2
	// PeerSendRequest). It runs before any routing so a sender learns the
	// vocabulary is wrong even when the recipient is also wrong, and AFTER the
	// ask-reply interception, whose reply carries its answer in the body rather
	// than the kind.
	if err := SenderMailKind(kind); err != nil {
		return "", "", err
	}
	if caller.IsChild() {
		return c.childSend(caller, to, kind, body, structured, inReplyTo)
	}
	return c.ownerSend(caller, to, kind, body, structured, inReplyTo)
}

// childSend is peerSend's HUB-AND-SPOKE half: a delegated child addresses only
// its own parent, resolved from journaled lineage — by ParentAddress or by the
// parent's own harp, nothing else.
func (c *Coordinator) childSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, string, error) {
	parent := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(caller.Harp); r != nil {
			parent = r.ParentHarp
		}
	})
	if parent == "" {
		return "", "", fmt.Errorf("agent_send: unknown sender %q: not a child of this coordinator", caller.Harp)
	}
	if to != ParentAddress && to != parent {
		return "", "", ErrPeerRouting
	}
	c.audit("agent_send", caller.Harp, map[string]string{"to": parent, "kind": kind})
	id, err := c.queueMailPayload(caller.Harp, parent, kind, body, structured, inReplyTo)
	if err != nil {
		return "", "", err
	}
	return id, "sent to the coordinator", nil
}

// ownerSend is peerSend's other half: the session owner addressing one of its
// own children by harp. The disposition names the §6a state the delivery
// observed (deliveryDisposition).
func (c *Coordinator) ownerSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, string, error) {
	if to == ParentAddress {
		return "", "", errors.New("agent_send: this session is the coordinator — it has no parent; address a child by its harp")
	}
	known := false
	c.runs.View(func() { known = c.runsF.currentRun(to) != nil })
	if !known {
		return "", "", fmt.Errorf("agent_send: unknown recipient %q: not a child of this session (spawn it with agent_run first)", to)
	}
	c.audit("agent_send", caller.Harp, map[string]string{"to": to, "kind": kind})
	msgID := newMessageID()
	observed, err := c.deliverMailID(msgID, caller.Harp, to, kind, body, structured, inReplyTo)
	if err != nil {
		return "", "", err
	}
	_, prose := deliveryDisposition(observed)
	return msgID, prose, nil
}

// deliveryDisposition classifies ONE §6a delivery-by-state outcome — the state
// the delivery observed (driveQueued's return) — into the two vocabularies the
// coordinator answers in: the typed mode a steer reports to its initiator, and the
// prose peerSend hands the sending agent. They are ONE classification on
// purpose: a state described two ways is a state the two surfaces can come to
// disagree about.
//
// mode is deliberately coarser: StateQueued and a mid-turn executing/parked
// child are both DeliveryQueued, while the prose separates them because "has
// not started yet" and "mid-turn" mean different things to a waiting agent.
func deliveryDisposition(state string) (mode, prose string) {
	switch state {
	case StateEnded:
		return DeliveryResumed, "child session had ended — resuming it with the message as its next turn"
	case deliveryEndedDraining:
		return DeliveryQueued, "queued: the child session had ended and the coordinator is draining, so it is not resumed; the message waits in its mailbox for the harp's next run"
	case StateIdle:
		return DeliveryNewTurn, "delivering as a new turn"
	case StateQueued:
		return DeliveryQueued, "queued: the child has not started yet; it will drain its mailbox after its first turn"
	case deliveryPaused:
		return DeliveryQueued, "held: the child is paused; the message waits in its spool and is delivered when it is resumed"
	default: // executing / parked race
		return DeliveryQueued, "queued mid-turn: delivered at the child's next turn boundary"
	}
}

// liveRun reports whether a call made as (harp, runID) still speaks for a live
// run (runsFold.liveRun).
func (c *Coordinator) liveRun(harp, runID string) bool {
	var live bool
	c.runs.View(func() { live = c.runsF.liveRun(harp, runID) })
	return live
}

// AgentRecv drains the caller's own durable mailbox, parking up to wait.
// Delivery is AT-LEAST-ONCE: this call implicitly acknowledges the messages a
// PRIOR recv returned (cursor-ack); unacknowledged deliveries are re-delivered
// after a coordinator relaunch. Dupes are deduped on message id at the store.
func (c *Coordinator) AgentRecv(ctx context.Context, caller Identity, wait time.Duration) ([]Message, error) {
	c.audit("agent_recv", caller.Harp, nil)
	if !c.ownerSpool(caller.Harp) {
		return nil, fmt.Errorf("%w (asked for %q; the owner is %q)", ErrRecvNotOwner, caller.Harp, c.ownerHarp)
	}
	return c.inbox.recv(ctx, caller.Harp, caller.RunID, wait)
}

// AgentStop kills a child run (KillRun semantics): the engine/container dies,
// the slot frees, the terminal is journaled, and the credential is revoked.
// The host-side verb is keyed by harp; reason (optional) becomes the run's
// terminal detail. The bulk form — every child of the caller — is
// StopChildren (drain.go).
func (c *Coordinator) AgentStop(caller Identity, harp, reason string, grace time.Duration) (string, error) {
	if caller.IsChild() {
		return "", errors.New("agent_stop: only the coordinating session may stop its children")
	}
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil {
		return "", fmt.Errorf("agent_stop: unknown session %q: not a child of this session", harp)
	}
	return c.stopRun(caller, rec, reason, grace), nil
}

// stopRun is the per-run agent_stop both surfaces (AgentStop, serveStopRun)
// share once the caller's claim on rec is settled; it returns the disposition
// prose the caller reports.
//
// The harp is marked stopped before anything else, and the LAUNCH is
// cancelled on both branches. A stop that only ends the run record cannot
// stop a launcher: a stop can land on an already-ended run (the retry loop's
// own terminal) with a relaunch already armed behind it, report success, and
// leave the retry loop spinning on indefinitely. The mark turns an
// armed-but-not-yet-enqueued relaunch back; cancelLaunch also cancels the
// context of any attempt currently in flight, which a container prepare makes
// a seconds-wide window.
//
// On a live run the stop's terminal is claimed BEFORE the launch is
// cancelled. A launch cancelled mid-StartRun fails with context canceled, and
// failChild would otherwise claim the exactly-once terminal as a launch
// failure: the parent would be told a child it stopped "failed to launch".
//
// reason is the run's terminal DETAIL — what the roster, the journal and the
// audit record show for this stop. Absent, the detail keeps the exact
// wording that shipped. A stop landing on an ended run reports the earlier
// terminal's reason when there was one, so a second agent_stop says WHY it
// ended, not just that it did.
func (c *Coordinator) stopRun(caller Identity, rec *RunRecord, reason string, grace time.Duration) string {
	c.markStopped(rec.Harp)
	cancelLaunch := func() {
		c.cancelLaunch(rec.Harp)
		if hook := c.stopCancelledLaunchHook; hook != nil {
			hook(rec.Harp)
		}
	}
	if rec.Ended {
		cancelLaunch()
		ended := rec.Cause
		if rec.Detail != "" {
			ended += ": " + rec.Detail
		}
		return fmt.Sprintf("child %s had already ended (%s); any pending relaunch is cancelled", rec.Harp, ended)
	}
	detail := fmt.Sprintf("stopped by %s", caller.Harp)
	audit := map[string]string{"harp": rec.Harp, "run_id": rec.RunID}
	if reason = strings.TrimSpace(reason); reason != "" {
		detail += ": " + reason
		audit["reason"] = reason
	}
	c.audit("agent_stop", caller.Harp, audit)
	c.setPendingStop(rec.RunID, detail)
	defer c.setPendingStop(rec.RunID, "")
	c.stopAtRunner(rec, reason, grace)
	c.terminateRun(rec.RunID, CauseStopped, detail)
	cancelLaunch()
	return fmt.Sprintf("stopped child %s; its execution slot is freed (a later agent_send resumes it as a fresh run)", rec.Harp)
}

// DefaultStopGrace is how long a stopped run's interrupted turn gets to end
// when agent_stop names no grace.
const DefaultStopGrace = 10 * time.Second

// stopAnswerMargin is how much longer than the grace the coordinator waits for
// the runner's StopRun answer — the close that follows the interrupt.
const stopAnswerMargin = 5 * time.Second

// stopAtRunner asks rec's runner to stop the run INTERRUPT-THEN-CLOSE
// (StopRun): the turn in flight is interrupted and given grace to reach its
// boundary — so its report is written — before the run closes. Bounded by
// grace + stopAnswerMargin; a run no runner request can reach, a runner that
// is gone, or one that does not answer in time is simply ended by the
// terminal that follows, as before.
func (c *Coordinator) stopAtRunner(rec *RunRecord, reason string, grace time.Duration) {
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	if !c.runnerReachable(rec) {
		return
	}
	ctx, cancel := context.WithTimeout(c.baseCtx, grace+stopAnswerMargin)
	defer cancel()
	resp, err := c.requestRunner(ctx, rec.CredHash, RunnerRequest{Kind: StopRun{RunID: rec.RunID, Reason: reason, Grace: grace}})
	if err == nil && resp.Err != nil {
		c.rep.Warnf("agent_stop %s: the runner refused the graceful stop (%v); ending the run now", rec.Harp, resp.Err)
	}
	if hook := c.stopAnsweredHook; hook != nil {
		hook(rec.RunID)
	}
}

// runnerReachable reports whether a runner request can reach rec's run: it
// was issued to a runner credential, and its harp is one the coordinator
// delivers to.
func (c *Coordinator) runnerReachable(rec *RunRecord) bool {
	return rec.CredHash != "" && c.spoolDeliverTo(rec.Harp)
}

// setPendingStop records (detail != "") or clears the terminal detail of an
// agent_stop in progress on runID.
func (c *Coordinator) setPendingStop(runID, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if detail == "" {
		delete(c.pendingStops, runID)
		return
	}
	if c.pendingStops == nil {
		c.pendingStops = make(map[string]string)
	}
	c.pendingStops[runID] = detail
}

// pendingStop is the terminal detail of an agent_stop in progress on runID.
func (c *Coordinator) pendingStop(runID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	detail, ok := c.pendingStops[runID]
	return detail, ok
}

// injectDigestRunes bounds the mirror notice body: enough for the parent to
// recognize what was said, never the bulk.
const injectDigestRunes = 120

func injectDigest(text string) string {
	r := []rune(text)
	if len(r) <= injectDigestRunes {
		return text
	}
	return fmt.Sprintf("%s… (%d chars total)", string(r[:injectDigestRunes]), len(r))
}
