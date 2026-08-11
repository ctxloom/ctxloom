package coord

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"

	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	pending      map[string]*homeReq

	buffer   []*agentcoordpb.PeerMessage
	consumed map[string]bool
	// returned are message ids handed to the harness by the LAST Recv,
	// not yet acknowledged: the NEXT Recv (cursor-ack) or a clean Close
	// emits their consumption fact. A crash before either re-delivers
	// (at-least-once; the safe direction).
	returned []string
	// parkedCtl is the UNPULLED-CONTROL-BODY ledger, oldest first: one entry
	// per control payload ParkControlPayload put into buffer, dropped the
	// moment Recv hands that id to the harness. It is what makes "the agent
	// never pulled the instruction" an OBSERVABLE state rather than something
	// only discoverable by rummaging in buffer — the engine host's
	// turn-boundary re-announcer reads it (PendingControlPayloads), and
	// nothing else does.
	//
	// Separate from buffer rather than derived from it because buffer holds
	// ordinary mail too (the pre-engine window), and "how long has the human's
	// instruction been sitting unread" is a question only about control bodies.
	parkedCtl []PendingControlPayload
	park      *homePark
	parked    bool
	// turnQ/turnPending are the ENGINE-HOST turn-delivery seam (§6a,
	// runner-side): once a hosted engine registers a sink (SetTurnSink), a
	// pushed PeerMessage with no recv parked is queued here in arrival
	// order and handed to the engine as a NEW TURN; the pump emits the
	// mail_consumed fact only AFTER the engine accepted it (at-least-once
	// preserved — a crash between notice and hand-off re-delivers).
	turnQ       chan *agentcoordpb.PeerMessage
	turnPending map[string]bool

	// ctrlHandler executes coordinator-initiated plane-2 control requests
	// (SetRequestHandler; EngineHost.BindHome registers itself). Nil means this
	// runner hosts no engine, and the Request arm answers UNIMPLEMENTED —
	// which is now a deliberate no-engine fallback rather than a window marker.
	ctrlHandler func(context.Context, *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse
	// inflightCtrl is the RESPONDER-side idempotency mirror of the
	// coordinator's reqTrack, keyed by request_id: a reissue that arrives
	// after a reconnect re-sends the SAME cached answer, and one that arrives
	// while the original dispatch is still running is joined to it rather than
	// starting a second execution. A control request consumes a child TURN, so
	// a double dispatch is not a wasted round trip — it is a second turn the
	// human never asked for.
	inflightCtrl map[string]*inflightCtrl

	link     *RunnerLink
	linkDone chan struct{} // closed when Close ran (stops the redial loops)

	// tracked owns Home's own background loops (runnerChannelLoop,
	// runChannelLoop, and one turnPump per hosted engine). Close/crash join it
	// so a runner-side teardown leaves no goroutine still touching h's state
	// (Home dispatches independently of the Coordinator's own group —
	// fakeSpawner.StartEngine's in-process Home/EngineHost pair in the coord
	// test suite is exactly this shape, and unblocked kill/crash is what the
	// crash-redelivery test's determinism depends on). SetTurnSink can dispatch
	// a fresh turnPump concurrently with crash()/Close(), which is why the seal
	// in trackedGroup is not theoretical here.
	tracked trackedGroup
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
	// can actually execute (RunnerCapabilities). Empty advertises
	// CapPeerMessaging alone — the mailbox surface every runner has.
	Capabilities []string
}

type homeReq struct {
	req *agentcoordpb.AgentRequest
	ch  chan *agentcoordpb.CoordinatorResponse
}

type homePark struct {
	ch   chan []*agentcoordpb.PeerMessage // nil payload = preempted
	done bool
}

// homeRedialBackoff paces reconnect attempts for both channels.
const homeRedialBackoff = 2 * time.Second

// defaultRequestTimeout bounds a plane-2 request when the tool call's ctx
// carries no deadline.
const defaultRequestTimeout = 60 * time.Second

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
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(cfg.Token)),
	)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithCancel(ctx)
	h := &Home{
		cfg:          cfg,
		ctx:          hctx,
		cancel:       cancel,
		conn:         conn,
		ackCh:        make(chan struct{}),
		pending:      make(map[string]*homeReq),
		consumed:     make(map[string]bool),
		turnPending:  make(map[string]bool),
		inflightCtrl: make(map[string]*inflightCtrl),
		linkDone:     make(chan struct{}),
	}
	h.goTracked(h.runnerChannelLoop)
	h.goTracked(h.runChannelLoop)
	return h, nil
}

// goTracked runs fn on a new goroutine Close/crash join — see trackedGroup.
func (h *Home) goTracked(fn func()) { h.tracked.dispatch(fn) }

// homeCloseJoinBudget bounds Close/crash's wait for Home's tracked
// goroutines — see Coordinator's closeJoinBudget for the identical reasoning
// (every tracked loop here selects on h.ctx, already cancelled by the time
// waitTracked runs).
const homeCloseJoinBudget = 3 * time.Second

// waitTracked joins every h.goTracked goroutine, with a bounded escape.
func (h *Home) waitTracked() {
	h.tracked.wait(homeCloseJoinBudget, "runner home close", "")
}

// runnerChannelLoop keeps the lifecycle RunnerChannel alive (Hello +
// heartbeats + best-effort RunExited at Close).
func (h *Home) runnerChannelLoop() {
	for {
		link, err := DialRunner(h.ctx, h.cfg.URL, h.cfg.Token, h.cfg.RunID, h.cfg.Harness, h.cfg.Version, h.cfg.Engine)
		if err != nil {
			clidiag.WarnOnce("ctxloom", "runner dial-home failed (reconnecting; the coordinator synthesizes loss meanwhile): %v", err)
			select {
			case <-time.After(homeRedialBackoff):
				continue
			case <-h.ctx.Done():
				return
			}
		}
		h.mu.Lock()
		h.link = link
		h.mu.Unlock()
		select {
		case <-link.Done():
			select {
			case <-time.After(homeRedialBackoff):
			case <-h.ctx.Done():
				return
			}
		case <-h.ctx.Done():
			return
		}
	}
}

// runChannelLoop keeps the RunChannel alive: Hello/HelloAck, reissue of
// unacked events + outstanding requests + park state, then the receive loop.
func (h *Home) runChannelLoop() {
	client := agentcoordpb.NewCoordinatorServiceClient(h.conn)
	for {
		if h.ctx.Err() != nil {
			return
		}
		if err := h.runChannelOnce(client); err != nil && h.ctx.Err() == nil {
			clidiag.WarnOnce("ctxloom", "run channel down (reconnecting): %v", err)
		}
		select {
		case <-time.After(homeRedialBackoff):
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
	h.mu.Lock()
	h.stream = stream
	h.everAttached = true
	events := append([]*agentcoordpb.AgentEvent(nil), h.unacked...)
	reqs := make([]*agentcoordpb.AgentRequest, 0, len(h.pending))
	for _, hr := range h.pending {
		reqs = append(reqs, hr.req)
	}
	parked := h.parked
	h.mu.Unlock()
	for _, ev := range events {
		h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
	}
	for _, r := range reqs {
		h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: r}})
	}
	if parked {
		h.emitCustomEvent(CustomRecvParked, nil)
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
// because the coordinator forgets it with the old stream. A runner whose config
// names none still advertises the mailbox surface it certainly has.
func (h *Home) helloCapabilities() []string {
	if len(h.cfg.Capabilities) == 0 {
		return []string{CapPeerMessaging}
	}
	return append([]string(nil), h.cfg.Capabilities...)
}

// send writes one frame on the current stream under the single-writer
// mutex. A nil/absent stream drops the frame — events sit in unacked and
// requests in pending, both reissued on reconnect.
func (h *Home) send(frame *agentcoordpb.AgentFrame) {
	h.mu.Lock()
	stream := h.stream
	h.mu.Unlock()
	if stream == nil {
		return
	}
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	if err := stream.Send(frame); err != nil {
		// The receive loop observes the same failure and re-dials.
		clidiag.WarnOnce("ctxloom", "run channel send failed (reconnecting): %v", err)
	}
}

// handleCoordinatorFrame dispatches one inbound coordinator frame.
func (h *Home) handleCoordinatorFrame(frame *agentcoordpb.CoordinatorFrame) {
	switch kind := frame.GetKind().(type) {
	case *agentcoordpb.CoordinatorFrame_Ack:
		h.advanceAck(kind.Ack.GetCommittedSeq())
	case *agentcoordpb.CoordinatorFrame_Response:
		h.mu.Lock()
		hr := h.pending[kind.Response.GetRequestId()]
		delete(h.pending, kind.Response.GetRequestId())
		h.mu.Unlock()
		if hr != nil {
			hr.ch <- kind.Response
		}
	case *agentcoordpb.CoordinatorFrame_Notice:
		if pm := kind.Notice.GetPeerMessage(); pm != nil {
			h.deliverNotice(pm)
		}
	case *agentcoordpb.CoordinatorFrame_Request:
		h.serveCoordinatorRequest(kind.Request)
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
//     shim-only child without a hosted engine).
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
	h.mu.Unlock()
	if msgs != nil {
		p.ch <- msgs
	}
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
		clidiag.Warn("ctxloom", "runner: a turn sink is already registered for this run; the second registration is refused and that engine will receive no turns")
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
			h.mu.Lock()
			delete(h.turnPending, pm.GetMessageId())
			if ok {
				h.consumed[pm.GetMessageId()] = true
			} else {
				h.buffer = append(h.buffer, pm) // engine gone: back to the recv buffer
			}
			h.mu.Unlock()
			if ok {
				h.emitCustomEvent(CustomMailConsumed, map[string]any{"message_ids": []any{pm.GetMessageId()}})
			}
		case <-h.ctx.Done():
			return
		}
	}
}

// ReportRunExited sends a best-effort RunExited on the live lifecycle link
// WITHOUT tearing the home down — the engine host's chat-ended signal (the
// runner process itself stays up until the coordinator kills it; the
// coordinator's loss synthesis covers a link that is down).
func (h *Home) ReportRunExited(exitCode int, harnessSessionID string) {
	if h.cfg.RunID == "" {
		return // a session-owner runner hosts no spawned run
	}
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
		clidiag.Warn("ctxloom", "runner: report RunExited: %v (coordinator will synthesize loss)", err)
	}
}

// Request runs one plane-2 request to completion: correlated by request_id,
// reissued (same id) across reconnects, bounded by ctx or the default
// budget.
func (h *Home) Request(ctx context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
	if req.GetRequestId() == "" {
		req.RequestId = randID("req-", 12)
	}
	hr := &homeReq{req: req, ch: make(chan *agentcoordpb.CoordinatorResponse, 1)}
	h.mu.Lock()
	h.pending[req.GetRequestId()] = hr
	h.mu.Unlock()
	h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: req}})

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	started := time.Now()
	select {
	case resp := <-hr.ch:
		return resp, nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, req.GetRequestId())
		h.mu.Unlock()
		return nil, h.requestFailure(ctx, time.Since(started))
	case <-h.ctx.Done():
		return nil, ErrCoordinatorUnreachable
	}
}

// Recv is the runner-LOCAL agent_recv: drain the notice buffer, or park
// against it for up to wait (one park; a newer receive preempts —
// ErrRecvPreempted). Returned messages stay TENTATIVE at the coordinator
// until acknowledged: this call first acks the PREVIOUS Recv's returned ids
// (cursor-ack — the closest observable point to "the shim actually returned
// them": the harness calling again proves it received the last batch), and
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
	h.mu.Unlock()
	if !wasParked {
		h.emitCustomEvent(CustomRecvParked, nil)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case msgs := <-p.ch:
		if msgs == nil {
			// Preempted: the newer poll holds the park — no unpark event.
			return nil, ErrRecvPreempted
		}
		h.unpark()
		h.recordReturned(msgs)
		return msgs, nil
	case <-timer.C:
		return nil, h.abandonPark(p, ErrRecvTimeout)
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
		h.forgetParkedCtl(m.GetMessageId())
	}
	h.mu.Unlock()
}

// forgetParkedCtl drops id from the unpulled ledger. Called from the ONE place
// a body stops being unpulled: the Recv that hands it to the harness. Caller
// holds h.mu.
func (h *Home) forgetParkedCtl(id string) {
	for i, p := range h.parkedCtl {
		if p.MessageID == id {
			h.parkedCtl = append(h.parkedCtl[:i], h.parkedCtl[i+1:]...)
			return
		}
	}
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
	vals := make([]any, len(ids))
	for i, id := range ids {
		vals[i] = id
	}
	h.emitCustomEvent(CustomMailConsumed, map[string]any{"message_ids": vals})
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
	h.mu.Unlock()
	if was {
		h.emitCustomEvent(CustomRecvUnparked, nil)
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
			clidiag.Warn("ctxloom", "runner: %s event value does not encode: %v (event dropped; the underlying state stays unacknowledged)", name, err)
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
func (h *Home) emitEvent(ev *agentcoordpb.AgentEvent) uint64 {
	h.mu.Lock()
	h.seq++
	ev.Seq = h.seq
	ev.RunId = h.cfg.RunID
	ev.OccurredAt = timestamppb.Now()
	h.unacked = append(h.unacked, ev)
	seq := h.seq
	h.mu.Unlock()
	h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
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
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
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
func (h *Home) reissueUnacked() {
	h.mu.Lock()
	events := append([]*agentcoordpb.AgentEvent(nil), h.unacked...)
	h.mu.Unlock()
	for _, ev := range events {
		h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: ev}})
	}
}

// crash tears the home down WITHOUT the clean-shutdown acknowledgements —
// the test seam simulating a runner crash (conformance: crash-before-ack
// re-delivers). Joins Home's own tracked loops (bounded) before returning
// so a caller (fakeSpawner.StartEngine's kill, wired
// as childRt.close) can rely on crash() actually being done, not merely
// dispatched, before it proceeds (Coordinator.Close's attachment loop calls
// closeFn synchronously for exactly this reason).
func (h *Home) crash() {
	h.tracked.seal()
	h.cancel()
	_ = h.conn.Close()
	h.waitTracked()
}

// Close tears the home down: best-effort final cursor-ack (a CLEAN exit
// acknowledges what the harness already received — a crash skips this and
// re-delivers, the safe direction), best-effort RunExited on the lifecycle
// link, then both loops stop and the conn closes — joined (bounded) before
// returning, mirroring crash().
func (h *Home) Close(exitCode int, harnessSessionID string) {
	h.ackReturned()
	h.tracked.seal()
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
