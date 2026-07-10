package coord

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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

	mu      sync.Mutex
	stream  grpc.BidiStreamingClient[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]
	sendMu  sync.Mutex // serializes stream.Send (single-writer discipline)
	seq     uint64
	unacked []*agentcoordpb.AgentEvent
	acked   uint64
	ackCh   chan struct{} // closed + replaced on every ack advance
	pending map[string]*homeReq

	buffer   []*agentcoordpb.PeerMessage
	consumed map[string]bool
	park     *homePark
	parked   bool

	link     *RunnerLink
	linkDone chan struct{} // closed when Close ran (stops the redial loops)
}

// HomeConfig carries the spawn-injected coordinator trio plus the runner's
// self-description.
type HomeConfig struct {
	URL     string // CTXLOOM_COORD_URL
	Token   string // CTXLOOM_COORD_CRED (held ONLY by the runner)
	RunID   string // CTXLOOM_RUN_ID ("" on the session-owner credential)
	Harness string
	Version string
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

// ErrCoordinatorUnreachable answers a tool verb whose plane-2 request could
// not complete within its budget.
var ErrCoordinatorUnreachable = errors.New("coordinator unreachable (the runner keeps reconnecting; retry, or finish standalone)")

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
		cfg:      cfg,
		ctx:      hctx,
		cancel:   cancel,
		conn:     conn,
		ackCh:    make(chan struct{}),
		pending:  make(map[string]*homeReq),
		consumed: make(map[string]bool),
		linkDone: make(chan struct{}),
	}
	go h.runnerChannelLoop()
	go h.runChannelLoop()
	return h, nil
}

// runnerChannelLoop keeps the lifecycle RunnerChannel alive (Hello +
// heartbeats + best-effort RunExited at Close).
func (h *Home) runnerChannelLoop() {
	for {
		link, err := DialRunner(h.ctx, h.cfg.URL, h.cfg.Token, h.cfg.RunID, h.cfg.Harness, h.cfg.Version)
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
		Capabilities:    []string{"peer_messaging"},
	}}}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := first.GetHelloAck()
	if ack == nil || !ack.GetAccepted() {
		return errors.New("run channel Hello rejected")
	}

	// Attach, then REISSUE: unacked events in order, outstanding requests
	// with their ORIGINAL request_ids, and a fresh park assertion when a
	// recv is parked (park state is runtime state the coordinator forgot
	// with the old stream).
	h.mu.Lock()
	h.stream = stream
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
		// No coordinator-initiated requests are served in the B window.
		h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Response{Response: &agentcoordpb.AgentResponse{
			RequestId: kind.Request.GetRequestId(),
			Status:    statusErr(codes.Unimplemented, "not offered in this window"),
		}}})
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

// deliverNotice buffers one pushed PeerMessage (deduped on message_id
// against the buffer and consumption history) and completes a parked recv.
func (h *Home) deliverNotice(pm *agentcoordpb.PeerMessage) {
	h.mu.Lock()
	if h.consumed[pm.GetMessageId()] {
		h.mu.Unlock()
		return
	}
	for _, b := range h.buffer {
		if b.GetMessageId() == pm.GetMessageId() {
			h.mu.Unlock()
			return
		}
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
	select {
	case resp := <-hr.ch:
		return resp, nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, req.GetRequestId())
		h.mu.Unlock()
		return nil, ErrCoordinatorUnreachable
	case <-h.ctx.Done():
		return nil, ErrCoordinatorUnreachable
	}
}

// Recv is the runner-LOCAL agent_recv: drain the notice buffer, or park
// against it for up to wait (one park; a newer receive preempts —
// ErrRecvPreempted). Returned messages are TENTATIVE until the returned
// consume func runs (after the response reaches the shim): it journals the
// consumption fact at the coordinator; a crash before it re-delivers
// (at-least-once, deduped on message_id).
func (h *Home) Recv(ctx context.Context, wait time.Duration) ([]*agentcoordpb.PeerMessage, func(), error) {
	h.mu.Lock()
	if len(h.buffer) > 0 {
		msgs := h.buffer
		h.buffer = nil
		h.mu.Unlock()
		return msgs, h.consumeFunc(msgs), nil
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
			return nil, nil, ErrRecvPreempted
		}
		h.unpark()
		return msgs, h.consumeFunc(msgs), nil
	case <-timer.C:
		return nil, nil, h.abandonPark(p, ErrRecvTimeout)
	case <-ctx.Done():
		return nil, nil, h.abandonPark(p, ctx.Err())
	case <-h.ctx.Done():
		return nil, nil, h.abandonPark(p, ErrCoordinatorUnreachable)
	}
}

// abandonPark resolves the timeout/cancel race against a delivery exactly
// like the coordinator's local poll: a delivery that already won is
// authoritative.
func (h *Home) abandonPark(p *homePark, err error) error {
	h.mu.Lock()
	if p.done {
		h.mu.Unlock()
		<-p.ch // the delivery beat us; but the caller is leaving — requeue
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

// consumeFunc builds the deferred consumption fact for one delivery: mark
// locally (dedupe against re-push) and emit the mail_consumed custom event.
func (h *Home) consumeFunc(msgs []*agentcoordpb.PeerMessage) func() {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.GetMessageId())
	}
	return func() {
		h.mu.Lock()
		for _, id := range ids {
			h.consumed[id] = true
		}
		h.mu.Unlock()
		vals := make([]any, len(ids))
		for i, id := range ids {
			vals[i] = id
		}
		h.emitCustomEvent(CustomMailConsumed, map[string]any{"message_ids": vals})
	}
}

// emitCustomEvent emits one ctxloom/* custom event on the event plane.
func (h *Home) emitCustomEvent(name string, value map[string]any) {
	var v *structpb.Struct
	if value != nil {
		v, _ = structpb.NewStruct(value)
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
func (h *Home) Report(ctx context.Context, summary *agentcoordpb.Summary, artifacts []*agentcoordpb.ArtifactProduced) error {
	var last uint64
	for _, a := range artifacts {
		last = h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ArtifactProduced{ArtifactProduced: a}})
	}
	if summary != nil {
		last = h.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Summary{Summary: summary}})
	}
	if last == 0 {
		return nil
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	for {
		h.mu.Lock()
		acked := h.acked
		ch := h.ackCh
		h.mu.Unlock()
		if acked >= last {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ErrCoordinatorUnreachable
		case <-h.ctx.Done():
			return ErrCoordinatorUnreachable
		}
	}
}

// Close tears the home down: best-effort RunExited on the lifecycle link,
// then both loops stop and the conn closes.
func (h *Home) Close(exitCode int, harnessSessionID string) {
	h.mu.Lock()
	link := h.link
	h.link = nil
	h.mu.Unlock()
	if link != nil {
		link.Shutdown(exitCode, harnessSessionID)
	}
	h.cancel()
	_ = h.conn.Close()
}
