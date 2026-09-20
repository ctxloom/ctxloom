package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Custom event/request names — the namespaced "ctxloom/*" vocabulary riding
// the contract's open extension points (CustomEvent / CustomRequest).
const (
	// CustomRecvParked / CustomRecvUnparked assert the runner-local
	// agent_recv park state: park yields the child's execution slot
	// (onRolePark); unpark re-acquires. Runtime state — handled, never
	// journaled; the runner RE-ASSERTS the current state after a reconnect.
	CustomRecvParked   = "ctxloom/recv_parked"
	CustomRecvUnparked = "ctxloom/recv_unparked"
	// CustomHarnessSession reports the harness-NATIVE session id the moment
	// the engine host learns it (the ACP Session event) — the coordinator
	// journals it (run.harness fact) as the resume handle, so a child killed
	// mid-run can respawn with Launch.Resume.NativeKey. Value:
	// {"session_id": "..."}.
	CustomHarnessSession = "ctxloom/harness_session"
	// CustomTurnStarted / CustomTurnIdle are the engine host's turn-state
	// transitions: started when engine output begins a turn, idle at its
	// completion boundary. The coordinator folds them into the §6a roster
	// state (executing/idle) and the D4 slot accounting.
	CustomTurnStarted = "ctxloom/turn_started"
	CustomTurnIdle    = "ctxloom/turn_idle"
)

// Relay response size discipline (plan: 4MiB gRPC cap WATCHED): warn at
// 3MiB, fail with a fix-it before the transport would.
const (
	relayWarnBytes = 3 << 20
	relayCapBytes  = 4<<20 - 64<<10 // headroom under the 4MiB frame cap
)

// HostRequest is a host-relayed tool call: the tool by name and its
// arguments as the tool's own JSON object. The set is derived — every tool
// that reads the sessions root or cross-session history — and the
// application service refuses a name outside it (ErrUnknownHostTool).
type HostRequest struct {
	Tool string
	Args json.RawMessage
}

// HostResult is the tool's answer as its own JSON object.
type HostResult struct {
	Body json.RawMessage
}

// HostApp is the port the application services implement for the Host verb:
// the coordinator is COMPOSED with it (Options.Host), so the relay handlers
// have one home and coord never imports them.
type HostApp interface {
	Serve(ctx context.Context, caller Identity, req HostRequest) (HostResult, error)
}

var (
	// ErrNoHostApp refuses every relayed tool on a coordinator composed
	// without an application service.
	ErrNoHostApp = errors.New("coord: no host application is composed for relayed tools")
	// ErrUnknownHostTool is the application service's refusal of a tool
	// outside the relayed set.
	ErrUnknownHostTool = errors.New("coord: not a host-relayed tool")
	// ErrHostAnswerTooLarge refuses an answer past the relay's frame cap.
	ErrHostAnswerTooLarge = errors.New("coord: the relayed tool's answer is past the relay cap")
)

// Host is the verb: every host-relayed tool dispatched to the composed
// application service under the CALLER's identity — the child's harp, the
// caller's project — never the host process's. The relay's size discipline
// is applied here: an answer past the frame cap is refused with the remedy
// rather than failed by the transport.
func (c *Coordinator) Host(ctx context.Context, caller Identity, req HostRequest) (HostResult, error) {
	if c.host == nil {
		return HostResult{}, ErrNoHostApp
	}
	res, err := c.host.Serve(ctx, caller, req)
	if err != nil {
		return HostResult{}, err
	}
	if len(res.Body) > relayCapBytes {
		return HostResult{}, fmt.Errorf("%w: %s answered %d bytes, past the 4MiB relay cap — narrow the request (e.g. target a specific session) or run the tool on the host session", ErrHostAnswerTooLarge, req.Tool, len(res.Body))
	}
	if len(res.Body) > relayWarnBytes {
		strictness.Record(strictness.ClassApply, "narrow the relayed tool's request before the 4MiB cap fails it",
			"host-relay tool %s returned %d bytes (watch: >3MiB)", req.Tool, len(res.Body))
	}
	return res, nil
}

// runChan is one live RunChannel: the coordinator side of a runner's
// plane-1/2/3 stream for a single run (or the owning session itself — a
// depth-0 credential attaches with an empty run_id). All mutable fields are
// guarded by Coordinator.mu; frames go out through the single writer pump.
type runChan struct {
	role   string // the harp this channel serves (child harp, or owner harp)
	id     Identity
	send   chan *agentcoordpb.CoordinatorFrame
	cancel context.CancelFunc

	// caps is this run's Hello advertisement (coordination.proto's
	// Hello.capabilities), captured at serve. It is per-CHANNEL because it is
	// per-run: a resumed harp gets a fresh channel from a fresh runner and may
	// advertise differently, so nothing may cache it against the harp.
	caps map[string]bool

	// Plane-2 request idempotency does NOT live here: it must survive this
	// channel's death so a request reissued on the NEXT dial (same request_id)
	// reuses the in-flight dispatch instead of starting a second one. It lives
	// on Coordinator.reqTrack, keyed (role, request_id) — see handleAgentRequest.

	// ackSeq is the highest event seq processed on this channel; the
	// cumulative plane-3 Ack advances only through flushedSeq — the highest
	// seq whose item facts are DURABLE (group-fsync on the Ack watermark).
	// items buffers unflushed facts (delta storm kinds). Durable dedupe for
	// journaled fact kinds rides the facts themselves.
	ackSeq     uint64
	flushedSeq uint64
	items      []Fact

	// completed closes exactly once, the moment this channel's run_completed
	// item has been FLUSHED (durably journaled) — D4's terminal-tail drain
	// race fix waits on it before severing the channel.
	// completedOnce guards the close (handleAgentEvent runs on this
	// channel's single recv goroutine, but drainTerminalTail's safety-net
	// timeout path must never double-close on a concurrent late arrival).
	completed     chan struct{}
	completedOnce sync.Once
}

// RunChannel is the run-level stream: opened by the runner for each run it
// hosts (and by the session owner's runner with an empty run_id). All agent
// traffic — plane-2 requests, plane-1 events, plane-3 notices — multiplexes
// here; identity derives from the connection credential.
func (s *coordService) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	c := s.c
	c.streams.Add(1)
	defer c.streams.Done() // registered FIRST so it runs LAST, after the teardown below
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first AgentFrame must be Hello")
	}
	// Ownership: the presented run_id must be the one this credential was
	// minted for; a depth-0 (session-owner) credential attaches with an
	// empty run_id — the channel then serves the owning session itself.
	if hello.GetRunId() != id.RunID {
		reject := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
			HelloAck: &agentcoordpb.HelloAck{Accepted: false, RejectReason: &rpcstatus.Status{
				Code:    int32(codes.PermissionDenied),
				Message: fmt.Sprintf("run %q was not issued to this credential", hello.GetRunId()),
			}},
		}}
		_ = stream.Send(reject)
		return status.Errorf(codes.PermissionDenied, "run %q was not issued to this credential", hello.GetRunId())
	}
	if err := stream.Send(&agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_HelloAck{
		HelloAck: &agentcoordpb.HelloAck{
			Accepted: true,
			// NOT an independent watermark: the coordinator keeps no durable
			// event log, so it echoes the runner's own claim back. The proto
			// says so at Hello.resume_from_seq / HelloAck.committed_seq's
			// doc — nobody should build a client that trusts this as
			// confirmation.
			CommittedSeq: hello.GetResumeFromSeq(),
			Capabilities: []string{CapPeerMessaging},
		},
	}}); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(stream.Context())
	caps := make(map[string]bool, len(hello.GetCapabilities()))
	for _, cap := range hello.GetCapabilities() {
		caps[cap] = true
	}
	ch := &runChan{
		role:      id.Harp,
		id:        id,
		send:      make(chan *agentcoordpb.CoordinatorFrame, 64),
		cancel:    cancel,
		completed: make(chan struct{}),
		caps:      caps,
	}
	c.mu.Lock()
	if prev := c.chans[id.Harp]; prev != nil {
		prev.cancel() // one RunChannel per role; newest wins (reconnect)
	}
	c.chans[id.Harp] = ch
	c.mu.Unlock()
	// The advertisement is journaled with the attach, not just held in memory: it
	// is per-run, so once the run is gone nothing can reconstruct what it claimed
	// to be able to do — which is exactly the question an operator asks when a
	// control request was refused.
	c.audit("run_channel", id.Harp, map[string]string{
		"run_id":       id.RunID,
		"capabilities": strings.Join(hello.GetCapabilities(), ","),
	})
	// PER-CHILD ATTACH SWEEP: the coordinator's own half of the same
	// reconnect reconciliation the runner does on its side — anything this
	// child wrote or consumed while its channel was down is picked up now
	// rather than at the slow timer. (The runner's own startup sweep is what
	// delivers mail written for it before it dialed home.)
	c.spoolReactor.mark(id.Harp)

	defer c.releaseRunChan(id.Harp, ch)

	// Single writer pump: everything outbound funnels through ch.send.
	// goTracked terminates once streamCtx is
	// cancelled — either locally (a newer reconnect, or this func's own
	// deferred cancel()) or when srv.close()'s GracefulStop/Stop tears the
	// underlying gRPC transport down (streamCtx derives from the STREAM's
	// context, not c.baseCtx, so only the server actually cutting the
	// transport unblocks a still-live channel — see Coordinator.Close's doc).
	c.goTracked(func() {
		for {
			select {
			case frame := <-ch.send:
				if err := stream.Send(frame); err != nil {
					cancel()
					return
				}
			case <-streamCtx.Done():
				return
			}
		}
	})

	recvErr := make(chan error, 1)
	c.goTracked(func() {
		for {
			frame, rerr := stream.Recv()
			if rerr != nil {
				recvErr <- rerr
				return
			}
			c.handleAgentFrame(ch, frame)
		}
	})

	select {
	case err := <-recvErr:
		return err
	case <-streamCtx.Done():
		return status.Error(codes.Canceled, "run channel closed")
	}
}

// handleAgentFrame dispatches one inbound frame.
func (c *Coordinator) handleAgentFrame(ch *runChan, frame *agentcoordpb.AgentFrame) {
	switch kind := frame.GetKind().(type) {
	case *agentcoordpb.AgentFrame_Event:
		c.handleAgentEvent(ch, kind.Event)
	case *agentcoordpb.AgentFrame_Request:
		c.handleAgentRequest(ch, kind.Request)
	case *agentcoordpb.AgentFrame_Heartbeat:
		// Plane-3 liveness; RunnerChannel owns loss detection.
	case *agentcoordpb.AgentFrame_Hello:
		// Duplicate hello on a live stream: tolerated.
	case *agentcoordpb.AgentFrame_SpoolChanged:
		c.handleSpoolChanged(ch, kind.SpoolChanged)
	}
}

// handleAgentEvent processes plane-1 events: the ctxloom custom events
// (mail consumption, park/turn state, harness session), the report kinds
// (Summary, ArtifactProduced — their own reports journal), and — since C1 —
// ITEM events (message/tool-call/run lifecycle), journaled with group-fsync
// on the Ack watermark (items.go): deltas buffer; any boundary event
// flushes; the cumulative Ack advances only through durable seqs. Dedupe is
// (run, seq) against the channel's watermark (and the items fold's own,
// which survives a channel reattach). D1: every NEW (non-duplicate) event is
// also teed live, full payload, to consumer.go's watchHub — independent of
// the durable/counts-only journal path below.
func (c *Coordinator) handleAgentEvent(ch *runChan, ev *agentcoordpb.AgentEvent) {
	c.mu.Lock()
	if seq := ev.GetSeq(); seq != 0 {
		if seq <= ch.ackSeq {
			flushed := ch.flushedSeq
			c.mu.Unlock()
			c.ackThrough(ch, flushed) // re-ack the durable watermark: the runner may have missed it
			return
		}
		ch.ackSeq = seq
	}
	c.mu.Unlock()
	if isLossMarker(ev) {
		// EventsLost is the watch hub's own synthetic marker (consumer.go),
		// never a runner's: teed through, it would tell every subscriber
		// they lagged when nothing was lost. Ack-and-drop before the tee,
		// like any foreign payload, but named — a runner emitting it is
		// either hostile or confused, and either is worth a line.
		clidiag.Warn("ctxloom", "run %q sent an events_lost marker — that kind is coordinator-emitted only; dropped", ch.id.RunID)
		c.flushItems(ch)
		return
	}
	c.watch.broadcast(ev)

	switch payload := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_Custom:
		c.handleCustomEvent(ch, payload.Custom)
		c.flushItems(ch)
	case *agentcoordpb.AgentEvent_Summary:
		c.recordSummary(ch.role, ch.id.RunID, ev.GetSeq(), payload.Summary)
		c.flushItems(ch)
	case *agentcoordpb.AgentEvent_ArtifactProduced:
		c.recordArtifact(ch.role, payload.ArtifactProduced)
		c.flushItems(ch)
	default:
		if kind := itemKind(ev); kind != "" {
			c.captureRunFailure(ch.role, ev)
			c.bufferItem(ch, ev, kind)
			if kind == "run_completed" {
				// D4: bufferItem flushes run_completed
				// synchronously (it is not a delta kind) — mark the channel
				// completed the moment it is DURABLE, so terminateRun's
				// drain wait (drainTerminalTail) can stop waiting the
				// instant it is safe to sever, not just after the fixed cap.
				ch.completedOnce.Do(func() { close(ch.completed) })
			}
		} else {
			// Unknown/foreign payloads: ack-and-drop (forward compatibility).
			c.flushItems(ch)
		}
	}
}

// ackThrough emits the cumulative plane-3 Ack watermark (non-blocking: the
// send pump has buffer; a full buffer drops the ack — cumulative acks make
// that safe).
func (c *Coordinator) ackThrough(ch *runChan, seq uint64) {
	frame := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Ack{
		Ack: &agentcoordpb.Ack{CommittedSeq: seq},
	}}
	select {
	case ch.send <- frame:
	default:
	}
}

// handleCustomEvent serves the ctxloom/* custom event vocabulary.
func (c *Coordinator) handleCustomEvent(ch *runChan, ev *agentcoordpb.CustomEvent) {
	switch ev.GetName() {
	case CustomRecvParked:
		c.onRolePark(ch.role)
	case CustomRecvUnparked:
		c.onRoleUnpark(ch.role)
	case CustomHarnessSession:
		s := ev.GetValue()
		sid := ""
		if v, ok := s.GetFields()["session_id"]; ok {
			sid = v.GetStringValue()
		}
		if sid == "" {
			// The harness-native session id is the run's ONLY resume handle: a
			// child killed mid-run respawns through Launch.Resume.NativeKey,
			// and the one-shot turn loop refuses to tear an engine down without
			// one (oneShotReady). recordHarnessSession drops an empty id, so
			// losing it here used to leave no trace at all — the run simply
			// stopped being resumable and nothing said why.
			clidiag.Warn("ctxloom", "coordinator: %s from %s carried no session_id; run %s has no resume handle, so it cannot be resumed by native session key",
				CustomHarnessSession, ch.role, ch.id.RunID)
			return
		}
		c.recordHarnessSession(ch.id.RunID, sid)
		// The engine's live loadSession capability (the one-shot gate's
		// live half) rides the SAME custom event as the session id.
		if v, ok := s.GetFields()["resumable"]; ok {
			c.recordResumable(ch.id.RunID, v.GetBoolValue())
		}
	case CustomTurnStarted:
		c.onTurnStarted(ch.role)
	case CustomTurnIdle:
		c.onTurnIdle(ch.role)
	}
}

// peerMessageProto projects a mailbox message onto the wire shape. Kind rides
// the typed PeerMessage.kind field, spelled from the mailbox vocabulary; a kind
// outside that vocabulary is an ERROR rather than UNSPECIFIED, so a message
// nobody mapped cannot reach a recipient as "unset". Structured is the caller's
// companion (e.g. an escalation ladder's relayed ApprovalRequest projection)
// carried verbatim — no key is merged into it, and the receive side reads no
// kind out of it, so a "kind" key a caller put there is inert.
//
// A payload that cannot be carried is an ERROR, not an empty result.
// Both failures were previously swallowed — the json.Unmarshal error by an
// `if err == nil` with no else, structpb.NewStruct's by assignment to `_` — and
// each produced a PeerMessage with the caller's payload silently missing. For a
// relayed ApprovalRequest that is the entire message: the recipient gets an
// approval notice with no request in it and nothing reports a fault. The
// caller (the runner's in/ sweep) moves such a file to in/failed/ rather than
// delivering a hollow message.
func peerMessageProto(m Message) (*agentcoordpb.PeerMessage, error) {
	kind, err := agentcoordpb.MessageKindForLegacyName(m.Kind)
	if err != nil {
		return nil, err
	}
	pm := &agentcoordpb.PeerMessage{
		MessageId:   m.ID,
		FromAgentId: m.From,
		Text:        m.Body,
		InReplyTo:   m.InReplyTo,
		Kind:        kind,
	}
	if len(m.Structured) > 0 {
		var fields map[string]any
		if err := json.Unmarshal(m.Structured, &fields); err != nil {
			return nil, fmt.Errorf("decode structured payload: %w", err)
		}
		if len(fields) > 0 {
			s, err := structpb.NewStruct(fields)
			if err != nil {
				return nil, fmt.Errorf("encode structured payload: %w", err)
			}
			pm.Structured = s
		}
	}
	return pm, nil
}

// releaseRunChan is the RunChannel handler's teardown: deregister the channel
// and cancel its context.
//
// Deregistration is conditional — only this channel's own registration may
// be removed, never a successor's. A reconnect registers the new channel and
// cancels its predecessor inside one c.mu window, so the OLD handler's
// teardown always observes the successor.
func (c *Coordinator) releaseRunChan(harp string, ch *runChan) {
	c.mu.Lock()
	if c.chans[harp] == ch {
		delete(c.chans, harp)
	}
	c.mu.Unlock()
	ch.cancel()
}

// terminalDrainWindow bounds drainTerminalTail's safety-net wait: the cap for
// a runner that reported RunExited (claiming a terminal event was sent) but
// whose run_completed item somehow never arrives. In the common case the
// wait resolves in microseconds (ch.completed is usually already closed by
// the time drainTerminalTail runs) or milliseconds (the race window); this
// cap only matters on a truly pathological runner and must never hang
// shutdown indefinitely.
const terminalDrainWindow = 500 * time.Millisecond

// drainTerminalTail closes the terminal-tail race (D4): the
// runner emits a normal exit's final run_completed item on the RunChannel
// and reports RunExited on the SEPARATE RunnerChannel back-to-back (see
// enginehost.go's adapt: emitEvent(RunCompleted) then ReportRunExited) — two
// different streams, no ordering guarantee between them. Cancelling the
// RunChannel's context (severChan) the instant RunExited lands can discard
// an already-in-flight-but-not-yet-processed run_completed frame (a
// pending/future stream.Recv on a cancelled context returns Canceled even
// for data already on the wire). A short, bounded wait for the channel's
// own "run_completed flushed" signal (or the window's expiry) closes the
// gap without holding up shutdown: in the OVERWHELMINGLY common case
// ch.completed is already closed by the time this runs (item events process
// well before the separate RunnerChannel round-trip completes), so the wait
// costs nothing.
//
// Scope: only CauseRunnerExit termination calls this — the ONLY cause whose
// production emitter (enginehost.go's adapt) is contractually guaranteed to
// have just attempted a run_completed. CauseStopped (agent_stop / KillRun)
// and CauseRunnerLoss (disconnect/heartbeat silence — the runner and its
// harness died together) have no such guarantee and must not pay this
// wait for no benefit.
func (c *Coordinator) drainTerminalTail(role string) {
	c.mu.Lock()
	ch := c.chans[role]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	if hook := c.drainHook; hook != nil {
		hook(role)
	}
	select {
	case <-ch.completed:
	case <-time.After(terminalDrainWindow):
	}
}

// severChan tears a role's live run channel down (credential revocation /
// terminal path); the stream's own deferred cleanup then finds itself
// unregistered and skips.
func (c *Coordinator) severChan(role string) {
	c.mu.Lock()
	ch := c.chans[role]
	if ch != nil {
		delete(c.chans, role)
	}
	c.mu.Unlock()
	if ch != nil {
		ch.cancel()
	}
}

// reqKey identifies a plane-2 request for idempotency that must SURVIVE a
// RunChannel reconnect. Keyed by (role, request_id): the runner reissues an
// outstanding request with its ORIGINAL request_id on the fresh channel
// (home.go), and the role (child harp) is stable across the reconnect.
type reqKey struct {
	role  string
	reqID string
}

// inflightReq tracks one plane-2 request's SINGLE in-progress dispatch and its
// eventual response, off the per-connection runChan so it outlives a reconnect.
// resp==nil means the dispatch is still running: a reissue that finds it must
// NOT start a second dispatch — the running one answers on whichever channel is
// current when it completes (respondRole). This is the trust-critical case: an
// approval relay parks for minutes waiting on a human, and a reconnect in that
// window must not mint a second relay + ladder walk that races the first (a
// human ACCEPT then answered on the dead channel while the live channel bottoms
// out at DECLINE — fix/approval-reconnect-race).
type inflightReq struct {
	resp *agentcoordpb.CoordinatorResponse
}

// handleAgentRequest serves one plane-2 request. request_id is the
// responder-side idempotency key, scoped (role, request_id) on Coordinator so
// it survives a reconnect: a completed request re-delivers its SAME response on
// the current channel; an in-flight one is NOT re-dispatched — the original
// dispatch answers on whichever channel is live when it finishes. Handlers run
// on their own goroutine — a spawn (or a human-facing approval relay) can take
// seconds to minutes and must not block the stream's recv loop.
func (c *Coordinator) handleAgentRequest(ch *runChan, req *agentcoordpb.AgentRequest) {
	reqID := req.GetRequestId()
	if reqID == "" {
		c.respond(ch, &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "request_id is required")})
		return
	}
	key := reqKey{role: ch.role, reqID: reqID}
	c.mu.Lock()
	if c.reqTrack == nil {
		c.reqTrack = make(map[reqKey]*inflightReq)
	}
	if tr := c.reqTrack[key]; tr != nil {
		resp := tr.resp
		c.mu.Unlock()
		if resp != nil {
			// Already answered: re-deliver the SAME response on the CURRENT
			// channel (a reconnect dropped the original, or a duplicate frame
			// arrived on one live stream). Idempotent by construction.
			c.respondRole(ch.role, resp)
		}
		// resp==nil: the original dispatch is still in flight (e.g. an approval
		// relay awaiting a human). It owns the answer and delivers it on the
		// then-current channel — do NOT start a second dispatch.
		return
	}
	tr := &inflightReq{}
	c.reqTrack[key] = tr
	c.mu.Unlock()

	// ch.id is the role's stable identity (same credential across reconnect);
	// the response is routed to whatever channel is CURRENT at completion, not
	// this ch, which may have died mid-dispatch.
	id := ch.id
	role := ch.role
	c.goTracked(func() {
		resp := c.serveAgentRequest(id, req)
		resp.RequestId = reqID
		c.mu.Lock()
		tr.resp = resp
		c.mu.Unlock()
		c.respondRole(role, resp)
	})
}

// responseQueueWindow bounds how long a plane-2 response waits for room on a
// saturated writer pump before it is given up on. The wait itself never runs on
// the channel's receive goroutine — see respond.
const responseQueueWindow = 5 * time.Second

// respond queues one response frame on the channel's writer pump.
//
// A saturated pump must NOT block the caller. respond runs on the
// channel's own RECEIVE goroutine for two paths — a request arriving with no
// request_id, and re-delivery of an already-cached response to a reissue — and
// waiting there stalls every inbound frame on that channel behind one slow
// writer: acks, mail-consumption facts, park and turn-state transitions. The
// bounded wait is therefore handed to a tracked goroutine, which is also what
// makes the wait worth anything: a pump that drains inside the window now
// DELIVERS the response instead of dropping it after five seconds of holding
// the receive loop still.
//
// Ordering is not a casualty: every response is correlated by request_id
// (reqTrack), so one that is overtaken while its pump was full is still matched
// to its own request.
//
// The give-up notice states what actually happens, which the old wording did
// not: the runner does not reissue on a live channel — Home.Request is bounded
// by its own defaultRequestTimeout and fails the call — and only a RECONNECT
// reissues, at which point reqTrack re-delivers the cached response.
func (c *Coordinator) respond(ch *runChan, resp *agentcoordpb.CoordinatorResponse) {
	frame := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Response{Response: resp}}
	select {
	case ch.send <- frame:
		return
	default:
	}
	role := ch.role
	c.goTracked(func() {
		select {
		case ch.send <- frame:
		case <-c.baseCtx.Done():
		case <-time.After(responseQueueWindow):
			clidiag.Warn("ctxloom", "coordinator: response to %s found its send pump full for %s and was dropped; the runner's request fails at its own timeout — only a reconnect reissues it, and the cached response is re-delivered then",
				role, responseQueueWindow)
		}
	})
}

// respondRole queues a response on the role's CURRENT live channel — the
// reconnect-safe sibling of respond. A dispatch that outlived the channel it
// arrived on (an approval relay that waited minutes for a human, across a
// reconnect) must answer on whatever channel is live NOW, never the dead one it
// started on. No live channel: drop it — the runner reissues on its next
// reconnect and reqTrack re-delivers the cached response then.
func (c *Coordinator) respondRole(role string, resp *agentcoordpb.CoordinatorResponse) {
	c.mu.Lock()
	ch := c.chans[role]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	c.respond(ch, resp)
}

// clearReqTrack drops a role's plane-2 idempotency records at the terminal
// seam (terminateRun) — alongside the ACCEPT_FOR_SESSION cache. A resumed harp
// gets a fresh run and re-dispatches cleanly; the records must not accumulate
// across the process's lifetime.
func (c *Coordinator) clearReqTrack(role string) {
	c.mu.Lock()
	for k := range c.reqTrack {
		if k.role == role {
			delete(c.reqTrack, k)
		}
	}
	c.mu.Unlock()
}

// serveAgentRequest maps plane-2 request kinds onto the EXISTING B1 stores —
// thin translation, the stores do not change.
func (c *Coordinator) serveAgentRequest(caller Identity, req *agentcoordpb.AgentRequest) *agentcoordpb.CoordinatorResponse {
	switch kind := req.GetKind().(type) {
	case *agentcoordpb.AgentRequest_PeerSend:
		// agent_send never reaches the wire: it is a LOCAL file write in the
		// runner (Home.sendPeerViaSpool), routed when the coordinator sweeps.
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unimplemented,
			"agent_send is a local spool write at the runner and is never served here; a runner that sent it over the wire is older than this coordinator")}
	case *agentcoordpb.AgentRequest_SpawnAgent:
		return c.serveSpawnAgent(caller, kind.SpawnAgent)
	case *agentcoordpb.AgentRequest_ListRuns:
		return c.serveListRuns(caller, kind.ListRuns)
	case *agentcoordpb.AgentRequest_StopRun:
		// The bulk shape waits on the drain, bounded; baseCtx is the only
		// ctx a plane-2 dispatch has, and Close settles the drain anyway.
		return c.serveStopRun(c.baseCtx, caller, kind.StopRun)
	case *agentcoordpb.AgentRequest_ControlRun:
		// Same ctx reasoning: each verb applies its own budget to baseCtx.
		return c.serveControlRun(c.baseCtx, caller, kind.ControlRun)
	case *agentcoordpb.AgentRequest_Host:
		return c.serveHost(caller, kind.Host)
	default:
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unimplemented, "request kind not offered in this window")}
	}
}

// spawnInputString reads a STRING value out of agent_run's free-form input
// Struct. A key that is absent yields "" — the caller said nothing, and every
// consumer treats that as "defer to the configured default".
//
// A key that is PRESENT but carries a non-string JSON value is an ERROR.
// structpb.Value.GetStringValue() answers "" for every other kind, which
// makes `{"dirty_tree_handler": 4}` indistinguishable from omitting the key —
// and these keys select postures whose unset path has a default that writes
// to the user's repository. Unset and unusable are different inputs and get
// different answers.
func spawnInputString(in *structpb.Struct, key string) (string, error) {
	v, ok := in.GetFields()[key]
	if !ok {
		return "", nil
	}
	sv, ok := v.GetKind().(*structpb.Value_StringValue)
	if !ok {
		return "", fmt.Errorf("agent_run: input.%s must be a string (got %s)", key, v.String())
	}
	return sv.StringValue, nil
}

// serveSpawnAgent is agent_run: role = the configured agent name,
// input.prompt = the briefing, input.workspace (GAP 2, optional) = a
// per-call workspace-axis override — "none"|"worktree", and
// input.dirty_tree_handler (optional) = a per-call override for what a
// worktree spawn does when the parent tree is dirty —
// "commit"|"copy"|"stale"|"fail" — riding the same free-form input Struct as
// prompt. Absent falls back to the project's cfg.Workspace /
// cfg.GetDirtyTreeHandler() defaults.
//
// THIS IS THE EDGE for the per-call vocabularies: the Struct is free-form
// (its generated schema carries the enums for a model to read, but nothing on
// the wire enforces them), and the caller filling it is a MODEL. So the value
// is converted here, through its owning package's parser, and only the typed
// value travels inward. An unrecognized spelling is refused with
// InvalidArgument at the verb the caller invoked, naming the legal values —
// never carried inward to be interpreted by a frame that answers a typo with
// a default. dirty_tree_handler's default member auto-commits the user's
// working tree, so a typo that fell through to it would write to the
// repository past both the caller's and the project's explicit choice.
// input.dirty_tree_handler deliberately carries NO acknowledgement for the
// "commit" handler's mutation — that is a per-checkout, human-only
// acknowledgement (dirty_tree_commit_ack — see
// config.DirtyTreeCommitAcknowledged) this free-form per-call field can
// never set: it is not even a config key, precisely so no channel an agent
// can reach can grant it.
func (c *Coordinator) serveSpawnAgent(caller Identity, req *agentcoordpb.SpawnAgentRequest) *agentcoordpb.CoordinatorResponse {
	role := req.GetRole()
	prompt := ""
	rawWorkspace := ""
	rawDirtyTreeHandler := ""
	if in := req.GetInput(); in != nil {
		if v, ok := in.GetFields()["prompt"]; ok {
			prompt = v.GetStringValue()
		}
		var err error
		if rawWorkspace, err = spawnInputString(in, "workspace"); err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, err.Error())}
		}
		if rawDirtyTreeHandler, err = spawnInputString(in, "dirty_tree_handler"); err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, err.Error())}
		}
	}
	// Both per-call vocabularies are converted at this edge. The workspace
	// axis is checked here rather than only where the child's axes are
	// resolved because THAT happens on the launch goroutine, after this verb
	// has already answered "spawned": a typo would otherwise be reported as a
	// child that died, to a caller who could no longer see which argument was
	// wrong.
	workspace, werr := isolation.ParseWorkspaceAxis(rawWorkspace)
	if werr != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: "+werr.Error())}
	}
	dirtyTreeHandler, derr := launch.ParseDirtyTreeHandler(rawDirtyTreeHandler)
	if derr != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: "+derr.Error())}
	}
	if role == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: role is required (a configured agent name; see `ctxloom agent list`)")}
	}
	if prompt == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: input.prompt is required (the child's briefing/first turn)")}
	}
	out, err := c.AgentRun(c.baseCtx, caller, role, prompt, workspace, dirtyTreeHandler)
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusFromErr(err)}
	}
	runtime := out.Runtime
	if runtime == "" {
		runtime = "host"
	}
	// The launch runs on its own goroutine, so "spawned" was historically a
	// claim, not an observation: a child whose launch had ALREADY failed by
	// the time this answer was composed still reported as spawned.
	// Read the run's terminal state before answering — a settled failure is
	// reported as a failure, and everything else keeps the exact wording that
	// shipped.
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(spawnDisposition(out, runtime, c.settledFailureCause(out.RunID))),
		Kind: &agentcoordpb.CoordinatorResponse_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentResult{
			ChildRunId:   out.RunID,
			ChildAgentId: out.Harp,
		}},
	}
}

// spawnDisposition renders agent_run's answer. failureCause is the run's
// terminal cause when the launch has ALREADY settled as failed by the time we
// answer (empty otherwise).
//
// The success wording is byte-identical to what shipped — an out-of-repo
// consumer (the ctxloom VS Code extension) may match on "spawned" — so only
// the failure case reads differently, and it deliberately does NOT contain
// "spawned " anywhere. The structured SpawnAgentResult (child_run_id /
// child_agent_id) still rides along on both, so a consumer that reads the
// payload rather than the prose is unaffected either way.
func spawnDisposition(out *RunOutcome, runtime launch.RuntimeAxis, failureCause string) string {
	if failureCause != "" {
		return fmt.Sprintf("agent_run FAILED: %s (engine %s, runtime %s) did not launch (%s) — see the roster for the run's terminal detail",
			out.Harp, out.Engine, runtime, failureCause)
	}
	disposition := fmt.Sprintf("spawned %s (engine %s, runtime %s)", out.Harp, out.Engine, runtime)
	if out.Queued {
		disposition += "; queued behind the execution cap"
	}
	for _, d := range out.Degraded {
		disposition += "; degraded: " + d
	}
	return disposition
}

// settledFailureCause reports a run's terminal cause when it has already ended
// in a LAUNCH FAILURE, and "" in every other case (still running, queued, or
// ended for any other reason). It never waits: a launch still in flight is not
// a failure, and blocking agent_run on a container prepare would be a worse
// bug than the one this closes.
func (c *Coordinator) settledFailureCause(runID string) string {
	var cause string
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil && r.Ended && r.Cause == CauseLaunchFailed {
			cause = r.Cause
		}
	})
	return cause
}

// serveListRuns is the roster: the caller's children from the roster/runs
// folds (single state, N transports — consumer.go's listRunsSnapshot is the
// shared projection; D1's ConsumerService is a fourth transport onto the
// same state).
func (c *Coordinator) serveListRuns(caller Identity, req *agentcoordpb.ListRunsRequest) *agentcoordpb.CoordinatorResponse {
	if caller.IsChild() {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.PermissionDenied, "roster: only the coordinating session may list its children")}
	}
	result := c.listRunsSnapshot(req.GetIncludeTerminal(), req.GetRole())
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.CoordinatorResponse_ListRuns{ListRuns: result},
	}
}

// serveStopRun is agent_stop (D1), in its two shapes. With a run_id it is
// ownership-checked against the REQUESTER's lineage — only the run's parent
// may stop it. With NO run_id it is the bulk sweep: every live child of the
// requester's session, under the drain bound, each named in the result with
// its outcome; a reason is required so an accidental omission stops nothing.
func (c *Coordinator) serveStopRun(ctx context.Context, caller Identity, req *agentcoordpb.StopRun) *agentcoordpb.CoordinatorResponse {
	runID := req.GetRunId()
	if runID == "" {
		return c.serveStopChildren(ctx, caller, req.GetReason())
	}
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil || rec.ParentHarp != caller.Harp {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.PermissionDenied, fmt.Sprintf("agent_stop: run %q is not a child of this session", runID))}
	}
	// This is the path a coordinator-capable CHILD uses to stop its own
	// grandchild, not just the host-side verb — the shared stopRun cancels
	// the launch here too.
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(c.stopRun(caller, rec, req.GetReason())),
		Kind:   &agentcoordpb.CoordinatorResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{}},
	}
}

// serveStopChildren is agent_stop's bulk shape on plane 2: StopChildren
// projected onto StopRunResult.children.
func (c *Coordinator) serveStopChildren(ctx context.Context, caller Identity, reason string) *agentcoordpb.CoordinatorResponse {
	stopped, err := c.StopChildren(ctx, caller, reason)
	if errors.Is(err, ErrStopReasonRequired) {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, fmt.Sprintf("%v — give run_id to stop one child, or reason to stop them all", err))}
	}
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unavailable, fmt.Sprintf("agent_stop: %v", err))}
	}
	result := &agentcoordpb.StopRunResult{}
	for _, sc := range stopped {
		result.Children = append(result.Children, &agentcoordpb.StopRunResult_Child{
			Harp: sc.Harp, RunId: sc.RunID, Agent: sc.Agent, Outcome: sc.Outcome, Detail: sc.Detail,
		})
	}
	msg := fmt.Sprintf("stopped %d child(ren) of this session; their execution slots are freed (a later agent_send resumes any of them as a fresh run)", len(stopped))
	if len(stopped) == 0 {
		msg = "no live children to stop"
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(msg),
		Kind:   &agentcoordpb.CoordinatorResponse_StopRun{StopRun: result},
	}
}

// serveCustom relays a host-resident tool to its coordinator-side handler,
// under the 4MiB response-size watch.
// serveHost decodes the typed host frame, runs the Host verb and encodes
// the answer; the frame's Struct halves are the tool's own JSON objects.
func (c *Coordinator) serveHost(caller Identity, req *agentcoordpb.HostRequest) *agentcoordpb.CoordinatorResponse {
	args, err := protojson.Marshal(req.GetArgs())
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, fmt.Sprintf("%s: decode args: %v", req.GetTool(), err))}
	}
	res, err := c.Host(c.baseCtx, caller, HostRequest{Tool: req.GetTool(), Args: args})
	switch {
	case errors.Is(err, ErrNoHostApp), errors.Is(err, ErrUnknownHostTool):
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unimplemented, err.Error())}
	case errors.Is(err, ErrHostAnswerTooLarge):
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.ResourceExhausted, err.Error())}
	case err != nil:
		return &agentcoordpb.CoordinatorResponse{Status: statusFromErr(err)}
	}
	body := &structpb.Struct{}
	if err := protojson.Unmarshal(res.Body, body); err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Internal, fmt.Sprintf("%s: encode result: %v", req.GetTool(), err))}
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.CoordinatorResponse_Host{Host: &agentcoordpb.HostResult{Body: body}},
	}
}

// --- small helpers -----------------------------------------------------------

func okStatus(msg string) *rpcstatus.Status {
	return &rpcstatus.Status{Code: int32(codes.OK), Message: msg}
}

func statusErr(code codes.Code, msg string) *rpcstatus.Status {
	return &rpcstatus.Status{Code: int32(code), Message: msg}
}

// statusFromErr maps a store/verb error onto a plane-2 status. Typed mailbox
// errors keep their vocabulary; everything else is INTERNAL with the message.
func statusFromErr(err error) *rpcstatus.Status {
	code := codes.Internal
	switch {
	case errors.Is(err, ErrPeerRouting):
		code = codes.PermissionDenied
	case errors.Is(err, ErrRecvTimeout):
		code = codes.DeadlineExceeded
	case errors.Is(err, ErrSenderMailKind):
		code = codes.InvalidArgument
	case errors.Is(err, ErrDraining):
		code = codes.Unavailable
	}
	return statusErr(code, err.Error())
}
