package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Custom event/request names — the namespaced "ctxloom/*" vocabulary riding
// the contract's open extension points (CustomEvent / CustomRequest).
const (
	// CustomMailConsumed is the runner's explicit consumption fact: the
	// mailbox cursor advances ONLY on it (tentative push-down delivery).
	// Value: {"message_ids": [...]}.
	CustomMailConsumed = "ctxloom/mail_consumed"
	// CustomRecvParked / CustomRecvUnparked assert the runner-local
	// agent_recv park state: park yields the child's execution slot
	// (onRolePark) and opens the mail push window; unpark re-acquires.
	// Runtime state — handled, never journaled; the runner RE-ASSERTS the
	// current state after a reconnect.
	CustomRecvParked   = "ctxloom/recv_parked"
	CustomRecvUnparked = "ctxloom/recv_unparked"
	// CustomHarnessSession reports the harness-NATIVE session id the moment
	// the engine host learns it (the ACP Session event) — the coordinator
	// journals it (run.harness fact) as the resume handle, so a child killed
	// mid-run can respawn with HarnessSpec.resume_session_id. Value:
	// {"session_id": "..."}.
	CustomHarnessSession = "ctxloom/harness_session"
	// CustomTurnStarted / CustomTurnIdle are the engine host's turn-state
	// transitions: started when engine output begins a turn, idle at its
	// completion boundary. The coordinator folds them into the §6a roster
	// state (executing/idle) and the D4 slot accounting — the migrated
	// path's replacement for the coordinator-side driveChild state machine.
	CustomTurnStarted = "ctxloom/turn_started"
	CustomTurnIdle    = "ctxloom/turn_idle"
	// CustomToolPrefix namespaces host-relay tool requests
	// (CustomRequest{name: "ctxloom/<tool>"}).
	CustomToolPrefix = "ctxloom/"
)

// Relay response size discipline (plan: 4MiB gRPC cap WATCHED): warn at
// 3MiB, fail with a fix-it before the transport would.
const (
	relayWarnBytes = 3 << 20
	relayCapBytes  = 4<<20 - 64<<10 // headroom under the 4MiB frame cap
)

// CustomHandler serves one host-relay tool on the coordinator side. args and
// the result are the tool's JSON payloads; caller is the credential-derived
// identity of the requesting run.
type CustomHandler func(ctx context.Context, caller Identity, args json.RawMessage) (json.RawMessage, error)

// SetCustomHandlers installs the host-relay tool handlers (host-resident
// tools: cross-session history, distillation). Called once at hosting setup,
// before any runner connects.
func (c *Coordinator) SetCustomHandlers(handlers map[string]CustomHandler) {
	c.custom = handlers
}

// runChan is one live RunChannel: the coordinator side of a runner's
// plane-1/2/3 stream for a single run (or the owning session itself — a
// depth-0 credential attaches with an empty run_id). All mutable fields are
// guarded by Coordinator.mu; frames go out through the single writer pump.
type runChan struct {
	role     string // the harp this channel serves (child harp, or owner harp)
	credHash string
	id       Identity
	send     chan *agentcoordpb.CoordinatorFrame
	cancel   context.CancelFunc

	parked bool     // runner-side agent_recv park is open
	pushed []string // message ids pushed tentatively (also in c.delivered)

	// reqCache caches responses by request_id — the responder-side
	// idempotency contract: a reissued request (same id, after reconnect)
	// gets the SAME response. inflight marks ids being served.
	reqCache map[string]*agentcoordpb.CoordinatorResponse
	inflight map[string]bool

	// ackSeq is the highest event seq processed on this channel; the
	// cumulative plane-3 Ack advances only through flushedSeq — the highest
	// seq whose item facts are DURABLE (group-fsync on the Ack watermark).
	// items buffers unflushed facts (delta storm kinds). Durable dedupe for
	// journaled fact kinds rides the facts themselves.
	ackSeq     uint64
	flushedSeq uint64
	items      []Fact
}

// RunChannel is the run-level stream: opened by the runner for each run it
// hosts (and by the session owner's runner with an empty run_id). All agent
// traffic — plane-2 requests, plane-1 events, plane-3 notices — multiplexes
// here; identity derives from the connection credential.
func (s *coordService) RunChannel(stream grpc.BidiStreamingServer[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]) error {
	c := s.c
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	credHash := hashToken(mdToken(stream.Context()))

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
			Accepted:     true,
			CommittedSeq: hello.GetResumeFromSeq(), // trivially resume where the runner stands (B window: no durable event log yet)
			EventWindow:  0,                        // effectively unbounded (Wave D adds credits when a real backpressure case exists)
			Capabilities: []string{"peer_messaging"},
		},
	}}); err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(stream.Context())
	ch := &runChan{
		role:     id.Harp,
		credHash: credHash,
		id:       id,
		send:     make(chan *agentcoordpb.CoordinatorFrame, 64),
		cancel:   cancel,
		reqCache: make(map[string]*agentcoordpb.CoordinatorResponse),
		inflight: make(map[string]bool),
	}
	c.mu.Lock()
	if prev := c.chans[id.Harp]; prev != nil {
		prev.cancel() // one RunChannel per role; newest wins (reconnect)
	}
	c.chans[id.Harp] = ch
	c.mu.Unlock()
	c.audit("run_channel", id.Harp, map[string]string{"run_id": id.RunID})
	// A migrated child's queued mail drains the moment its channel attaches
	// (fresh spawn: the pre-engine window; reconnect: unconsumed
	// redelivery — at-least-once, deduped runner-side on message_id).
	c.mu.Lock()
	rtAttach := c.byHarp[id.Harp]
	migratedAttach := rtAttach != nil && rtAttach.viaStartRun
	c.mu.Unlock()
	if migratedAttach && c.pendingCount(id.Harp) > 0 {
		c.pushMail(id.Harp)
	}

	defer func() {
		c.mu.Lock()
		registered := c.chans[id.Harp] == ch
		if registered {
			delete(c.chans, id.Harp)
		}
		pushed := ch.pushed
		ch.pushed = nil
		c.mu.Unlock()
		cancel()
		if registered {
			// Tentative deliveries die with the channel: un-reserve so the
			// pending messages re-deliver on reattach (at-least-once) and so
			// the terminal path's leftover-mail resume sees them.
			c.unreserveRuntime(id.Harp, pushed)
		}
	}()

	// Single writer pump: everything outbound funnels through ch.send.
	go func() {
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
	}()

	recvErr := make(chan error, 1)
	go func() {
		for {
			frame, rerr := stream.Recv()
			if rerr != nil {
				recvErr <- rerr
				return
			}
			c.handleAgentFrame(ch, frame)
		}
	}()

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
	case *agentcoordpb.AgentFrame_Response:
		// No coordinator-initiated requests exist in the B window.
	}
}

// handleAgentEvent processes plane-1 events: the ctxloom custom events
// (mail consumption, park/turn state, harness session), the report kinds
// (Summary, ArtifactProduced — their own reports journal), and — since C1 —
// ITEM events (message/tool-call/run lifecycle), journaled with group-fsync
// on the Ack watermark (items.go): deltas buffer; any boundary event
// flushes; the cumulative Ack advances only through durable seqs. Dedupe is
// (run, seq) against the channel's watermark (and the items fold's own,
// which survives a channel reattach).
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

	switch payload := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_Custom:
		c.handleCustomEvent(ch, payload.Custom)
		c.flushItems(ch)
	case *agentcoordpb.AgentEvent_Summary:
		c.recordSummary(ch.role, ev.GetSeq(), payload.Summary)
		c.flushItems(ch)
	case *agentcoordpb.AgentEvent_ArtifactProduced:
		c.recordArtifact(ch.role, ev.GetSeq(), payload.ArtifactProduced)
		c.flushItems(ch)
	default:
		if kind := itemKind(ev); kind != "" {
			c.bufferItem(ch, ev, kind)
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
	case CustomMailConsumed:
		ids := stringList(ev.GetValue(), "message_ids")
		if len(ids) == 0 {
			return
		}
		if err := c.mail.Exec(func() ([]Fact, error) {
			return []Fact{factAt(factMailConsumed, c.now(), mailConsumed{Role: ch.role, MessageIDs: ids})}, nil
		}); err != nil {
			clidiag.Warn("ctxloom", "coordinator: journal mail consumption for %s: %v", ch.role, err)
			return
		}
		c.unreserve(ch.role, ids)
		c.mu.Lock()
		ch.pushed = removeIDs(ch.pushed, ids)
		c.mu.Unlock()
	case CustomRecvParked:
		c.mu.Lock()
		ch.parked = true
		c.mu.Unlock()
		c.onRolePark(ch.role)
		c.pushMail(ch.role)
	case CustomRecvUnparked:
		c.mu.Lock()
		ch.parked = false
		c.mu.Unlock()
		c.onRoleUnpark(ch.role)
	case CustomHarnessSession:
		if s := ev.GetValue(); s != nil {
			if v, ok := s.GetFields()["session_id"]; ok {
				c.recordHarnessSession(ch.id.RunID, v.GetStringValue())
			}
		}
	case CustomTurnStarted:
		c.onTurnStarted(ch.role)
	case CustomTurnIdle:
		c.onTurnIdle(ch.role)
	}
}

// pushMail pushes the role's undelivered mail to its parked runner channel
// as CoordinatorNotice frames. Delivery is TENTATIVE: each pushed id is
// reserved in the runtime delivery ledger (excluded from turn-boundary
// drains) but stays pending in the fold until the runner's explicit
// mail_consumed fact — a crash between notice and consume re-delivers
// (at-least-once, deduped on message_id at both ends). Push happens ONLY
// while the runner-side recv is parked: an unparked child's mail is the
// turn machinery's to deliver (delivery-by-state, §6a), and pushing then
// would hand the harness the same message twice.
func (c *Coordinator) pushMail(role string) {
	c.mu.Lock()
	ch := c.chans[role]
	rt := c.byHarp[role]
	migrated := rt != nil && rt.viaStartRun
	// Push targets: a parked runner-side recv (any child), or — MIGRATED
	// children only — a live channel whose runner delivers by state (§6a:
	// the engine host queues to the turn boundary or starts a new turn).
	// A LEGACY child's unparked channel is never pushed: its turn-boundary
	// drain (takeNextMail) owns that delivery, and a push would strand the
	// message in the runner's recv buffer.
	if ch == nil || (!ch.parked && !migrated) {
		c.mu.Unlock()
		return
	}
	msgs := c.undeliveredLocked(role)
	for _, m := range msgs {
		c.delivered[role] = append(c.delivered[role], m.ID)
		ch.pushed = append(ch.pushed, m.ID)
	}
	send := ch.send
	c.mu.Unlock()

	for _, m := range msgs {
		notice := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Notice{
			Notice: &agentcoordpb.CoordinatorNotice{Kind: &agentcoordpb.CoordinatorNotice_PeerMessage{
				PeerMessage: peerMessageProto(m),
			}},
		}}
		select {
		case send <- notice:
		default:
			// Send pump saturated: drop the notice; the ids stay reserved
			// until the channel dies (then re-deliver) — never lost.
		}
	}
}

// peerMessageProto projects a mailbox message onto the wire shape. Kind
// rides structured.kind (PeerMessage has no kind field by design); any
// caller-supplied Structured payload (e.g. an escalation ladder's relayed
// ApprovalRequest projection, Wave C2) merges under it — kind always wins on
// a key collision, so a caller cannot spoof the message's own kind.
func peerMessageProto(m Message) *agentcoordpb.PeerMessage {
	pm := &agentcoordpb.PeerMessage{
		MessageId:   m.ID,
		FromAgentId: m.From,
		Text:        m.Body,
		InReplyTo:   m.InReplyTo,
	}
	fields := map[string]any{}
	if len(m.Structured) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(m.Structured, &extra); err == nil {
			for k, v := range extra {
				fields[k] = v
			}
		}
	}
	if m.Kind != "" {
		fields["kind"] = m.Kind
	}
	if len(fields) > 0 {
		pm.Structured, _ = structpb.NewStruct(fields)
	}
	return pm
}

// unreserveRuntime drops ids from the runtime delivery ledger WITHOUT a
// consume fact — the channel died before consumption, so the messages
// return to deliverable state.
func (c *Coordinator) unreserveRuntime(role string, ids []string) {
	if len(ids) == 0 {
		return
	}
	c.unreserve(role, ids)
}

// severChan tears a role's live run channel down (credential revocation /
// terminal path), un-reserving its tentative deliveries SYNCHRONOUSLY so the
// caller's immediate leftover-mail check sees them (the stream's own
// deferred cleanup then finds itself unregistered and skips).
func (c *Coordinator) severChan(role string) {
	c.mu.Lock()
	ch := c.chans[role]
	var pushed []string
	if ch != nil {
		pushed = ch.pushed
		ch.pushed = nil
		delete(c.chans, role)
	}
	c.mu.Unlock()
	if ch != nil {
		ch.cancel()
		c.unreserveRuntime(role, pushed)
	}
}

// handleAgentRequest serves one plane-2 request. request_id is the
// responder-side idempotency key: a duplicate (reissued after reconnect)
// returns the cached response; an in-flight duplicate is dropped (the
// original will answer). Handlers run on their own goroutine — a spawn can
// take seconds and must not block the stream's recv loop.
func (c *Coordinator) handleAgentRequest(ch *runChan, req *agentcoordpb.AgentRequest) {
	reqID := req.GetRequestId()
	if reqID == "" {
		c.respond(ch, &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "request_id is required")})
		return
	}
	c.mu.Lock()
	if cached, ok := ch.reqCache[reqID]; ok {
		c.mu.Unlock()
		c.respond(ch, cached)
		return
	}
	if ch.inflight[reqID] {
		c.mu.Unlock()
		return
	}
	ch.inflight[reqID] = true
	c.mu.Unlock()

	go func() {
		resp := c.serveAgentRequest(ch.id, req)
		resp.RequestId = reqID
		c.mu.Lock()
		delete(ch.inflight, reqID)
		ch.reqCache[reqID] = resp
		c.mu.Unlock()
		c.respond(ch, resp)
	}()
}

// respond queues one response frame on the channel's writer pump.
func (c *Coordinator) respond(ch *runChan, resp *agentcoordpb.CoordinatorResponse) {
	frame := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Response{Response: resp}}
	select {
	case ch.send <- frame:
	case <-time.After(5 * time.Second):
		clidiag.Warn("ctxloom", "coordinator: response to %s stalled; dropping (the runner reissues on reconnect)", ch.role)
	}
}

// serveAgentRequest maps plane-2 request kinds onto the EXISTING B1 stores —
// thin translation, the stores do not change.
func (c *Coordinator) serveAgentRequest(caller Identity, req *agentcoordpb.AgentRequest) *agentcoordpb.CoordinatorResponse {
	switch kind := req.GetKind().(type) {
	case *agentcoordpb.AgentRequest_PeerSend:
		return c.servePeerSend(caller, kind.PeerSend)
	case *agentcoordpb.AgentRequest_SpawnAgent:
		return c.serveSpawnAgent(caller, kind.SpawnAgent)
	case *agentcoordpb.AgentRequest_ListRuns:
		return c.serveListRuns(caller, kind.ListRuns)
	case *agentcoordpb.AgentRequest_StopRun:
		return c.serveStopRun(caller, kind.StopRun)
	case *agentcoordpb.AgentRequest_Approval:
		return c.serveApproval(caller, kind.Approval)
	case *agentcoordpb.AgentRequest_Custom:
		return c.serveCustom(caller, kind.Custom)
	default:
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unimplemented, "request kind not offered in this window")}
	}
}

// servePeerSend is agent_send: children address to_role "parent"; the owner
// addresses children by to_agent_id (harp). The kind convention rides
// structured.kind.
func (c *Coordinator) servePeerSend(caller Identity, req *agentcoordpb.PeerSendRequest) *agentcoordpb.CoordinatorResponse {
	to := req.GetToAgentId()
	if role := req.GetToRole(); role != "" {
		if to != "" {
			return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_send: set exactly one of to_agent_id / to_role, not both")}
		}
		to = role // ParentAddress ("parent") is the only role address in the B window
	}
	if to == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, `agent_send: a recipient is required — to_agent_id (a child harp) or to_role: "parent"`)}
	}
	if req.GetText() == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_send: text is required")}
	}
	kind := ""
	var structured json.RawMessage
	if s := req.GetStructured(); s != nil {
		if v, ok := s.GetFields()["kind"]; ok {
			kind = v.GetStringValue()
		}
		if raw, merr := protojson.Marshal(s); merr == nil {
			structured = raw
		}
	}
	msgID, delivered, disposition, err := c.peerSend(caller, to, kind, req.GetText(), structured, req.GetInReplyTo())
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusFromErr(err)}
	}
	delivery := agentcoordpb.PeerSendResult_DELIVERY_QUEUED
	if delivered {
		delivery = agentcoordpb.PeerSendResult_DELIVERY_DELIVERED
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(disposition),
		Kind: &agentcoordpb.CoordinatorResponse_PeerSend{PeerSend: &agentcoordpb.PeerSendResult{
			MessageId: msgID,
			Delivery:  delivery,
		}},
	}
}

// serveSpawnAgent is agent_run: role = the configured agent name, input.prompt
// = the briefing.
func (c *Coordinator) serveSpawnAgent(caller Identity, req *agentcoordpb.SpawnAgentRequest) *agentcoordpb.CoordinatorResponse {
	role := req.GetRole()
	prompt := ""
	if in := req.GetInput(); in != nil {
		if v, ok := in.GetFields()["prompt"]; ok {
			prompt = v.GetStringValue()
		}
	}
	if role == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: role is required (a configured agent name; see `ctxloom agent list`)")}
	}
	if prompt == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_run: input.prompt is required (the child's briefing/first turn)")}
	}
	out, err := c.AgentRun(c.baseCtx, caller, role, prompt)
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusFromErr(err)}
	}
	disposition := fmt.Sprintf("spawned %s (engine %s, runtime %s)", out.Harp, out.Engine, orHost(out.Runtime))
	if out.Queued {
		disposition += "; queued behind the execution cap"
	}
	for _, d := range out.Degraded {
		disposition += "; degraded: " + d
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(disposition),
		Kind: &agentcoordpb.CoordinatorResponse_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentResult{
			ChildRunId:   out.RunID,
			ChildAgentId: out.Harp,
		}},
	}
}

func orHost(runtime string) string {
	if runtime == "" {
		return "host"
	}
	return runtime
}

// serveListRuns is the roster: the caller's children from the roster/runs
// folds (single state, N transports).
func (c *Coordinator) serveListRuns(caller Identity, req *agentcoordpb.ListRunsRequest) *agentcoordpb.CoordinatorResponse {
	if caller.IsChild() {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.PermissionDenied, "roster: only the coordinating session may list its children")}
	}
	result := &agentcoordpb.ListRunsResult{}
	c.runs.View(func() {
		for _, e := range c.rosterF.snapshot() {
			if !req.GetIncludeTerminal() && e.State == StateEnded {
				continue
			}
			rec := c.runsF.currentRun(e.Harp)
			if rec == nil {
				continue
			}
			if role := req.GetRole(); role != "" && rec.Agent != role {
				continue
			}
			info := &agentcoordpb.ListRunsResult_RunInfo{
				RunId: rec.RunID,
				Agent: &agentcoordpb.AgentIdentity{
					AgentId: e.Harp,
					Role:    rec.Agent,
				},
				Phase:         e.State,
				LatestSummary: c.reportsF.latestSummary(e.Harp),
			}
			result.Runs = append(result.Runs, info)
		}
	})
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.CoordinatorResponse_ListRuns{ListRuns: result},
	}
}

// serveStopRun is agent_stop (D1): ownership-checked against the REQUESTER's
// lineage — only the run's parent may stop it.
func (c *Coordinator) serveStopRun(caller Identity, req *agentcoordpb.StopRun) *agentcoordpb.CoordinatorResponse {
	runID := req.GetRunId()
	if runID == "" {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "agent_stop: run_id is required (from spawn's child_run_id or the roster)")}
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
	if rec.Ended {
		return &agentcoordpb.CoordinatorResponse{
			Status: okStatus(fmt.Sprintf("child %s had already ended (%s)", rec.Harp, rec.Cause)),
			Kind:   &agentcoordpb.CoordinatorResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{ExitedWithinGrace: true}},
		}
	}
	c.audit("agent_stop", caller.Harp, map[string]string{"harp": rec.Harp, "run_id": runID})
	c.terminateRun(runID, CauseStopped, fmt.Sprintf("stopped by %s", caller.Harp))
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(fmt.Sprintf("stopped child %s; its execution slot is freed (a later agent_send resumes it as a fresh run)", rec.Harp)),
		Kind:   &agentcoordpb.CoordinatorResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{ExitedWithinGrace: true}},
	}
}

// serveCustom relays a host-resident tool to its coordinator-side handler,
// under the 4MiB response-size watch.
func (c *Coordinator) serveCustom(caller Identity, req *agentcoordpb.CustomRequest) *agentcoordpb.CoordinatorResponse {
	h := c.custom[req.GetName()]
	if h == nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unimplemented, fmt.Sprintf("no host handler for %q", req.GetName()))}
	}
	args, err := protojson.Marshal(req.GetValue())
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, fmt.Sprintf("%s: decode args: %v", req.GetName(), err))}
	}
	out, err := h(c.baseCtx, caller, args)
	if err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusFromErr(err)}
	}
	if len(out) > relayCapBytes {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.ResourceExhausted,
			fmt.Sprintf("%s: response is %d bytes, past the 4MiB relay cap — narrow the request (e.g. target a specific session) or run the tool on the host session", req.GetName(), len(out)))}
	}
	if len(out) > relayWarnBytes {
		strictness.Record(strictness.ClassApply, "narrow the relayed tool's request before the 4MiB cap fails it",
			"host-relay tool %s returned %d bytes (watch: >3MiB)", req.GetName(), len(out))
	}
	val := &structpb.Struct{}
	if err := protojson.Unmarshal(out, val); err != nil {
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Internal, fmt.Sprintf("%s: encode result: %v", req.GetName(), err))}
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(""),
		Kind:   &agentcoordpb.CoordinatorResponse_Custom{Custom: val},
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
	}
	return statusErr(code, err.Error())
}

// stringList extracts a []string field from a Struct value.
func stringList(s *structpb.Struct, key string) []string {
	if s == nil {
		return nil
	}
	v, ok := s.GetFields()[key]
	if !ok {
		return nil
	}
	var out []string
	for _, e := range v.GetListValue().GetValues() {
		if str := e.GetStringValue(); str != "" {
			out = append(out, str)
		}
	}
	return out
}

// removeIDs filters ids out of list.
func removeIDs(list, ids []string) []string {
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := list[:0]
	for _, id := range list {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	return kept
}
