package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Home is the RUNNER's connection home: it owns the coordinator dial (one
// gRPC conn), the RunnerChannel lifecycle link (Hello/heartbeats/RunExited),
// and the RunChannel the runner-terminated MCP tools ride — plane-2 requests
// with reissue-after-reconnect idempotency, the plane-3 notice buffer behind
// the runner-LOCAL agent_recv, and plane-1 event emission (reports, mail
// consumption, park state) with cumulative-Ack tracking.
//
// Both channels reconnect with backoff. Requests survive a reconnect: the
// runner re-Hellos with its resume cursor, re-emits unacked events, REISSUES
// outstanding requests with the SAME request_id (the coordinator treats
// request_id as the idempotency key), and re-asserts its current park state.
type Home struct {
	cfg HomeConfig
	rep report.Reporter // HomeConfig.Reporter, or silence

	ctx    context.Context
	cancel context.CancelFunc

	conn *grpc.ClientConn

	mu     sync.Mutex
	stream grpc.BidiStreamingClient[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]
	sendMu sync.Mutex // serializes stream.Send (single-writer discipline)
	// everAttached records that the run channel's Hello was ACCEPTED at least
	// once. It is sticky by design: it separates "we never got through"
	// (unreachable) from "we got through, and the request is simply taking a
	// while" — a distinction the caller's error must preserve, and which a
	// momentarily-nil stream mid-reconnect must not erase.
	everAttached bool
	seq          uint64
	unacked      []*agentcoordpb.AgentEvent
	acked        uint64
	ackCh        chan struct{} // closed + replaced on every ack advance
	// redial is closed and replaced by Redial: the kick a loop in its backoff
	// wakes on (redialWake). Guarded by mu.
	redial chan struct{}
	// requests is the one BidiSession scaffold's correlation for the
	// requests this Home issues over RunChannel (its send queue is unused:
	// the stream reconnects, so frames are written directly under sendMu).
	requests coord.BidiSession[*agentcoordpb.AgentFrame, *agentcoordpb.AgentRequest, *agentcoordpb.CoordinatorResponse]

	buffer   []*agentcoordpb.PeerMessage
	consumed map[string]bool
	// returned are message ids handed to the harness by the LAST Recv,
	// not yet acknowledged: the NEXT Recv (cursor-ack) or a clean Close
	// emits their consumption fact. A crash before either re-delivers
	// (at-least-once; the safe direction).
	returned []string
	park     *homePark
	parked   bool
	// turnQ/turnPending are the ENGINE-HOST turn-delivery seam (§6a,
	// runner-side): once a hosted engine registers a sink (SetTurnSink), a
	// pushed PeerMessage with no recv parked is queued here in arrival
	// order and handed to the engine as a NEW TURN; the pump emits the
	// mail_consumed fact only AFTER the engine accepted it (at-least-once
	// preserved — a crash between notice and hand-off re-delivers).
	turnQ       chan *agentcoordpb.PeerMessage
	turnPending map[string]bool
	// acking holds the ids the engine has ACCEPTED whose consume-rename the
	// pump has not yet performed; ackWake is closed and replaced whenever
	// turnPending or acking shrinks. Together they let AwaitMailAcked answer
	// "is any of these still on its way to consumed/" without polling.
	acking  map[string]bool
	ackWake chan struct{}
	// exited is set once ReportRunExited has run for a spawned run: the
	// hosted engine is gone, and this runner must not sweep in/ again — mail
	// written for the harp's NEXT run would land in a dead sink and be
	// consumed out from under the run launched to answer it.
	exited atomic.Bool

	// terminalNudge is the SESSION-OWNER's delivery-by-state seam, the
	// counterpart to turnQ for a Home no engine ever registered a turn sink
	// on: deliverNotice's third case (neither a parked recv nor a turn sink)
	// buffers mail for a Recv that a terminal-driven engine never makes on
	// its own. Nil everywhere except the one wiring that owns a live PTY for
	// this run — the interactive path that builds a NewTerminalInjector and
	// threads its Wrap through as a func; deliverNotice fires it, unlocked,
	// whenever it buffers with nothing else to tell.
	terminalNudge func()

	// wake is the SESSION-OWNER's engine wake (SetWake): when set it takes
	// deliverNotice's third case instead of terminalNudge. wakeMu serialises
	// fireWake, so two notices cannot each see no wake outstanding and arm
	// two.
	wake   engine.Wake
	wakeMu sync.Mutex

	// spoolHandler is THE consumer for validated inbound spool doorbells
	// (SetSpoolDoorbellHandler), registered by startSpoolReactor. spoolDoorbell
	// counts what the doorbell deliberately does not retry — see
	// spooldoorbell.go.
	spoolHandler  coord.SpoolDoorbellHandler
	spoolDoorbell coord.SpoolDoorbellCounters
	// identity is this run's, bound ONCE: from the Launch the coordinator's
	// StartRun carries (BindIdentity, called by the engine host as it
	// drives), or at dial for the plugin-hosted owner (HomeConfig.Harp). Its
	// harp names this runner's spool; its depth gates the automatic turn
	// report (a depth-0 run has no parent to report to). Zero until bound:
	// the spool machinery sweeps and writes nothing for a run that does not
	// yet know which run it is.
	identity coord.Identity
	// spoolOut lends this harp's out/ writer: where this agent's sends go.
	spoolOut *coord.SpoolWriterCache
	// spoolRefs maps a delivered message's dedupe id to the in/ file it came
	// from, so the CONSUME-RENAME can happen at the existing acknowledgement
	// moments (the engine accepted the turn / a later Recv proved the harness
	// took the batch) rather than at read time. Renaming at read would convert
	// this path's at-least-once guarantee into at-most-once silently.
	spoolRefs map[string]spool.Ref
	// spoolIn serialises this runner's own in/ sweeps — see spoolReactor.
	spoolIn            *coord.SpoolReactor
	spoolDeliveryCount coord.SpoolDeliveryCounters
	// selfReported records that this run sent its parent a message during the
	// CURRENT turn, which suppresses the automatic turn report for that turn
	// (spoolturnresult.go) — the runner-side home of the no-double-delivery
	// rule the coordinator kept on childRt.selfReported. Read-and-cleared at
	// each turn boundary.
	selfReported bool

	link *RunnerLink
	// ownerLost is closed once, by runnerChannelLoop, when the runner has
	// waited OwnerLossWindow on an absent owner — see OwnerLost.
	ownerLost chan struct{}
	// ownerUp/present are the owner's presence: up exactly while the lifecycle
	// link is attached; present is closed while up (ownerPresent). turning and
	// awaiting are the waiting state the owner-loss clock reads (waitState);
	// waitChange is closed and replaced at each change of it. Guarded by mu.
	ownerUp    bool
	present    chan struct{}
	turning    bool
	awaiting   int
	waitChange chan struct{}

	// tracked owns Home's own background loops (runnerChannelLoop,
	// runChannelLoop, and one turnPump per hosted engine). Close/crash join it
	// so a runner-side teardown leaves no goroutine still touching h's state
	// (Home dispatches independently of the Coordinator's own group —
	// fakeSpawner.StartEngine's in-process Home/EngineHost pair in the coord
	// test suite is exactly this shape, and unblocked kill/crash is what the
	// crash-redelivery test's determinism depends on). SetTurnSink can dispatch
	// a fresh turnPump concurrently with crash()/Close(), which is why the seal
	// in trackedGroup is not theoretical here.
	tracked coord.TrackedGroup
}

// HomeConfig carries the spawn-injected coordinator trio plus the runner's
// self-description.
type HomeConfig struct {
	URL   string // CTXLOOM_COORD_URL
	Token string // CTXLOOM_COORD_CRED (held ONLY by the runner)
	// RunID is CTXLOOM_RUN_ID. Empty on the plugin-hosted session-owner
	// credential (that runner hosts no run of its own). NON-EMPTY on a
	// container top-level session's owned run (StartOwnedRun mints one for
	// it, reusing the owner's own harp as its run role) as well as on every
	// delegated child — both are runs the coordinator tracks and can StartRun
	// on.
	RunID   string
	Harness string
	Version string
	// Engine answers coordinator-initiated RunnerRequests on the
	// RunnerChannel (StartRun foremost) — the runner's engine-control seam.
	// Nil when this Home hosts no run at all (a plugin-hosted session-owner's
	// Home, RunID == ""); a Home whose RunID is set — a delegated child OR a
	// container top-level session's owned run — always carries one once the
	// caller wires it (llm_serve.go).
	Engine RunnerRequestHandler
	// Capabilities is this runner's Hello advertisement: what its hosted engine
	// can actually execute (RunnerCapabilities). Empty advertises nothing —
	// the mailbox surface every runner has is not a capability.
	Capabilities []string
	// Harp is set for the session owner's plugin-hosted runner ALONE: no
	// StartRun ever reaches it, so its harp — the name of its spool — rides
	// the process env and binds at dial, at depth 0. A hosted run leaves it
	// empty and binds its identity from the Launch (BindIdentity).
	Harp string
	// Mapper resolves spool references to paths on this runner's side — the
	// one mapper every spool read and write here goes through. Nil is the
	// home-relative mapper.
	Mapper spool.PathMapper
	// SpoolSweepInterval overrides the spool reconciliation cadence (0 = the
	// built-in spoolSweepInterval) — see coord.Options.SpoolSweepInterval.
	SpoolSweepInterval time.Duration
	// RedialBackoff paces reconnect attempts for both channels (0 =
	// HomeRedialBackoff). Redial preempts it.
	RedialBackoff time.Duration
	// OwnerLossWindow is how long this runner WAITS on an absent owner —
	// the lifecycle link never attached, or lost — before it declares the
	// owner gone (OwnerLost). Only waiting counts (see runnerChannelLoop): a
	// turn making progress pauses it. 0 = DefaultOwnerLossWindow.
	OwnerLossWindow time.Duration
	// Reporter receives every diagnostic this Home and the courier, doorbell
	// and terminal injector built on it raise; the runner's composition
	// chooses the sink. Nil discards.
	Reporter report.Sink
}

type homePark struct {
	ch   chan []*agentcoordpb.PeerMessage // nil payload = preempted
	done bool
}

// HomeRedialBackoff is the default HomeConfig.RedialBackoff.
const HomeRedialBackoff = 2 * time.Second

// DefaultOwnerLossWindow is how long a runner waits on an unreachable
// coordinator before it exits on its own (HomeConfig.OwnerLossWindow;
// sessions.EnvRunnerOwnerLossWindow overrides it). Time a turn spends making
// progress does not count, so there is no cap on an orphaned turn: it runs
// to its end, and the wait starts there.
//
// It is NOT the coordinator's runner-loss grace (coord's runnerLossTimeout), and
// must not be tied to it: that grace is what a LIVE coordinator gives a silent
// runner, and what a RESTARTED one gives a runner to re-Hello once it is back.
// A coordinator that is down ends nothing, and a restarted one re-adopts any
// runner that dials back, however long the restart took — so the only cost of
// a long window is a truly orphaned container living that long, while a short
// one kills every live child of a coordinator that is merely slow to restart.
const DefaultOwnerLossWindow = 2 * time.Minute

// ErrCoordinatorUnreachable answers a plane-2 request that never got through:
// the run channel has not once attached, so nothing was delivered and a retry
// is free. A request the coordinator ACCEPTED does not fail with this — see
// requestFailure, which keeps the two apart.
var ErrCoordinatorUnreachable = errors.New("coordinator unreachable (the runner keeps reconnecting; retry, or finish standalone)")

// Attached reports whether the run channel's Hello has ever been accepted.
func (h *Home) Attached() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.everAttached
}

// requestFailure names why a plane-2 request ended without a response, and the
// distinction is the whole point: an unattached channel means the request was
// never delivered, while an attached one means the coordinator TOOK it and is
// still working — its handler runs on the coordinator's base context, so the
// caller giving up neither stops it nor undoes it. Collapsing the second case
// into "unreachable" reads as an outage and invites exactly the wrong reflex:
// an immediate retry that duplicates work already in flight.
func (h *Home) requestFailure(ctx context.Context, waited time.Duration) error {
	if !h.Attached() {
		return ErrCoordinatorUnreachable
	}
	return fmt.Errorf("%w after %s: the coordinator accepted this request and may still be running it — "+
		"it is not cancelled, and retrying starts a second run alongside it", ctx.Err(), waited.Round(time.Second))
}

// NewHome dials the coordinator and starts both channel loops. It never
// fails hard: an unreachable coordinator leaves the loops reconnecting and
// the tool verbs failing fast with ErrCoordinatorUnreachable — the
// coordinator's runner-loss synthesis covers the lifecycle either way.
func NewHome(ctx context.Context, cfg HomeConfig) (*Home, error) {
	target, err := grpcTarget(cfg.URL)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target, coordDialOptions(cfg.Token)...)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithCancel(ctx)
	h := &Home{
		cfg:         cfg,
		rep:         report.To(cfg.Reporter),
		ctx:         hctx,
		cancel:      cancel,
		conn:        conn,
		ackCh:       make(chan struct{}),
		redial:      make(chan struct{}),
		requests:    coord.NewBidiSession[*agentcoordpb.AgentFrame, *agentcoordpb.AgentRequest, *agentcoordpb.CoordinatorResponse](cancel, 0),
		consumed:    make(map[string]bool),
		turnPending: make(map[string]bool),
		acking:      make(map[string]bool),
		ackWake:     make(chan struct{}),
		ownerLost:   make(chan struct{}),
		present:     make(chan struct{}),
		waitChange:  make(chan struct{}),
	}
	// A runner writes exactly ONE spool: its own harp's out/. The cache is
	// still keyed by harp because spoolWriterCache is shared with the
	// coordinator's half, which serves many; the writer id is the harp,
	// stamped at bind.
	if h.cfg.Mapper == nil {
		h.cfg.Mapper = spool.NewHomeMapper()
	}
	if h.cfg.RedialBackoff == 0 {
		h.cfg.RedialBackoff = HomeRedialBackoff
	}
	if h.cfg.OwnerLossWindow == 0 {
		h.cfg.OwnerLossWindow = DefaultOwnerLossWindow
	}
	h.spoolOut = coord.NewSpoolWriterCache(h.cfg.Mapper, spool.DirOut, "")
	h.spoolRefs = make(map[string]spool.Ref)
	h.startSpoolReactor()
	if cfg.Harp != "" {
		h.BindIdentity(coord.Identity{Harp: cfg.Harp})
	}
	h.goTracked(h.runnerChannelLoop)
	h.goTracked(h.runChannelLoop)
	return h, nil
}

// ErrIdentityUnbound refuses a send from a runner whose identity is not yet
// bound: it does not know whose spool it writes. In production the engine
// that would send is started by the drive that binds, so this is a
// protocol slip, not a state a healthy runner passes through.
var ErrIdentityUnbound = errors.New("runner: this runner's identity is not bound yet; it cannot send")

// BindIdentity binds this run's identity ONCE — the harp its spool is named
// by, the depth its turn report is gated on — from the Launch the
// coordinator's StartRun carried (the engine host binds as it drives). A
// second bind is refused silently: a live runner's spool cannot be renamed
// under it. Binding wakes the startup sweep, so mail written while the run
// was coming up is delivered as turns.
func (h *Home) BindIdentity(id coord.Identity) {
	h.mu.Lock()
	if h.identity.Harp != "" || id.Harp == "" {
		h.mu.Unlock()
		return
	}
	h.identity = id
	h.spoolOut.SetWriterID(id.Harp)
	h.mu.Unlock()
	h.SweepSpoolIn()
}

// Harp is this run's session harp — the name of its spool — "" until the
// identity is bound.
func (h *Home) Harp() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.identity.Harp
}

// Depth is this run's delegation depth: 0 is the session owner's own run,
// which has no parent, so its automatic turn report has nobody to go to
// (ReportTurnResult). A report written to "parent" from depth 0 would be
// refused and the refusal mailed back to the run as its next turn.
func (h *Home) Depth() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.identity.Depth
}

// RunID is the run this Home hosts ("" for a session owner's runner).
func (h *Home) RunID() string { return h.cfg.RunID }

// Capabilities is this runner's Hello advertisement.
func (h *Home) Capabilities() []string { return h.helloCapabilities() }

// Done is closed once this Home has been closed or crashed: the moment its
// loops stop and its spool is no longer its own.
func (h *Home) Done() <-chan struct{} { return h.ctx.Done() }

// OwnerLost is closed when this runner has waited the whole OwnerLossWindow
// on an owner that never came back.
// The lifecycle loop stops redialling at that moment; tearing the Home down
// (Close) is its owner's call — runner.Main makes it, and its process exit is
// what lets a container's --rm remove the container.
func (h *Home) OwnerLost() <-chan struct{} { return h.ownerLost }

// EmittedSeq is the seq of the last event this Home emitted (0 before any).
func (h *Home) EmittedSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// goTracked runs fn on a new goroutine Close/crash join — see trackedGroup.
func (h *Home) goTracked(fn func()) { h.tracked.Dispatch(fn) }

// homeCloseJoinBudget bounds Close/crash's wait for Home's tracked
// goroutines — see Coordinator's closeJoinBudget for the identical reasoning
// (every tracked loop here selects on h.ctx, already cancelled by the time
// waitTracked runs).
const homeCloseJoinBudget = 3 * time.Second

// waitTracked joins every h.goTracked goroutine, with a bounded escape.
func (h *Home) waitTracked() {
	h.tracked.Wait(homeCloseJoinBudget, "runner home close", "")
}

// Redial asks both channel loops to redial NOW rather than at the end of
// their backoff: the caller knows the endpoint is back (a coordinator
// restarted on the recorded endpoint; a rebind), and the runner's
// re-adoption should not cost it the backoff.
func (h *Home) Redial() {
	h.mu.Lock()
	close(h.redial)
	h.redial = make(chan struct{})
	h.mu.Unlock()
	// The run channel's conn keeps its own reconnect backoff; a kick that
	// left it waiting would redial into a conn that fails fast.
	h.conn.ResetConnectBackoff()
}

// redialWake is the pending kick: a channel closed and replaced by Redial,
// which a loop in its backoff selects on beside the timer. A loop takes it
// BEFORE it dials, not when the backoff starts: a kick that lands while a
// doomed dial is in flight must still cut the backoff that follows, or the
// caller who knows the endpoint is back pays the whole backoff anyway.
func (h *Home) redialWake() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.redial
}

// runnerChannelLoop keeps the lifecycle RunnerChannel alive (Hello +
// heartbeats + best-effort RunExited at Close), redialling for as long as the
// Home lives — mid-turn too, so the owner can re-adopt this runner at any
// moment — and runs the owner-loss clock (ownerClock) while the link is down.
// The clock spends its budget only while the runner is WAITING on its owner
// (Home.waiting): idle between turns, parked on a recv, or blocked on a
// coordinator-bound request. A turn making progress pauses it, so an
// orphaned turn always runs to its end and the clock starts where it stops.
// The budget is refilled on every drop. Nothing the loop waits on can outlast
// it — a dial is cut off at the budget left (dialLink), and the conns'
// keepalive ends a half-open link. Expiry closes ownerLost and ends the loop.
func (h *Home) runnerChannelLoop() {
	clock := ownerClock{budget: h.cfg.OwnerLossWindow}
	// backoff waits out one redial pause, running the clock, and reports
	// whether to go on.
	backoff := func(wake <-chan struct{}) bool {
		pause := time.After(h.cfg.RedialBackoff)
		for {
			waiting, changed := h.waitState()
			now := time.Now()
			clock.observe(waiting, now)
			var expire <-chan time.Time
			if waiting {
				expire = time.After(clock.left(now))
			}
			select {
			case <-expire:
				h.rep.Warnf("runner: waited %s for an absent coordinator (the owner-loss window, %s); exiting", h.cfg.OwnerLossWindow, sessions.EnvRunnerOwnerLossWindow)
				close(h.ownerLost)
				return false
			case <-changed:
			case <-h.ctx.Done():
				return false
			case <-pause:
				return true
			case <-wake:
				return true
			}
		}
	}
	for {
		wake := h.redialWake()
		waiting, _ := h.waitState()
		now := time.Now()
		clock.observe(waiting, now)
		link, release, err := h.dialLink(clock.left(now))
		if err != nil {
			h.rep.WarnOncef("runner dial-home failed (reconnecting; the coordinator synthesizes loss meanwhile): %v", err)
			if !backoff(wake) {
				return
			}
			continue
		}
		h.mu.Lock()
		h.link = link
		h.mu.Unlock()
		h.setOwnerPresent(true)
		select {
		case <-link.Done():
			h.setOwnerPresent(false)
			// The dead link's connection is closed HERE, before the redial
			// replaces it: Shutdown only ever reaches the link Close finds
			// current, so an unreleased predecessor is a leaked ClientConn.
			h.releaseLink(link)
			release()
			clock = ownerClock{budget: h.cfg.OwnerLossWindow}
			if !backoff(wake) {
				return
			}
		case <-h.ctx.Done():
			h.releaseLink(link)
			release()
			return
		}
	}
}

// ownerClock is the owner-loss window's budget, spent only while the runner
// waits on its owner. observe is told the current state at every change; left
// is what remains. A paused clock keeps its budget.
type ownerClock struct {
	budget  time.Duration
	running bool
	since   time.Time
}

func (c *ownerClock) observe(waiting bool, now time.Time) {
	if c.running {
		c.budget -= now.Sub(c.since)
	}
	c.running, c.since = waiting, now
}

func (c *ownerClock) left(now time.Time) time.Duration {
	if c.running {
		return c.budget - now.Sub(c.since)
	}
	return c.budget
}

// waiting reports whether the runner is waiting on its owner — the only state
// the owner-loss clock runs in: no turn in progress, or a turn that is parked
// on a recv or blocked on a coordinator-bound request (Home.Request). changed
// is closed at the next change of that state. Caller need not hold mu.
func (h *Home) waitState() (waiting bool, changed <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.turning || h.awaiting > 0 || h.parked, h.waitChange
}

// noteWaitLocked signals a change in the waiting state. Caller holds mu.
func (h *Home) noteWaitLocked() {
	close(h.waitChange)
	h.waitChange = make(chan struct{})
}

// setTurning records a turn starting or reaching its boundary (the engine
// host's beginTurn/endTurn).
func (h *Home) setTurning(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turning != on {
		h.turning = on
		h.noteWaitLocked()
	}
}

// ownerPresent is closed while the owner is present — the lifecycle link is
// attached — and a fresh, open channel while it is not. It is what "let it
// finish, then wait" reads: a turn in flight when the link drops runs to its
// boundary (its events are kept and resent on re-adoption), but no NEW turn
// starts until the owner is back (EngineHost.enqueueTurn, deliverNotice's
// wake and nudge). Mail keeps arriving on the mounted spool meanwhile — the
// periodic and turn-boundary sweeps still read it — and simply waits.
func (h *Home) ownerPresent() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.present
}

// setOwnerPresent records the owner arriving or leaving. Arriving re-fires the
// session owner's wake or nudge when mail was buffered while it was away,
// because the notice that buffered it did not.
func (h *Home) setOwnerPresent(up bool) {
	h.mu.Lock()
	if up == h.ownerUp {
		h.mu.Unlock()
		return
	}
	h.ownerUp = up
	if !up {
		h.present = make(chan struct{})
		h.mu.Unlock()
		return
	}
	close(h.present)
	buffered := len(h.buffer) > 0
	wake, nudge := h.wake, h.terminalNudge
	h.mu.Unlock()
	if !buffered {
		return
	}
	if wake != nil {
		go h.fireWake(wake)
	} else if nudge != nil {
		nudge()
	}
}

// errDialOutlivedWindow is a dial the owner-loss window ran out on.
var errDialOutlivedWindow = errors.New("runner: the dial home outlived the owner-loss window")

// dialLink dials the lifecycle link with at most within to complete the
// handshake: a coordinator that accepts the connection and never answers the
// Hello would otherwise hold the loop — and the owner-loss clock — forever.
// The link lives under the dial's context, so release (which the caller runs
// once the link is done) is what frees it; a dial cut off at the bound hands
// back nothing.
func (h *Home) dialLink(within time.Duration) (*RunnerLink, func(), error) {
	if within <= 0 {
		return nil, nil, errDialOutlivedWindow
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cutoff := time.AfterFunc(within, cancel)
	link, err := DialRunner(ctx, h.rep, h.cfg.URL, h.cfg.Token, h.cfg.RunID, h.cfg.Harness, h.cfg.Version, h.cfg.Engine)
	if !cutoff.Stop() {
		if link != nil {
			link.Abort()
		}
		cancel()
		return nil, nil, errDialOutlivedWindow
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return link, cancel, nil
}

// runChannelLoop keeps the RunChannel alive: Hello/HelloAck, reissue of
// unacked events + outstanding requests + park state, then the receive loop.
func (h *Home) runChannelLoop() {
	client := agentcoordpb.NewCoordinatorServiceClient(h.conn)
	for {
		if h.ctx.Err() != nil {
			return
		}
		wake := h.redialWake()
		if err := h.runChannelOnce(client); err != nil && h.ctx.Err() == nil {
			h.rep.WarnOncef("run channel down (reconnecting): %v", err)
		}
		select {
		case <-time.After(h.cfg.RedialBackoff):
		case <-wake:
		case <-h.ctx.Done():
			return
		}
	}
}

func (h *Home) runChannelOnce(client agentcoordpb.CoordinatorServiceClient) error {
	stream, err := client.RunChannel(h.ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	resume := h.acked
	h.mu.Unlock()
	if err := stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Hello{Hello: &agentcoordpb.Hello{
		RunId:           h.cfg.RunID,
		ResumeFromSeq:   resume,
		ProtocolVersion: 1,
		Capabilities:    h.helloCapabilities(),
	}}}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := first.GetHelloAck()
	if ack == nil {
		return errors.New("first CoordinatorFrame was not a HelloAck")
	}
	if !ack.GetAccepted() {
		return rejectedHelloError("run channel Hello", ack.GetRejectReason())
	}

	// Attach, then REISSUE: unacked events in order, outstanding requests
	// with their ORIGINAL request_ids, and a fresh park assertion when a
	// recv is parked (park state is runtime state the coordinator forgot
	// with the old stream).
	h.sendMu.Lock()
	h.mu.Lock()
	h.stream = stream
	h.everAttached = true
	events := append([]*agentcoordpb.AgentEvent(nil), h.unacked...)
	parked := h.parked
	h.mu.Unlock()
	// The reissue batch holds sendMu so no fresh emit can interleave a
	// higher seq into it (see emitEvent).
	for _, ev := range events {
		h.sendLocked(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
	}
	for _, r := range h.requests.Outstanding() {
		h.sendLocked(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: r}})
	}
	h.sendMu.Unlock()
	// RECONNECT SWEEP: every doorbell rung while this stream was down was
	// dropped by design (the file was the truth, so nothing needed reissuing),
	// and this is the moment that costs nothing to make good.
	h.SweepSpoolIn()
	if parked {
		h.emitCustomEvent(coord.CustomRecvParked, nil)
	}

	defer func() {
		h.mu.Lock()
		if h.stream == stream {
			h.stream = nil
		}
		h.mu.Unlock()
	}()
	for {
		frame, rerr := stream.Recv()
		if rerr != nil {
			return rerr
		}
		h.handleCoordinatorFrame(frame)
	}
}

// helloCapabilities is this runner's advertisement, re-sent on every reconnect
// because the coordinator forgets it with the old stream.
func (h *Home) helloCapabilities() []string {
	return append([]string(nil), h.cfg.Capabilities...)
}

// send writes one frame on the current stream under the single-writer
// mutex. A nil/absent stream drops the frame — events sit in unacked and
// requests in pending, both reissued on reconnect.
func (h *Home) send(frame *agentcoordpb.AgentFrame) { _ = h.trySend(frame) }

// trySend is send with an answer: false means the frame did NOT reach the
// stream, either because there is none or because the write failed.
//
// The reissuing senders ignore that answer — their own buffers cover them —
// but the spool doorbell has no buffer BY DESIGN, so it is the one caller that
// must be able to count what it dropped instead of letting a permanently down
// channel look like a permanently quiet one.
func (h *Home) trySend(frame *agentcoordpb.AgentFrame) bool {
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	return h.sendLocked(frame)
}

// sendLocked is trySend's body for a caller already holding sendMu — the
// emitters that must pair a seq assignment with the write under one guard.
func (h *Home) sendLocked(frame *agentcoordpb.AgentFrame) bool {
	h.mu.Lock()
	stream := h.stream
	h.mu.Unlock()
	if stream == nil {
		return false
	}
	if err := stream.Send(frame); err != nil {
		// The receive loop observes the same failure and re-dials.
		h.rep.WarnOncef("run channel send failed (reconnecting): %v", err)
		return false
	}
	return true
}

// handleCoordinatorFrame dispatches one inbound coordinator frame.
func (h *Home) handleCoordinatorFrame(frame *agentcoordpb.CoordinatorFrame) {
	switch kind := frame.GetKind().(type) {
	case *agentcoordpb.CoordinatorFrame_Ack:
		h.advanceAck(kind.Ack.GetCommittedSeq())
	case *agentcoordpb.CoordinatorFrame_Response:
		h.requests.Resolve(kind.Response.GetRequestId(), kind.Response)
	case *agentcoordpb.CoordinatorFrame_Notice:
		if sc := kind.Notice.GetSpoolChanged(); sc != nil {
			h.handleSpoolChanged(sc)
		}
	case *agentcoordpb.CoordinatorFrame_HelloAck:
		// Duplicate ack on a live stream; ignore.
	}
}

// advanceAck moves the cumulative watermark, drops acked events, and wakes
// Report waiters.
func (h *Home) advanceAck(seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if seq <= h.acked {
		return
	}
	h.acked = seq
	kept := h.unacked[:0]
	for _, ev := range h.unacked {
		if ev.GetSeq() > seq {
			kept = append(kept, ev)
		}
	}
	h.unacked = kept
	close(h.ackCh)
	h.ackCh = make(chan struct{})
}

// deliverNotice routes one pushed PeerMessage (deduped on message_id against
// the buffer, the turn queue, and consumption history) by the runner's state
// — the §6a delivery-by-state seam, runner side:
//
//  1. a PARKED recv → complete it (the harness is actively polling);
//  2. no park but a hosted ENGINE registered a turn sink → queue for
//     delivery as a NEW TURN (arrival order; the pump below);
//  3. neither → buffer for a future recv (pre-engine window, or a
//     shim-only child without a hosted engine) AND fire terminalNudge, if
//     one is registered — the session owner's only way to learn mail
//     arrived, since nothing here will ever call Recv on its own.
func (h *Home) deliverNotice(pm *agentcoordpb.PeerMessage) {
	h.mu.Lock()
	if h.consumed[pm.GetMessageId()] || h.turnPending[pm.GetMessageId()] {
		h.mu.Unlock()
		return
	}
	for _, b := range h.buffer {
		if b.GetMessageId() == pm.GetMessageId() {
			h.mu.Unlock()
			return
		}
	}
	if p := h.park; (p == nil || p.done) && h.turnQ != nil {
		h.turnPending[pm.GetMessageId()] = true
		q := h.turnQ
		h.mu.Unlock()
		select {
		case q <- pm:
		case <-h.ctx.Done():
		}
		return
	}
	h.buffer = append(h.buffer, pm)
	p := h.park
	var msgs []*agentcoordpb.PeerMessage
	if p != nil && !p.done {
		p.done = true
		h.park = nil
		msgs = h.buffer
		h.buffer = nil
	}
	nudge := h.terminalNudge
	wake := h.wake
	up := h.ownerUp
	h.mu.Unlock()
	if msgs != nil {
		p.ch <- msgs
		return
	}
	if !up {
		return // no new turn while the owner is away; its return re-fires (setOwnerPresent)
	}
	// Nothing claimed it: no parked recv (checked above) and no turn sink
	// (the branch above this block already ruled that out). A terminal-driven
	// engine has no structural way to be handed a new turn, so this is its
	// only notification. A registered wake supersedes the nudge: the two
	// would each start a turn for the same mail.
	if wake != nil {
		go h.fireWake(wake)
	} else if nudge != nil {
		nudge()
	}
}

// SetWake registers the session owner's engine wake (engine.WakeSpec, bound
// once for this session). One per Home, as SetTerminalNudge: a second
// registration is refused and reported, and the first stays bound.
func (h *Home) SetWake(w engine.Wake) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wake != nil {
		h.rep.FailOncef(report.KindConfig,
			"bind the engine's wake ONCE per session and register it once",
			"runner: a wake is already registered for this run; the second registration is refused")
		return
	}
	h.wake = w
}

// fireWake owns what a wake needs that is not the engine's: it fires only
// while the owner's in/ holds unclaimed mail, only when no earlier wake is
// still unanswered (that wake's hook drains everything), and only after the
// nonce is armed on disk. A wake that fails never went out, so its nonce is
// disarmed and the next notice may try again.
func (h *Home) fireWake(w engine.Wake) {
	h.wakeMu.Lock()
	defer h.wakeMu.Unlock()
	m, harp := h.cfg.Mapper, h.Harp()
	if harp == "" {
		return
	}
	if pending, err := spool.Pending(m, harp); err != nil || !pending {
		if err != nil {
			h.rep.Warnf("runner: cannot tell whether the session owner has mail, so it is not woken: %v", err)
		}
		return
	}
	if out, err := spool.OutstandingWake(m, harp); err != nil || len(out) > 0 {
		if err != nil {
			h.rep.Warnf("runner: cannot list the session owner's outstanding wakes, so it is not woken: %v", err)
		}
		return
	}
	nonce, err := spool.ArmWake(m, harp)
	if err != nil {
		h.rep.Warnf("runner: cannot arm a wake for the session owner: %v", err)
		return
	}
	if err := w.Fire(h.ctx, nonce); err != nil {
		if _, derr := spool.ConsumeWake(m, harp, nonce); derr != nil {
			h.rep.Warnf("runner: a wake that did not fire could not be disarmed, and blocks later wakes: %v", derr)
		}
		h.rep.Warnf("runner: the session owner was not woken; its mail waits for the next prompt: %v", err)
	}
}

// SetTerminalNudge registers the session-owner's terminal-injection hook: it
// fires, unlocked and asynchronously with respect to the caller, every time
// deliverNotice's third case buffers a message with no parked recv and no
// turn sink to hand it to. fn must not block — it runs on the same goroutine
// that just received the coordinator's push, so a blocking fn stalls every
// later notice on this Home.
//
// One per Home, mirroring SetTurnSink: a run drives at most one terminal, and
// a second registration almost certainly means two engines think they own
// it, so it is refused rather than silently replacing the first.
func (h *Home) SetTerminalNudge(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.terminalNudge != nil {
		// A warning here is not enough: the refusal leaves the FIRST
		// registration bound to a stdin that turn has since abandoned, so the
		// session owner is never told mail arrived and every gate stays green
		// — the "succeeds without doing the thing" shape. Report it and let
		// strictness decide fatality.
		h.rep.FailOncef(report.KindConfig,
			"construct ONE TerminalInjector per Home and call Wrap on it once per turn (see llm_serve.go) instead of building a new injector for each turn",
			"runner: a terminal nudge is already registered for this run; the second registration is refused, which silently disables the session owner's mail wake")
		return
	}
	h.terminalNudge = fn
}

// BufferedMailCount reports how many messages are sitting in the recv buffer
// with no route to their recipient (deliverNotice's third case). The
// terminal injector reads this AT INJECTION TIME rather than at arrival
// time, so a burst that coalesces into one nudge reports the count as it
// stands when the frame is actually written, not the count when the first
// message of the burst arrived.
// RecvParked reports whether a receive is currently parked on this Home.
func (h *Home) RecvParked() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.parked
}

func (h *Home) BufferedMailCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.buffer)
}

// turnQueueCap bounds the engine turn-delivery queue. Far above any realistic
// mailbox depth; enqueue blocks (never drops) if ever reached.
const turnQueueCap = 256

// SetTurnSink registers a hosted engine's turn-delivery seam: sink hands one
// coordinator-delivered message to the engine as a new turn, returning
// whether the engine accepted it (false = engine gone; the message returns
// to the buffer). Already-buffered messages (queued before the engine
// started) drain through the sink first, in arrival order. One sink per
// Home — a runner hosts one run, so a second registration is a wiring bug: the
// FIRST sink keeps the queue and the second engine would sit turnless forever,
// which is why the refusal warns instead of returning quietly.
func (h *Home) SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool) {
	h.mu.Lock()
	if h.turnQ != nil {
		h.mu.Unlock()
		h.rep.Warnf("runner: a turn sink is already registered for this run; the second registration is refused and that engine will receive no turns")
		return
	}
	q := make(chan *agentcoordpb.PeerMessage, turnQueueCap)
	for _, pm := range h.buffer {
		h.turnPending[pm.GetMessageId()] = true
		q <- pm // fresh buffered channel: never blocks (buffer << cap)
	}
	h.buffer = nil
	h.turnQ = q
	h.mu.Unlock()
	h.goTracked(func() { h.turnPump(q, sink) })
}

// turnPump serializes turn deliveries (one at a time, arrival order) and
// emits the mail_consumed fact only AFTER the engine accepted the message —
// the turn path's analog of the recv cursor-ack: a runner crash between the
// pushed notice and the hand-off re-delivers on reattach (at-least-once,
// deduped on message_id at both ends).
func (h *Home) turnPump(q <-chan *agentcoordpb.PeerMessage, sink func(*agentcoordpb.PeerMessage) bool) {
	for {
		select {
		case pm := <-q:
			ok := sink(pm)
			id := pm.GetMessageId()
			h.mu.Lock()
			delete(h.turnPending, id)
			if ok {
				h.consumed[id] = true
				h.acking[id] = true
			} else {
				h.buffer = append(h.buffer, pm) // engine gone: back to the recv buffer
				h.wakeAckWaitersLocked()
			}
			h.mu.Unlock()
			if ok {
				// THE ACK, whichever substrate carried it (mailConsumed):
				// emitted only now, because "the engine accepted the turn" is
				// the earliest moment the delivery is real.
				h.ackMailConsumed([]string{id})
				h.mu.Lock()
				delete(h.acking, id)
				h.wakeAckWaitersLocked()
				h.mu.Unlock()
			}
		case <-h.ctx.Done():
			return
		}
	}
}

// wakeAckWaitersLocked releases every AwaitMailAcked caller to re-check.
// Caller holds h.mu.
func (h *Home) wakeAckWaitersLocked() {
	close(h.ackWake)
	h.ackWake = make(chan struct{})
}

// AwaitMailAcked blocks until none of ids is still on its way to consumed/:
// neither queued for the engine (turnPending) nor accepted with its
// consume-rename in flight (acking). An id this runner never delivered, or
// one already acked, needs no wait. Bounded by ctx.
//
// The engine host calls this before it reports RunExited, for the turns the
// engine actually took: the pump's ack for an accepted turn trails the
// engine's acceptance (transcript write, bookkeeping, rename), and an engine
// that exits on the turn it just accepted can otherwise have its exit reach
// the coordinator while the file is still in in/ — where terminateRun's
// leftover-mail tail reads it as unanswered and launches the harp again for
// a message that is already consumed by the time that run sweeps.
func (h *Home) AwaitMailAcked(ctx context.Context, ids []string) error {
	for {
		h.mu.Lock()
		inFlight := false
		for _, id := range ids {
			if h.turnPending[id] || h.acking[id] {
				inFlight = true
				break
			}
		}
		wake := h.ackWake
		h.mu.Unlock()
		if !inFlight {
			return nil
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// releaseLink aborts link and forgets it, if it is still the current one — a
// Close that already took it (h.link nil) owns its graceful Shutdown instead.
func (h *Home) releaseLink(link *RunnerLink) {
	h.mu.Lock()
	current := h.link == link
	if current {
		h.link = nil
	}
	h.mu.Unlock()
	if current {
		link.Abort()
	}
}

// ReportRunExited sends a best-effort RunExited on the live lifecycle link
// WITHOUT tearing the home down — the engine host's chat-ended signal (the
// runner process itself stays up until the coordinator kills it; the
// coordinator's loss synthesis covers a link that is down). It also retires
// this runner's in/ sweep (Home.exited): whether or not the frame gets out,
// the engine is gone and nothing here can take a turn.
func (h *Home) ReportRunExited(exitCode int, harnessSessionID string) {
	if h.cfg.RunID == "" {
		return // a session-owner runner hosts no spawned run
	}
	h.exited.Store(true)
	h.mu.Lock()
	link := h.link
	h.mu.Unlock()
	if link == nil {
		return
	}
	if err := link.send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_RunExited{
		RunExited: &agentcoordpb.RunExited{
			RunId:            h.cfg.RunID,
			ExitCode:         int32(exitCode),
			HarnessSessionId: harnessSessionID,
			// Always true: ReportRunExited is only ever called after the
			// engine's chat stream has ended (enginehost.go's adapt), which
			// is itself the terminal event. There is no call path where the
			// terminal event was not seen.
			TerminalEventSeen: true,
		},
	}}); err != nil {
		h.rep.Warnf("runner: report RunExited: %v (coordinator will synthesize loss)", err)
	}
}

// Request runs one plane-2 request to completion: correlated by request_id,
// reissued (same id) across reconnects, bounded by ctx or the default
// budget.
func (h *Home) Request(ctx context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
	if req.GetRequestId() == "" {
		req.RequestId = coord.RandID("req-", 12)
	}
	// agent_send is a LOCAL durable file write plus a doorbell, with no
	// coordinator round trip — so it also succeeds while the coordinator is
	// restarting, and the coordinator routes it when it sweeps.
	if resp, handled := h.sendPeerViaSpool(req); handled {
		return resp, nil
	}
	ch, ok := h.requests.Register(req.GetRequestId(), req)
	if !ok {
		return nil, ErrCoordinatorUnreachable
	}
	h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: req}})
	// Blocked on the coordinator from here: waiting, for the owner-loss clock.
	h.mu.Lock()
	h.awaiting++
	h.noteWaitLocked()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.awaiting--
		h.noteWaitLocked()
		h.mu.Unlock()
	}()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, coord.DefaultRequestTimeout)
		defer cancel()
	}
	started := time.Now()
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		h.requests.Withdraw(req.GetRequestId())
		return nil, h.requestFailure(ctx, time.Since(started))
	case <-h.ctx.Done():
		return nil, ErrCoordinatorUnreachable
	}
}

// Recv is the runner-LOCAL agent_recv: drain the notice buffer, or park
// against it for up to wait (one park; a newer receive preempts —
// ErrRecvPreempted). Returned messages stay TENTATIVE at the coordinator
// until acknowledged: this call first acks the PREVIOUS Recv's returned ids
// (cursor-ack — the closest observable point to "the engine actually
// received them": the harness calling again proves it got the last batch), and
// a clean Close acks the final batch. A crash before the ack re-delivers
// (at-least-once, deduped on message_id).
func (h *Home) Recv(ctx context.Context, wait time.Duration) ([]*agentcoordpb.PeerMessage, error) {
	h.ackReturned()
	h.mu.Lock()
	if len(h.buffer) > 0 {
		msgs := h.buffer
		h.buffer = nil
		h.mu.Unlock()
		h.recordReturned(msgs)
		return msgs, nil
	}
	if prev := h.park; prev != nil && !prev.done {
		// Newest preempts: the older poll completes with the typed error.
		prev.done = true
		prev.ch <- nil
	}
	p := &homePark{ch: make(chan []*agentcoordpb.PeerMessage, 1)}
	h.park = p
	wasParked := h.parked
	h.parked = true
	if !wasParked {
		h.noteWaitLocked()
	}
	h.mu.Unlock()
	if !wasParked {
		h.emitCustomEvent(coord.CustomRecvParked, nil)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case msgs := <-p.ch:
		if msgs == nil {
			// Preempted: the newer poll holds the park — no unpark event.
			return nil, coord.ErrRecvPreempted
		}
		h.unpark()
		h.recordReturned(msgs)
		return msgs, nil
	case <-timer.C:
		return nil, h.abandonPark(p, coord.ErrRecvTimeout)
	case <-ctx.Done():
		return nil, h.abandonPark(p, ctx.Err())
	case <-h.ctx.Done():
		return nil, h.abandonPark(p, ErrCoordinatorUnreachable)
	}
}

// recordReturned remembers a Recv's returned ids for the cursor-ack.
func (h *Home) recordReturned(msgs []*agentcoordpb.PeerMessage) {
	h.mu.Lock()
	for _, m := range msgs {
		h.consumed[m.GetMessageId()] = true // never re-deliver to this harness
		h.returned = append(h.returned, m.GetMessageId())
	}
	h.mu.Unlock()
}

// ackReturned emits the consumption fact for everything a prior Recv handed
// to the harness (the durable cursor advance).
func (h *Home) ackReturned() {
	h.mu.Lock()
	ids := h.returned
	h.returned = nil
	h.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	h.ackMailConsumed(ids)
}

// abandonPark resolves the timeout/cancel race against a delivery exactly
// like the coordinator's local poll: a delivery that already won is
// authoritative.
func (h *Home) abandonPark(p *homePark, err error) error {
	h.mu.Lock()
	if p.done {
		h.mu.Unlock()
		// The delivery (or a preemption, which sends nil — see
		// Recv's "newest preempts" branch) beat us to it. The caller is
		// leaving regardless (this Recv is returning err either way), but a
		// GENUINE delivery must not be silently dropped: put it back on the
		// buffer for the next Recv to pick up, exactly like this function's
		// own comment always claimed it would ("the delivery beat us; but
		// the caller is leaving — requeue") without ever actually doing it.
		if msgs := <-p.ch; len(msgs) > 0 {
			h.mu.Lock()
			h.buffer = append(msgs, h.buffer...)
			h.mu.Unlock()
		}
		return err
	}
	p.done = true
	if h.park == p {
		h.park = nil
	}
	h.mu.Unlock()
	h.unpark()
	return err
}

// unpark clears the park state and tells the coordinator (slot
// re-acquisition + closes the mail push window).
func (h *Home) unpark() {
	h.mu.Lock()
	was := h.parked
	h.parked = false
	if was {
		h.noteWaitLocked()
	}
	h.mu.Unlock()
	if was {
		h.emitCustomEvent(coord.CustomRecvUnparked, nil)
	}
}

// emitCustomEvent emits one ctxloom/* custom event on the event plane. An
// event whose value does not encode is DROPPED, not emitted valueless: every
// value-carrying member of this vocabulary IS its value (mail_consumed's
// message_ids are the consumption cursor, harness_session's id is the resume
// handle), so a valueless copy is a lie the coordinator would act on. Dropping
// it leaves the underlying state unacknowledged — for mail that re-delivers,
// the safe direction — and warns rather than passing silently.
func (h *Home) emitCustomEvent(name string, value map[string]any) {
	var v *structpb.Struct
	if value != nil {
		var err error
		v, err = structpb.NewStruct(value)
		if err != nil {
			h.rep.Warnf("runner: %s event value does not encode: %v (event dropped; the underlying state stays unacknowledged)", name, err)
			return
		}
	}
	h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{
		Name:  name,
		Value: v,
	}}})
}

// emitEvent assigns the next seq, buffers the event as unacked, and sends it
// when the stream is live. Returns the assigned seq.
//
// Seq assignment and the stream write are ONE critical section (sendMu is
// held across both). The coordinator's cumulative Ack dedupes by "seq at or
// below the channel's watermark", so an event reaching the wire behind a
// higher seq is dropped as a duplicate and acked past — and a Report waiting
// on that ack returns with its fact never journaled. Concurrent emitters
// (a Report on one goroutine, the engine host's park/turn events on another)
// must therefore serialize the whole assign-then-write, not just the assign.
func (h *Home) emitEvent(ev *agentcoordpb.AgentEvent) uint64 {
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	h.mu.Lock()
	h.seq++
	ev.Seq = h.seq
	ev.RunId = h.cfg.RunID
	ev.OccurredAt = timestamppb.Now()
	h.unacked = append(h.unacked, ev)
	seq := h.seq
	h.mu.Unlock()
	h.sendLocked(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
	return seq
}

// Report files a summary (+ artifact manifests) as plane-1 events and waits
// for the cumulative Ack to cover them — the coordinator fsyncs the facts
// before acking, so a returned Report is durably journaled.
// An empty report — no summary, no artifacts — is REFUSED: there is nothing to
// journal, so a nil return would assert durability for facts that were never
// filed.
func (h *Home) Report(ctx context.Context, summary *agentcoordpb.Summary, artifacts []*agentcoordpb.ArtifactProduced) error {
	if summary == nil && len(artifacts) == 0 {
		return errors.New("coord: report needs a summary or at least one artifact (nothing was filed)")
	}
	var last uint64
	for _, a := range artifacts {
		last = h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ArtifactProduced{ArtifactProduced: a}})
	}
	if summary != nil {
		last = h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Summary{Summary: summary}})
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, coord.DefaultRequestTimeout)
		defer cancel()
	}
	return h.awaitAck(ctx, last)
}

// awaitAck blocks until the cumulative Ack watermark covers seq — the point at
// which the coordinator has fsynced those facts. A quiet stretch re-issues the
// unacked events rather than waiting on in silence, because the watermark that would end
// this wait is droppable in flight (see reissueUnacked).
func (h *Home) awaitAck(ctx context.Context, seq uint64) error {
	started := time.Now()
	for {
		h.mu.Lock()
		acked := h.acked
		ch := h.ackCh
		h.mu.Unlock()
		if acked >= seq {
			return nil
		}
		prod := time.NewTimer(ackReissueInterval)
		select {
		case <-ch:
		case <-prod.C:
			h.reissueUnacked()
		case <-ctx.Done():
			prod.Stop()
			return h.requestFailure(ctx, time.Since(started))
		case <-h.ctx.Done():
			prod.Stop()
			return ErrCoordinatorUnreachable
		}
		prod.Stop()
	}
}

// ackReissueInterval paces the re-issue that recovers a LOST Ack. Long enough
// that a coordinator merely busy fsyncing is never re-prompted, short enough
// that nobody waits out the request budget for a watermark that will never come.
const ackReissueInterval = 2 * time.Second

// reissueUnacked re-sends every event still awaiting an Ack. The coordinator's
// ack send is deliberately non-blocking and DROPS the frame when its outbound
// buffer is full — cumulative watermarks make that safe only for a stream that
// keeps flowing. A waiter has nothing else in flight to carry the next
// watermark, so a dropped one is indistinguishable from "not durable yet";
// re-issuing is what resolves the two. It costs nothing: (run, seq) is the
// coordinator's idempotency key, so a seq it already processed is re-acked
// rather than re-journaled — the same reissue the post-Hello reattach performs.
//
// The whole batch goes out under sendMu: a fresh emit landing between two
// reissued events would put its higher seq ahead of the rest of the batch,
// and the coordinator would drop those as duplicates (see emitEvent).
func (h *Home) reissueUnacked() {
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	h.mu.Lock()
	events := append([]*agentcoordpb.AgentEvent(nil), h.unacked...)
	h.mu.Unlock()
	for _, ev := range events {
		h.sendLocked(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
	}
}

// Crash tears the home down WITHOUT the clean-shutdown acknowledgements —
// the runner-process death a killed container or a SIGKILL is, and the seam
// an in-process runner double's Kill uses (coordtest, and coord's own fake):
// a crash before the ack re-delivers, which is the at-least-once contract.
// Joins Home's own tracked loops (bounded) before returning so a caller
// wired as childRt.close can rely on Crash actually being done, not merely
// dispatched, before it proceeds (Coordinator.Close's attachment loop calls
// closeFn synchronously for exactly this reason), and closes the out/
// writer LAST so nothing lands after the join.
func (h *Home) Crash() {
	// Torn down means nothing here can take a turn: the sweep and the
	// consume-rename refuse from this point (a consume mkdirs its target,
	// and a late one would recreate a spool under a root the run is done
	// with). Marked BEFORE the join, so a caller the join gives up on still
	// sees it.
	h.exited.Store(true)
	h.tracked.Seal()
	h.cancel()
	_ = h.conn.Close()
	h.waitTracked()
	// The out/ writer closes with the run, mirroring the coordinator closing
	// its in/ writer with the journals (closePartial). Without this, a write
	// that loses the race with teardown still lands: the cache outlives every
	// tracked goroutine, so a late turn result or peer send creates a file in
	// a spool directory the run has finished with — observed as a test's
	// TempDir cleanup failing on a directory that filled up under it.
	// After waitTracked so an in-flight write completes rather than being
	// refused.
	h.spoolOut.Close()
}

// Close tears the home down: best-effort final cursor-ack (a CLEAN exit
// acknowledges what the harness already received — a crash skips this and
// re-delivers, the safe direction), best-effort RunExited on the lifecycle
// link, then both loops stop and the conn closes — joined (bounded) before
// returning, mirroring crash().
func (h *Home) Close(exitCode int, harnessSessionID string) {
	h.ackReturned()
	h.exited.Store(true) // see Crash: nothing here takes a turn past this point
	h.tracked.Seal()
	h.mu.Lock()
	link := h.link
	h.link = nil
	h.mu.Unlock()
	if link != nil {
		link.Shutdown(exitCode, harnessSessionID)
	}
	h.cancel()
	_ = h.conn.Close()
	h.waitTracked()
}
