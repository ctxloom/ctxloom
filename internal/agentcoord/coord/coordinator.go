package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/config"
	livenesspkg "github.com/ctxloom/ctxloom/internal/liveness"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ErrNotInjectable rejects a user injection whose target the coordinator does
// not hold and cannot resume (an unknown harp, or a foreign process's session
// — there is no delivery channel into another process's terminal, by
// design). Relocated from the retired internal/agentbus package (D2): the
// vocabulary was always native to the coordinator's own Inject method; the
// bus socket was just one of two transports wrapping it.
var ErrNotInjectable = errors.New("inject: target is not a child this coordinator holds or can resume")

// ErrDraining is returned by every admission site (AgentRun, StartOwnedRun,
// coordService.RunnerChannel's Hello for a runner with nothing already in
// flight, Serve) once BeginDrain has been called: the coordinator refuses
// new work, though already-admitted runs continue to completion. Mapped to
// codes.Unavailable wherever a site answers over gRPC (statusFromErr,
// runchannel.go) — the refusal is recoverable (retry against a coordinator
// that isn't draining), never a permanent failure.
var ErrDraining = errors.New("coordinator is draining: refusing new work (already-admitted runs continue to completion)")

// Delivery modes Inject reports back: which §6a delivery-by-state rule the
// coordinator applied to the user's text. Relocated from internal/agentbus
// (D2) — same vocabulary, now native rather than borrowed from the retired
// bus wire protocol.
const (
	DeliveryCompletedRecv = "completed-recv" // completed the child's parked agent_recv
	DeliveryNewTurn       = "new-turn"       // woke an idle child into a new turn
	DeliveryQueued        = "queued"         // queued for the child's next turn boundary
	DeliveryResumed       = "resumed"        // relaunched an ended session, the text as its next turn
	// DeliveryRejected is the plane-2 arrival the mailbox route could never
	// produce: the target was reached, understood the steer, and DECLINED it
	// (paused, foremost). It exists so a refusal is never printed as a queue —
	// the mailbox vocabulary had no way to say "it did not land".
	DeliveryRejected = "rejected"
)

// Options configures a Coordinator.
type Options struct {
	// Cfg is the loaded project config (required for the production
	// spawner; tests inject Spawner instead).
	Cfg *config.Config
	// ProjectDir is the project working directory the coordinator serves.
	ProjectDir string
	// ProjectKey is the stable project identity keying the durable state
	// dir ("" falls back to a path-derived key).
	ProjectKey string
	// StateDir overrides the state dir entirely (tests).
	StateDir string
	// Spawner overrides the launch seam (tests). Nil = production.
	Spawner Spawner
	// Factory is the production spawner's plugin-construction test seam.
	Factory pb.ClientFactory
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
	// Every harp's CURRENT run (the resume key) is ALWAYS kept, outside this
	// count. <= 0 keeps the package default (defaultEndedRunTail). Tests set a
	// tiny value to exercise reaping; production keeps the default.
	EndedRunTail int
	// EndedRunMaxAge reaps an ended, non-current run once it is older than
	// this, independent of EndedRunTail. <= 0 keeps the package default
	// (defaultEndedRunMaxAge).
	EndedRunMaxAge time.Duration
	// RunnerAwaitTimeout overrides how long issueStartRun waits for a
	// just-spawned runner to dial home before declaring the launch attempt
	// failed (children.go's dial-home barrier, awaitRunner). <= 0 keeps the
	// package default (defaultRunnerAwaitTimeout). Production leaves this
	// unset; tests lower it to exercise the timeout path without waiting out
	// the real budget, or raise/lower it to prove a slow-but-successful
	// dial-home survives (or doesn't) at a given budget.
	RunnerAwaitTimeout time.Duration
	// SpoolTee turns on the mailbox's SHADOW TEE onto the file spool
	// (spooltee.go): mail is additionally written as spool files and
	// doorbelled, reads are untouched. Default false, and false means nothing
	// happens at all — no directory, no doorbell. Production sources this from
	// project config (config.Config.GetDelegationSpoolTee); the coordinator
	// also stamps it onto every runner it spawns (EnvRunSpoolTee) so both ends
	// of a run agree without asking each other.
	SpoolTee bool
	// SpoolDelivery CUTS coordinator<->child mail over onto the file spool
	// (spooldelivery.go): the file is the only copy, the consume-rename is the
	// delivery ack, and the doorbell only bounds latency. Default false, and
	// false is byte-identical pre-spool behaviour. Production sources it from
	// project config (config.Config.GetDelegationSpoolDelivery); the
	// coordinator stamps it onto every runner it spawns
	// (EnvRunSpoolDelivery), because a run cut over on one side only delivers
	// nothing at all.
	SpoolDelivery bool
	// SpoolSweepInterval overrides the spool reconciliation cadence (0 = the
	// built-in spoolSweepInterval). Exposed for tests, which must be able to
	// prove that a DROPPED doorbell is still delivered by the sweep without
	// waiting out the production interval.
	SpoolSweepInterval time.Duration
}

// spoolPosture is the pair of spool switches a run is spawned under, kept
// together so the per-spawn env stamp cannot acquire a third bare bool
// argument and so a caller cannot pass one of the two and forget the other.
type spoolPosture struct {
	// Tee mirrors mail onto the spool without changing delivery.
	Tee bool
	// Delivery makes the spool the delivery path.
	Delivery bool
}

// Coordinator is the runtime coordinator: durable CQRS stores + credential
// registry + the delegation orchestration, served over gRPC (RunnerChannel)
// and streamable-HTTP MCP by the listeners in httpserver.go.
type Coordinator struct {
	projectDir string
	stateDir   string
	ephemeral  bool // state dir is per-process (owner lock lost); no adoption
	now        func() time.Time

	baseCtx context.Context
	cancel  context.CancelFunc

	releaseOwner func()

	runs     *Store
	runsF    *runsFold
	queueF   *queueFold
	rosterF  *rosterFold
	reportsF *reportsFold
	mail     *Store
	mailF    *mailFold
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
	// custom maps host-relay tool names ("ctxloom/<tool>") onto the
	// coordinator-side handlers the hosting process injects.
	custom map[string]CustomHandler
	// spoolDoorbell counts what the spool doorbell deliberately does not
	// retry (spooldoorbell.go). Atomics, not mu-guarded: a counter that
	// needed the coordinator lock would put contention on the exact path
	// whose whole point is to cost nothing when it fails.
	spoolDoorbell spoolDoorbellCounters
	// pushUnavailable counts mail that could not be PUSHED to its recipient
	// because that recipient has no pushable run channel (runchannel.go's
	// pushMail guard). Same atomics-not-mu reasoning as spoolDoorbell above.
	//
	// It is not an error count. Some of it is by design — a legacy child's
	// unparked channel is drained by its own turn boundary, not by a push. But
	// one case is architectural and was invisible: THE SESSION OWNER'S OWN
	// HARP NEVER HAS A RUN CHANNEL, so mail queued for a coordinator is never
	// pushed and waits for that coordinator to call agent_recv itself. That
	// silence is what made a missing wake read as "the system is just a bit
	// slow" — the same failure the spool doorbell counts its drops to avoid.
	pushUnavailable atomic.Uint64
	// spoolTee is the SHADOW-TEE switch (Options.SpoolTee, config
	// delegation.spool_tee). Read-only after New, so no lock: it is a
	// process-lifetime posture, not runtime state, and making it mutable would
	// mean a delivery could be half-teed across a flip.
	spoolTee bool
	// spoolDelivery is the CUTOVER switch (Options.SpoolDelivery, config
	// delegation.spool_delivery), read-only after New for the same reason
	// spoolTee is: a delivery half-cut across a flip would be a message with
	// no reader.
	spoolDelivery bool
	// spoolIn lends the per-child in/ writers. Non-nil ONLY when the spool is
	// switched on at all (tee or delivery): constructing it is what would
	// create spool directories, and "both flags are off" has to mean nothing
	// on disk changed.
	spoolIn       *spoolWriterCache
	spoolTeeCount spoolTeeCounters
	// spoolReactor serialises the coordinator's own spool reading (out/ and
	// in/consumed sweeps). Non-nil only under spoolDelivery — see its type.
	spoolReactor *spoolReactor
	// spoolSweepInterval overrides the reconciliation cadence
	// (Options.SpoolSweepInterval; 0 = spoolSweepInterval). Test seam: a
	// missed-doorbell test has to prove the sweep RECOVERS delivery, and the
	// only honest way to do that is to let the sweep actually run.
	spoolSweepInterval time.Duration
	spoolDeliveryCount spoolDeliveryCounters
	// spoolSeen remembers which in/consumed entries have already been credited
	// as progress, per role. consumed/ is an audit trail nothing prunes yet, so
	// without this every sweep would re-credit the whole history.
	spoolSeenMu sync.Mutex
	spoolSeen   map[string]map[string]bool

	mu          sync.Mutex
	attach      map[string]*childRt // runID → runtime attachment
	byHarp      map[string]*childRt // harp → current attachment
	polls       map[string]*parkedPoll
	delivered   map[string][]string       // role → delivered-but-unacked ids
	runners     map[string]*runnerSession // credHash → connected runner
	runnerReady map[string]chan struct{}  // credHash → closed on Hello registration (awaitRunner)
	chans       map[string]*runChan       // role harp → live RunChannel
	// reqTrack is plane-2 request idempotency that SURVIVES a RunChannel
	// reconnect, keyed by (role, request_id). It replaces the per-connection
	// runChan.reqCache/inflight (reset to empty on every dial): a request the
	// runner reissues with the same request_id on a fresh channel (home.go)
	// must reuse the in-flight dispatch, never start a second one — a
	// duplicate dispatch would run the request's effect twice, and the
	// answer to one copy would be lost with the channel it arrived on.
	// Cleaned per-role at terminal (clearReqTrack); lazily initialized.
	reqTrack map[reqKey]*inflightReq
	// downTrack is reqTrack's mirror in the DOWN direction: outstanding
	// coordinator→agent control requests, keyed by the same (role, request_id).
	// It lives here rather than on runChan for the same reason — a request
	// survives the channel it was queued on and is reissued on the role's next
	// attach — and it is where the DRAIN SEAM's capability re-validation reads
	// from (control.go's redrainDownRequests). Cleaned per-role at terminal
	// (clearDownTrack); lazily initialized.
	downTrack map[reqKey]*downReq
	// asks holds the outstanding correlated asks (spoolcontrol.go) — question
	// and summarize — keyed by the id their request file carries as origin_id,
	// which is what a reply quotes in in_reply_to. Registered BEFORE the file
	// is published, so that an answer arriving the instant the file becomes
	// observable still resolves instead of racing its own registration.
	// Lazily initialized.
	asks map[string]*pendingAsk
	// onAskPublished, when set, is called by controlAsk between REGISTERING the
	// waiter and PUBLISHING the ask — the register-before-publish test seam,
	// It fires on that side
	// of the publish deliberately: a hook fired after it cannot distinguish a
	// correct implementation from one that registers between the write and the
	// hook, since both have registered by then. Nil in production.
	onAskPublished func(askID string)
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
	// RunnerChannel pumps). Close() joins it BEFORE closing the journals and
	// removing an ephemeral state dir — see trackedGroup for why an unjoined
	// goroutine racing that teardown is this package's worst flake class.
	tracked trackedGroup

	// srv is the listener set (httpserver.go), nil until Serve. ATOMIC, not
	// mu-guarded: Serve publishes it while the spawn path (ReachURL, building
	// a child's env) and Close read it from other goroutines, and those
	// readers must not have to take the coordinator's big lock — nor can they
	// be allowed to observe a half-published coordServing.
	srv atomic.Pointer[coordServing]

	// admissionClosed is the application-layer DRAIN flag (task
	// definite-phoniness): BeginDrain sets it once, and every admission
	// site (AgentRun, StartOwnedRun, RunnerChannel's Hello for a runner
	// with nothing already in flight, Serve) checks it and returns
	// ErrDraining instead of admitting new work. It does NOT touch
	// c.baseCtx, c.srv, or any live attachment — an already-admitted run
	// keeps running exactly as it would without a drain in progress — and
	// it is deliberately not consulted by Close(), which is the hard,
	// immediate teardown (see its own doc): the caller flips this first,
	// waits for nothing to be left in flight (Roster/WatchRuns), and only
	// then calls Close(). ATOMIC, not mu-guarded, for the same reason srv
	// is: every admission site must be able to check it without taking
	// c.mu.
	admissionClosed atomic.Bool

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
	// spawnDispatchedHook, if set (tests only, same package), runs
	// synchronously in AgentRun immediately after the child's driver
	// goroutine has been dispatched, with the child's harp. It is the
	// deterministic seam for the disposition-truthfulness test: the point of
	// the test is that the dispatched goroutine can reach its terminal — and
	// so give back the slot the enqueue claimed — before agent_run composes
	// its answer, and parking here is what makes that ordering a fact rather
	// than a scheduler coin flip. Nil in production (zero cost).
	spawnDispatchedHook func(harp string)

	closeOnce sync.Once
}

// New opens (or adopts) the project's coordinator state and starts the
// orchestration core. Listeners come up separately via Serve (httpserver.go)
// so tests can run the core without ports.
func New(opts Options) (*Coordinator, error) {
	claim, err := acquireStateDir(opts)
	if err != nil {
		return nil, err
	}
	t := resolveTunables(opts)

	c := &Coordinator{
		projectDir:         opts.ProjectDir,
		stateDir:           claim.dir,
		ephemeral:          claim.ephemeral,
		now:                t.now,
		releaseOwner:       claim.release,
		spawner:            opts.Spawner,
		slots:              semaphore.NewWeighted(int64(t.concurrencyCap)),
		depthCap:           t.depthCap,
		spawnNoticeAfter:   defaultSpawnNoticeAfter,
		endedRunTail:       t.endedRunTail,
		endedRunMaxAge:     t.endedRunMaxAge,
		runnerAwaitTimeout: t.runnerAwaitTimeout,
		maxLaunchAttempts:  t.maxLaunchAttempts,
		launchBackoffBase:  t.launchBackoffBase,
		launchBackoffMax:   t.launchBackoffMax,
		watch:              newWatchHub(),
		consumerCreds:      &consumerCreds{},
		attach:             make(map[string]*childRt),
		byHarp:             make(map[string]*childRt),
		polls:              make(map[string]*parkedPoll),
		delivered:          make(map[string][]string),
		runners:            make(map[string]*runnerSession),
		runnerReady:        make(map[string]chan struct{}),
		chans:              make(map[string]*runChan),
		launchArmed:        make(map[string][]chan struct{}),
		launches:           make(map[string]*launchState),
		spoolTee:           opts.SpoolTee,
		spoolDelivery:      opts.SpoolDelivery,
		spoolSweepInterval: opts.SpoolSweepInterval,
	}
	if c.spoolTee || c.spoolDelivery {
		// Built ONLY under a flag — see the field's doc. The writers
		// themselves are still lazy (one per child, on its first message), so
		// enabling either switch does not create a spool for a run that never
		// sends.
		c.spoolIn = newSpoolWriterCache(spool.NewHomeMapper(), spool.DirIn, spoolWriterIDCoordinator)
	}
	c.baseCtx, c.cancel = context.WithCancel(context.Background())
	if c.spawner == nil {
		if opts.Cfg == nil {
			return nil, c.abortNew(errors.New("coord: Options.Cfg is required without an injected Spawner"))
		}
		c.spawner = newProdSpawner(opts.Cfg, opts.ProjectDir, opts.Factory)
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
	// The PROGRESS watchdog (liveness.go), alongside the runner-liveness one
	// above. They answer different questions and neither subsumes the other:
	// runnerWatchdog catches a runtime that DIED (heartbeat silence) and acts
	// on it; this one catches a runtime that is very much alive and making no
	// progress, and only ever warns.
	c.goTracked(c.livenessWatchdog)
	return c, nil
}

// abortNew unwinds a coordinator New never returned: cancel the base context,
// release the journals and the owner lock (closePartial), and REMOVE an
// ephemeral state dir this call itself created. Returns err unchanged so every
// failure path reads `return nil, c.abortNew(err)`.
//
// The ephemeral removal is the part that mattered: the fallback mints a fresh
// os.MkdirTemp dir, and a New that then failed left it behind with nobody
// holding a reference to it — one stranded 0700 directory per attempt, since
// nothing else knows the name.
func (c *Coordinator) abortNew(err error) error {
	c.cancel()
	c.closePartial()
	if c.ephemeral {
		_ = os.RemoveAll(c.stateDir)
	}
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
	maxLaunchAttempts  int
	launchBackoffBase  time.Duration
	launchBackoffMax   time.Duration
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
	// The launch-retry budget has no Options field (deliberately — it is an
	// operator/env tunable, not a per-call test seam): resolved once, here,
	// from the environment (resolveLaunchTunables), never per attempt.
	t.maxLaunchAttempts, t.launchBackoffBase, t.launchBackoffMax = resolveLaunchTunables()
	return t
}

// stateDirClaim is an acquired state dir plus how it was acquired: release
// frees the exclusive-owner lock (nil when there is none), and ephemeral marks
// a per-process dir whose contents die with this coordinator.
type stateDirClaim struct {
	dir       string
	release   func()
	ephemeral bool
}

// acquireStateDir resolves and claims the coordinator's state dir: an explicit
// Options.StateDir verbatim (tests), otherwise the project's durable dir under
// its exclusive-owner lock, otherwise an ephemeral fallback.
func acquireStateDir(opts Options) (stateDirClaim, error) {
	if opts.StateDir != "" {
		return stateDirClaim{dir: opts.StateDir}, nil
	}
	key := opts.ProjectKey
	if key == "" {
		key = pathDerivedProjectKey(opts.ProjectDir)
	}
	dir, err := stateDirForProject(key)
	if err != nil {
		return stateDirClaim{}, err
	}
	release, err := claimOwner(dir)
	if err == nil {
		return stateDirClaim{dir: dir, release: release}, nil
	}
	// Another live session-owning process holds this project's journals:
	// single writer per journal holds across processes, so this coordinator
	// runs on an ephemeral state dir (no adoption; its own state dies with
	// it). Warned, not fatal — concurrent sessions in one project are
	// legitimate.
	clidiag.Warn("ctxloom", "coordinator state for this project is owned by another live session; running this session's coordinator on ephemeral state (no cross-relaunch adoption)")
	tmp, terr := os.MkdirTemp("", "ctxloom-coord-")
	if terr != nil {
		return stateDirClaim{}, fmt.Errorf("coord: ephemeral state dir: %w", terr)
	}
	return stateDirClaim{dir: tmp, ephemeral: true}, nil
}

// openJournals builds the folds and opens every journal (plus the artifact
// blob store) in the claimed state dir. On failure the caller aborts; each
// store opened so far is closed by closePartial.
func (c *Coordinator) openJournals() error {
	c.runsF, c.queueF, c.rosterF, c.reportsF = newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold()
	runs, err := openStore(filepath.Join(c.stateDir, "runs.jsonl"), c.runsF, c.queueF, c.rosterF, c.reportsF)
	if err != nil {
		return err
	}
	c.runs = runs
	c.mailF = newMailFold()
	mail, err := openStore(filepath.Join(c.stateDir, "mailbox.jsonl"), c.mailF)
	if err != nil {
		return err
	}
	c.mail = mail
	c.itemsF = newItemsFold()
	// D4 CHECKPOINT compaction: a prior snapshot (if one exists — the
	// common case is none, a fresh project) seeds the fold and replay
	// starts at its offset instead of byte 0 — openStoreFromOffset falls
	// back to a full replay by itself if the offset is stale (journal.go).
	itemsOffset := int64(0)
	if snap, ok := loadItemsSnapshot(c.stateDir); ok {
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
func (c *Coordinator) goTracked(fn func()) { c.tracked.dispatch(fn) }

// closeJoinBudget bounds Close's wait for tracked goroutines: generous headroom
// above the ctx-aware waits every tracked loop selects on (slot acquisition,
// runner awaits, request round-trips all key off c.baseCtx, already cancelled by
// the time waitTracked runs), short enough that one wedged handler cannot hang
// shutdown forever. The most generous of the package's four, because the
// journals and an ephemeral state dir's removal wait behind it.
const closeJoinBudget = 5 * time.Second

// waitTracked joins every c.goTracked goroutine, with a bounded escape.
func (c *Coordinator) waitTracked() {
	c.tracked.wait(closeJoinBudget, "coordinator close", "a leaked goroutine may still touch the state dir")
}

// adopt reconciles state read from disk with the fresh process (acceptance
// (4)): queued mail is preserved as-is (drainable); non-ended HOST runs died
// with the previous process and are terminated (orphaned); non-ended
// CONTAINER runs may still be alive — their credentials stay valid so their
// RunnerChannels can re-Hello against the re-bound endpoint, and a grace
// timer terminates any that never dial back (runner loss). Live-child
// engine-stream continuity is Wave C.
func (c *Coordinator) adopt() {
	type pending struct {
		runID     string
		credHash  string
		container bool
	}
	var stale []pending
	c.runs.View(func() {
		for id, r := range c.runsF.runs {
			if r.Ended {
				continue
			}
			// EITHER ownership mode gets the runner-loss grace below: what
			// earns it is that the run's engine outlives the coordinator
			// process, which is true of a container regardless of who owns
			// its daemon.
			stale = append(stale, pending{runID: id, credHash: r.CredHash, container: agent.IsContainerRuntimeAxis(r.Runtime)})
		}
	})
	for _, p := range stale {
		if !p.container {
			c.terminateRun(p.runID, CauseOrphaned, "coordinator relaunched; the child's engine died with the previous process")
			continue
		}
		// Container run: its runner may still be alive. Give it one
		// runner-loss window to re-Hello; terminate as runner loss if it
		// never does.
		runID, credHash := p.runID, p.credHash
		c.goTracked(func() {
			select {
			case <-time.After(runnerLossTimeout):
			case <-c.baseCtx.Done():
				return
			}
			c.mu.Lock()
			_, connected := c.runners[credHash]
			c.mu.Unlock()
			if !connected {
				c.terminateRun(runID, CauseRunnerLoss, "no runner re-Hello after coordinator relaunch")
			}
		})
	}
}

// Close tears the coordinator down: listeners, journals, owner lock. Live
// children are killed via their launch close (the run process is their
// lifetime).
//
// Order: seal the tracked group (goTracked stops
// Add()ing, so nothing can race the join below) → cancel baseCtx (every ctx-aware
// tracked goroutine starts unwinding) → kill live attachments (best-effort;
// a goroutine still mid-launch may not have published rt.close yet — that is
// exactly what the wg join below catches) → srv.close (Stop the gRPC server
// — this is what actually unblocks the RunChannel/RunnerChannel pump
// goroutines, which key off the STREAM's own context, not baseCtx) →
// wg.Wait (bounded escape) → close journals → remove an ephemeral state
// dir. This guarantees no tracked goroutine touches the state dir after
// Close() returns (barring the logged bounded-escape case).
// BeginDrain flips the coordinator into the application-layer DRAINING
// state: every admission site (AgentRun, StartOwnedRun, RunnerChannel's
// Hello for a runner with nothing already in flight, Serve) starts
// returning ErrDraining instead of admitting new work. Already-admitted
// runs are untouched — this is a one-way, idempotent flip (no
// corresponding "undrain": nothing in this codebase resumes accepting work
// after announcing it will not).
//
// This is deliberately an APPLICATION-layer drain, not a transport-level
// one: coordServing.close's doc explains why grpc-go's GracefulStop cannot
// be used on this server — its only transport (h2c via ServeHTTP) wraps
// every connection in a serverHandlerTransport whose Drain() is an
// unconditional panic, and RunChannel/RunnerChannel are perpetual streams
// a transport-level drain would never resolve against even without that
// panic. BeginDrain instead closes admission here, at the verbs that mint
// new work, and leaves the transport alone; the caller is responsible for
// waiting until nothing is left in flight (Roster/WatchRuns) before
// calling Close().
func (c *Coordinator) BeginDrain() {
	c.admissionClosed.Store(true)
}

// Draining reports whether BeginDrain has been called.
func (c *Coordinator) Draining() bool {
	return c.admissionClosed.Load()
}

func (c *Coordinator) Close() {
	c.closeOnce.Do(func() {
		c.tracked.seal()
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
		if srv := c.srv.Load(); srv != nil {
			srv.close()
		}
		c.waitTracked()
		c.closePartial()
		if c.ephemeral {
			_ = os.RemoveAll(c.stateDir)
		}
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
	shut("mailbox.jsonl", c.mail)
	shut("items.jsonl", c.items)
	shut("interactions.jsonl", c.auditJ)
	if len(errs) > 0 {
		clidiag.Warn("ctxloom", "coordinator: closing journals under %s: %v", c.stateDir, errors.Join(errs...))
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
		clidiag.Warn("ctxloom", "coordinator audit (%s): %v", kind, err)
	}
}

// RegisterSessionOwner mints the depth-0 credential identifying a session
// owner (the parent harness `ctxloom run`/`ctxloom acp` launches). The token
// is returned exactly once for the env seam; only its hash is journaled.
func (c *Coordinator) RegisterSessionOwner(harp string) (token string, err error) {
	token, credHash, err := mintToken()
	if err != nil {
		return "", err
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factSessionCred, c.now(), sessionCred{Harp: harp, Project: c.projectDir, CredHash: credHash})}, nil
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
		clidiag.Warn("ctxloom", "coordinator: revoke session credential: %v", err)
	}
}

// Identify resolves a presented bearer token to its identity, constant-time
// per candidate. Used per-request by the MCP auth middleware and
// per-stream-establishment + per-request by the gRPC interceptors. The D1
// consumer credential is checked first (a small, separate class — see
// consumer.go); a match short-circuits before the run-registry lookup.
func (c *Coordinator) Identify(token string) (Identity, bool) {
	if c.consumerCreds.verify(token) {
		return Identity{Project: c.projectDir, Consumer: true}, true
	}
	var (
		id Identity
		ok bool
	)
	c.runs.View(func() {
		id, ok = verifyToken(token, c.runsF.creds)
	})
	if ok && id.Project == "" {
		id.Project = c.projectDir
	}
	return id, ok
}

// Roster lists the coordinator's children — the single state behind BOTH
// transports (MCP and the surviving bus roster verb).
func (c *Coordinator) Roster() []RosterEntry {
	var out []RosterEntry
	c.runs.View(func() { out = c.rosterF.snapshot() })
	return out
}

// AgentSend delivers a message per §6a delivery-by-state. Children address
// only their parent (hub-and-spoke); the session owner addresses its
// children by harp. inReplyTo carries a correlation — see peerSend for the
// ask-reply interception it enables.
func (c *Coordinator) AgentSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, error) {
	_, _, disposition, err := c.peerSend(caller, to, kind, body, structured, inReplyTo)
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
func (c *Coordinator) peerSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (msgID string, delivered bool, disposition string, err error) {
	// THE COLLISION, and where it is resolved (spoolturnresult.go).
	//
	// A cut-over child's AUTOMATIC turn report quotes the id of the message
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
	if inReplyTo != "" && !isAutoReport(structured) {
		// The CORRELATED ASK's answer (spoolcontrol.go): a reply to a
		// coordinator question/summarize resolves the parked ask and does NOT
		// also become mail — the asker is this coordinator, not a mailbox, and
		// delivering the answer onward would give the target's parent a message
		// it never asked for. A miss falls through, so a stale correlation
		// degrades to ordinary mail rather than failing the send.
		if disposition, matched := c.resolveAskReply(caller, inReplyTo, body, structured); matched {
			return inReplyTo, true, disposition, nil
		}
	}
	// The closed-vocabulary ingress guard, at the ONE point both sender surfaces
	// funnel through (agent_send's bare-MCP handler and the plane-2
	// PeerSendRequest). It runs before any routing so a sender learns the
	// vocabulary is wrong even when the recipient is also wrong, and AFTER the
	// ask-reply interception, whose reply carries its answer in the body rather
	// than the kind.
	if err := SenderMailKind(kind); err != nil {
		return "", false, "", err
	}
	if caller.IsChild() {
		return c.childSend(caller, to, kind, body, structured, inReplyTo)
	}
	return c.ownerSend(caller, to, kind, body, structured, inReplyTo)
}

// childSend is peerSend's HUB-AND-SPOKE half: a delegated child addresses only
// its own parent, resolved from journaled lineage — by ParentAddress or by the
// parent's own harp, nothing else.
func (c *Coordinator) childSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, bool, string, error) {
	parent := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(caller.Harp); r != nil {
			parent = r.ParentHarp
		}
	})
	if parent == "" {
		return "", false, "", fmt.Errorf("agent_send: unknown sender %q: not a child of this coordinator", caller.Harp)
	}
	if to != ParentAddress && to != parent {
		return "", false, "", ErrPeerRouting
	}
	c.audit("agent_send", caller.Harp, map[string]string{"to": parent, "kind": kind})
	// NO DOUBLE DELIVERY: this child reported to its parent in
	// its own words, so the automatic turn-boundary bridge (children.go's
	// bridgeTurnResult) must not report the same turn again. Marked here — the
	// one place a child→parent send is accepted — rather than at either call
	// site.
	c.noteChildReported(caller.Harp)
	id, completed, err := c.queueMailPayload(caller.Harp, parent, kind, body, structured, inReplyTo)
	if err != nil {
		return "", false, "", err
	}
	return id, completed, "sent to the coordinator", nil
}

// ownerSend is peerSend's other half: the session owner addressing one of its
// own children by harp. The disposition names the §6a state the delivery
// observed (deliveryDisposition).
func (c *Coordinator) ownerSend(caller Identity, to, kind, body string, structured json.RawMessage, inReplyTo string) (string, bool, string, error) {
	if to == ParentAddress {
		return "", false, "", errors.New("agent_send: this session is the coordinator — it has no parent; address a child by its harp")
	}
	known := false
	c.runs.View(func() { known = c.runsF.currentRun(to) != nil })
	if !known {
		return "", false, "", fmt.Errorf("agent_send: unknown recipient %q: not a child of this session (spawn it with agent_run first)", to)
	}
	c.audit("agent_send", caller.Harp, map[string]string{"to": to, "kind": kind})
	msgID, delivered, err := c.queueMailPayload(caller.Harp, to, kind, body, structured, inReplyTo)
	if err != nil {
		return "", false, "", err
	}
	if delivered {
		return msgID, true, "completed the child's waiting agent_recv", nil
	}
	_, prose := deliveryDisposition(c.driveQueued(to))
	return msgID, false, prose, nil
}

// deliveryDisposition classifies ONE §6a delivery-by-state outcome — the state
// the delivery observed (driveQueued's return) — into the two vocabularies the
// coordinator answers in: the typed mode Inject reports to the TUI, and the
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
	case StateIdle:
		return DeliveryNewTurn, "delivering as a new turn"
	case StateQueued:
		return DeliveryQueued, "queued: the child has not started yet; it will drain its mailbox after its first turn"
	default: // executing / parked race
		return DeliveryQueued, "queued mid-turn: delivered at the child's next turn boundary"
	}
}

// AgentRecv drains the caller's own durable mailbox, parking up to wait.
// Delivery is AT-LEAST-ONCE: this call implicitly acknowledges the messages a
// PRIOR recv returned (cursor-ack); unacknowledged deliveries are re-delivered
// after a coordinator relaunch. Dupes are deduped on message id at the store.
func (c *Coordinator) AgentRecv(ctx context.Context, caller Identity, wait time.Duration) ([]Message, error) {
	c.audit("agent_recv", caller.Harp, nil)
	return c.recvMail(ctx, caller.Harp, wait)
}

// AgentStop kills a child run (KillRun semantics): the engine/container dies,
// the slot frees, the terminal is journaled, and the credential is revoked.
func (c *Coordinator) AgentStop(caller Identity, harp string) (string, error) {
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
	// Cancel the LAUNCH before anything else, and do it on BOTH paths below.
	// A stop that only ends the run record cannot stop a launcher: a stop can
	// land on an already-ended run (the retry loop's own terminal) with a
	// relaunch already armed behind it, report success, and leave the retry
	// loop spinning on indefinitely. This marks the harp stopped — so an
	// armed-but-not-yet-enqueued relaunch turns back — and cancels the context
	// of any attempt currently in flight, which a container prepare makes a
	// seconds-wide window.
	c.cancelLaunch(harp)
	if rec.Ended {
		return fmt.Sprintf("child %s had already ended (%s); any pending relaunch is cancelled", harp, rec.Cause), nil
	}
	c.audit("agent_stop", caller.Harp, map[string]string{"harp": harp, "run_id": rec.RunID})
	c.terminateRun(rec.RunID, CauseStopped, fmt.Sprintf("stopped by %s", caller.Harp))
	return fmt.Sprintf("stopped child %s; its execution slot is freed (a later agent_send resumes it as a fresh run)", harp), nil
}

// Inject delivers user-typed text into a child. It is now a thin wrapper over
// ControlSteer with a HUMAN initiator — same verb, one implementation — which
// is the Wave-D2 convergence: an attached, migrated target that advertises
// `steer` rides plane 2 and gets a correlated acknowledgement; everything else
// takes §5.6's mailbox route, which is this method's ORIGINAL behaviour,
// preserved as a strict superset so no target loses anything.
//
// The signature is deliberately unchanged (caller: run_terminal_ui.go): the
// TUI's contract is a Delivery* mode string, so the plane-2 SteerResult is
// mapped back onto that vocabulary here rather than pushed onto the caller.
//
// INVARIANT (decision O3), unchanged on both routes: the KindUserInjected
// mirror notice to the target's parent fires on EVERY successful injection — a
// coordinator's picture of its child never diverges without a trace.
func (c *Coordinator) Inject(harp, text string) (string, error) {
	out, err := c.ControlSteer(context.Background(), ControlInitiator{
		Kind: agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN,
	}, harp, text)
	if err != nil {
		// ErrNotInjectable is the TUI's typed refusal and must survive the
		// indirection; ControlSteer already wraps it.
		return "", err
	}
	if out.Fallback != "" {
		return out.Fallback, nil
	}
	return steerAppliedToDelivery(out.Applied), nil
}

// steerAppliedToDelivery maps a plane-2 acknowledgement onto the Delivery*
// vocabulary the viewer speaks. APPLIED_IMMEDIATE means the target was IDLE and
// a new turn started now — which is exactly DeliveryNewTurn — and
// APPLIED_NEXT_TURN means it was mid-turn, which is DeliveryQueued. There is no
// Delivery* value meaning "the target refused", so a rejection is reported as
// the queue it is NOT: it maps to DeliveryQueued only when the runner actually
// queued it, and REJECTED is surfaced as its own string so the TUI cannot
// print a success for a refusal.
func steerAppliedToDelivery(applied agentcoordpb.SteerResult_Applied) string {
	switch applied {
	case agentcoordpb.SteerResult_APPLIED_IMMEDIATE:
		return DeliveryNewTurn
	case agentcoordpb.SteerResult_APPLIED_NEXT_TURN:
		return DeliveryQueued
	case agentcoordpb.SteerResult_APPLIED_REJECTED:
		return DeliveryRejected
	default:
		return DeliveryQueued
	}
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

// D2 retired the agentbus-backed viewer socket entirely (BindSessionSocket,
// the per-owner-harp agent-bus.sock, Hub()): observe/roster/inject now ride
// ConsumerService (consumer.go) — a single coordinator-wide surface, not a
// per-session-owner socket — so no per-harp bind step exists anymore. Inject
// itself (below) is unchanged; it was always native to the coordinator, the
// socket was only ever one of two transports wrapping it.
