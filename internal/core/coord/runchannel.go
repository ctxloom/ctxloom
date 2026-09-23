package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	// binds it onto the harp's session entry (bindNativeSession) as the
	// resume handle, so a child killed mid-run can respawn with
	// Launch.Resume.NativeKey. Value:
	// {"session_id": "..."}.
	CustomHarnessSession = "ctxloom/harness_session"
	// CustomTurnStarted / CustomTurnIdle are the engine host's turn-state
	// transitions: started when engine output begins a turn, idle at its
	// completion boundary. The coordinator folds them into the roster
	// state (executing/idle) and the slot accounting.
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

// Validate requires the tool's name; the tool decides what its args mean.
func (r HostRequest) Validate() error {
	if strings.TrimSpace(r.Tool) == "" {
		return fmt.Errorf("%w: host: tool is required", ErrInvalidRequest)
	}
	return nil
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
		c.rep.Recordf(report.KindApply, "narrow the relayed tool's request before the 4MiB cap fails it",
			"host-relay tool %s returned %d bytes (watch: >3MiB)", req.Tool, len(res.Body))
	}
	return res, nil
}

// RunChannel is one live run channel as the coordinator holds it: the
// coordinator side of a runner's plane-1/2/3 stream for a single run (or
// the owning session itself — a depth-0 credential attaches with an empty
// run_id), on the one BidiSession scaffold. The wire adapter opens it
// (AttachRun), pumps its outbound frames onto the stream (Pump) and hands
// every inbound frame to the Handle* methods; this side issues no requests
// over it (the runner's requests arrive and are answered inline). All
// mutable fields are guarded by Coordinator.mu.
type RunChannel struct {
	BidiSession[OutFrame, OutFrame, OutFrame]
	role string // the harp this channel serves (child harp, or owner harp)
	id   Identity

	// caps is this run's Hello advertisement, captured at attach. It is
	// per-CHANNEL because it is per-run: a resumed harp gets a fresh channel
	// from a fresh runner and may advertise differently, so nothing may
	// cache it against the harp.
	caps map[string]bool

	// Plane-2 request idempotency does NOT live here: it must survive this
	// channel's death so a request reissued on the NEXT dial (same request_id)
	// reuses the in-flight dispatch instead of starting a second one. It lives
	// on Coordinator.reqTrack, keyed (role, request_id) — see HandleRequest.

	// ackSeq is the highest event seq processed on this channel; the
	// cumulative plane-3 Ack advances only through flushedSeq — the highest
	// seq whose item facts are DURABLE (group-fsync on the Ack watermark).
	// items buffers unflushed facts (delta storm kinds). Durable dedupe for
	// journaled fact kinds rides the facts themselves.
	ackSeq     uint64
	flushedSeq uint64
	items      []Fact

	// completed closes exactly once, the moment this channel's run_completed
	// item has been FLUSHED (durably journaled) — drainTerminalTail waits on
	// it before severing the channel.
	// completedOnce guards the close (HandleEvent runs on this channel's
	// single recv goroutine, but drainTerminalTail's safety-net timeout path
	// must never double-close on a concurrent late arrival).
	completed     chan struct{}
	completedOnce sync.Once
}

// Role is the harp this channel serves.
func (ch *RunChannel) Role() string { return ch.role }

// Identity is the credential-derived identity the channel was attached
// under.
func (ch *RunChannel) Identity() Identity { return ch.id }

// AttachRun registers a run channel for the identity's harp once the wire
// adapter has verified the handshake: the presented run_id must be the one
// this credential was minted for (a depth-0 session-owner credential
// attaches with an empty run_id — the channel then serves the owning
// session itself); one channel per role, newest wins (reconnect). cancel is
// the stream context's, so the coordinator can sever the channel (severChan,
// a reconnect, the terminal path).
func (c *Coordinator) AttachRun(id Identity, hello RunHello, cancel context.CancelFunc) (*RunChannel, error) {
	if hello.RunID != id.RunID {
		return nil, fmt.Errorf("%w: run %q", ErrRunNotIssued, hello.RunID)
	}
	caps := make(map[string]bool, len(hello.Capabilities))
	for _, cap := range hello.Capabilities {
		caps[cap] = true
	}
	ch := &RunChannel{
		BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](cancel, 64),
		role:        id.Harp,
		id:          id,
		completed:   make(chan struct{}),
		caps:        caps,
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
		"capabilities": strings.Join(hello.Capabilities, ","),
	})
	// PER-CHILD ATTACH SWEEP: the coordinator's own half of the same
	// reconnect reconciliation the runner does on its side — anything this
	// child wrote or consumed while its channel was down is picked up now
	// rather than at the slow timer. (The runner's own startup sweep is what
	// delivers mail written for it before it dialed home.)
	c.spoolReactor.Mark(id.Harp)
	return ch, nil
}

// HandleEvent processes plane-1 events: the ctxloom custom events
// (mail consumption, park/turn state, harness session), the report kinds
// (Summary, ArtifactProduced — their own reports journal), and
// ITEM events (message/tool-call/run lifecycle), journaled with group-fsync
// on the Ack watermark (items.go): deltas buffer; any boundary event
// flushes; the cumulative Ack advances only through durable seqs. Dedupe is
// (run, seq) against the channel's watermark (and the items fold's own,
// which survives a channel reattach). Every NEW (non-duplicate) event is
// also teed live, full payload, to the watch hub — independent of the
// durable/counts-only journal path below.
func (c *Coordinator) HandleEvent(ch *RunChannel, ev Event) {
	c.mu.Lock()
	if seq := ev.Seq; seq != 0 {
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
		c.rep.Warnf("run %q sent an events_lost marker — that kind is coordinator-emitted only; dropped", ch.id.RunID)
		c.flushItems(ch)
		return
	}
	c.watch.broadcast(ev)

	switch payload := ev.Payload.(type) {
	case CustomEvent:
		c.handleCustomEvent(ch, payload)
		c.flushItems(ch)
	case Summary:
		c.recordSummary(ch.role, ch.id.RunID, ev.Seq, payload)
		c.flushItems(ch)
	case ArtifactProduced:
		if err := c.recordArtifact(ch.role, payload); err != nil {
			c.rep.Warnf("coordinator: journal artifact manifest for %s: %v — the manifest is LOST, "+
				"so any bytes already uploaded for it are unreachable through the log", ch.role, err)
		}
		c.flushItems(ch)
	default:
		if kind := itemKind(ev); kind != "" {
			c.captureRunFailure(ch.role, ev)
			c.bufferItem(ch, ev, kind)
			if kind == "run_completed" {
				// bufferItem flushes run_completed
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
func (c *Coordinator) ackThrough(ch *RunChannel, seq uint64) {
	select {
	case ch.send <- OutFrame{Ack: &Ack{CommittedSeq: seq}}:
	default:
	}
}

// handleCustomEvent serves the ctxloom/* custom event vocabulary.
func (c *Coordinator) handleCustomEvent(ch *RunChannel, ev CustomEvent) {
	switch ev.Name {
	case CustomRecvParked:
		c.onRolePark(ch.role)
	case CustomRecvUnparked:
		c.onRoleUnpark(ch.role)
	case CustomHarnessSession:
		sid, _ := ev.Value["session_id"].(string)
		if sid == "" {
			// The harness-native session id is the run's ONLY resume handle: a
			// child killed mid-run respawns through Launch.Resume.NativeKey,
			// and a one-shot runner will not park its engine without one.
			// bindNativeSession drops an empty id, so losing it here would
			// leave no trace at all — the run simply stops being resumable and
			// nothing says why.
			c.rep.Warnf("coordinator: %s from %s carried no session_id; run %s has no resume handle, so it cannot be resumed by native session key",
				CustomHarnessSession, ch.role, ch.id.RunID)
			return
		}
		c.bindNativeSession(ch.id.Harp, sid)
		// The engine's live loadSession capability (the one-shot gate's
		// live half) rides the SAME custom event as the session id.
		if v, ok := ev.Value["resumable"].(bool); ok {
			c.recordResumable(ch.id.RunID, v)
		}
	case CustomTurnStarted:
		c.onTurnStarted(ch.role)
	case CustomTurnIdle:
		c.onTurnIdle(ch.role)
	}
}

// ReleaseRun is the run channel's teardown: deregister the channel and
// cancel its context.
//
// Deregistration is conditional — only this channel's own registration may
// be removed, never a successor's. A reconnect registers the new channel and
// cancels its predecessor inside one c.mu window, so the OLD handler's
// teardown always observes the successor.
func (c *Coordinator) ReleaseRun(ch *RunChannel) {
	c.mu.Lock()
	if c.chans[ch.role] == ch {
		delete(c.chans, ch.role)
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

// drainTerminalTail closes the terminal-tail race: the
// runner emits a normal exit's final run_completed item on the RunChannel
// and reports RunExited on the SEPARATE RunnerChannel back-to-back — two
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
// production emitter (the engine host's adapt) is contractually guaranteed
// to have just attempted a run_completed. CauseStopped (agent_stop / KillRun)
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
// outstanding request with its ORIGINAL request_id on the fresh channel,
// and the role (child harp) is stable across the reconnect.
type reqKey struct {
	role  string
	reqID string
}

// inflightReq tracks one plane-2 request's SINGLE in-progress dispatch and its
// eventual reply, off the per-connection RunChannel so it outlives a
// reconnect. reply==nil means the dispatch is still running: a reissue that
// finds it must NOT start a second dispatch — the running one answers on
// whichever channel is current when it completes (respondRole). This is the
// trust-critical case: an approval relay parks for minutes waiting on a
// human, and a reconnect in that window must not mint a second relay +
// ladder walk that races the first (a human ACCEPT then answered on the dead
// channel while the live channel bottoms out at DECLINE).
type inflightReq struct {
	reply *AgentReply
}

// HandleRequest serves one plane-2 request. request_id is the
// responder-side idempotency key, scoped (role, request_id) on Coordinator so
// it survives a reconnect: a completed request re-delivers its SAME reply on
// the current channel; an in-flight one is NOT re-dispatched — the original
// dispatch answers on whichever channel is live when it finishes. Handlers run
// on their own goroutine — a spawn (or a human-facing approval relay) can take
// seconds to minutes and must not block the stream's recv loop.
func (c *Coordinator) HandleRequest(ch *RunChannel, req AgentRequest) {
	reqID := req.RequestID
	if reqID == "" {
		c.respond(ch, AgentReply{Err: fmt.Errorf("%w: request_id is required", ErrInvalidRequest)})
		return
	}
	key := reqKey{role: ch.role, reqID: reqID}
	c.mu.Lock()
	if c.reqTrack == nil {
		c.reqTrack = make(map[reqKey]*inflightReq)
	}
	if tr := c.reqTrack[key]; tr != nil {
		reply := tr.reply
		c.mu.Unlock()
		if reply != nil {
			// Already answered: re-deliver the SAME reply on the CURRENT
			// channel (a reconnect dropped the original, or a duplicate frame
			// arrived on one live stream). Idempotent by construction.
			c.respondRole(ch.role, *reply)
		}
		// reply==nil: the original dispatch is still in flight (e.g. an approval
		// relay awaiting a human). It owns the answer and delivers it on the
		// then-current channel — do NOT start a second dispatch.
		return
	}
	tr := &inflightReq{}
	c.reqTrack[key] = tr
	c.mu.Unlock()

	// ch.id is the role's stable identity (same credential across reconnect);
	// the reply is routed to whatever channel is CURRENT at completion, not
	// this ch, which may have died mid-dispatch.
	id := ch.id
	role := ch.role
	c.goTracked(func() {
		reply := c.serveAgentRequest(id, req)
		reply.RequestID = reqID
		c.mu.Lock()
		tr.reply = &reply
		c.mu.Unlock()
		c.respondRole(role, reply)
	})
}

// RefuseRequest answers a request the wire could not decode: the refusal is
// correlated to the request id like any reply, but never dispatched and never
// tracked — a frame that cannot mean a request has nothing to reissue.
func (c *Coordinator) RefuseRequest(ch *RunChannel, requestID string, err error) {
	c.respond(ch, AgentReply{RequestID: requestID, Err: err})
}

// responseQueueWindow bounds how long a plane-2 reply waits for room on a
// saturated writer pump before it is given up on. The wait itself never runs on
// the channel's receive goroutine — see respond.
const responseQueueWindow = 5 * time.Second

// respond queues one reply frame on the channel's writer pump.
//
// A saturated pump must NOT block the caller. respond runs on the
// channel's own RECEIVE goroutine for two paths — a request arriving with no
// request_id, and re-delivery of an already-cached reply to a reissue — and
// waiting there stalls every inbound frame on that channel behind one slow
// writer: acks, mail-consumption facts, park and turn-state transitions. The
// bounded wait is therefore handed to a tracked goroutine, which is also what
// makes the wait worth anything: a pump that drains inside the window now
// DELIVERS the reply instead of dropping it after five seconds of holding
// the receive loop still.
//
// Ordering is not a casualty: every reply is correlated by request_id
// (reqTrack), so one that is overtaken while its pump was full is still matched
// to its own request.
//
// The give-up notice states what actually happens: the runner does not
// reissue on a live channel — its request is bounded by its own timeout and
// fails the call — and only a RECONNECT reissues, at which point reqTrack
// re-delivers the cached reply.
func (c *Coordinator) respond(ch *RunChannel, reply AgentReply) {
	frame := OutFrame{Reply: &reply}
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
			c.rep.Warnf("coordinator: response to %s found its send pump full for %s and was dropped; the runner's request fails at its own timeout — only a reconnect reissues it, and the cached response is re-delivered then",
				role, responseQueueWindow)
		}
	})
}

// respondRole queues a reply on the role's CURRENT live channel — the
// reconnect-safe sibling of respond. A dispatch that outlived the channel it
// arrived on (an approval relay that waited minutes for a human, across a
// reconnect) must answer on whatever channel is live NOW, never the dead one it
// started on. No live channel: drop it — the runner reissues on its next
// reconnect and reqTrack re-delivers the cached reply then.
func (c *Coordinator) respondRole(role string, reply AgentReply) {
	c.mu.Lock()
	ch := c.chans[role]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	c.respond(ch, reply)
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

// serveAgentRequest dispatches one plane-2 request kind onto its verb — thin
// translation; the verbs do not change.
func (c *Coordinator) serveAgentRequest(caller Identity, req AgentRequest) AgentReply {
	switch kind := req.Kind.(type) {
	case SpawnRequest:
		out, err := c.Spawn(c.baseCtx, caller, kind)
		if err != nil {
			return AgentReply{Err: err}
		}
		return AgentReply{Message: out.Disposition, Result: out}
	case RosterRequest:
		return c.serveRoster(caller, kind)
	case StopRun:
		// The bulk shape waits on the drain, bounded; baseCtx is the only
		// ctx a plane-2 dispatch has, and Close settles the drain anyway.
		return c.serveStopRun(c.baseCtx, caller, kind)
	case ControlRequest:
		// Same ctx reasoning: each verb applies its own budget to baseCtx.
		by := ControlInitiator{Kind: InitiatorAgent, Harp: caller.Harp}
		out, err := c.Control(c.baseCtx, by, kind)
		if err != nil {
			return AgentReply{Err: fmt.Errorf("%s: %w", ControlToolName(kind.Verb), err)}
		}
		return AgentReply{Message: controlDisposition(kind, out), Result: out}
	case HostRequest:
		res, err := c.Host(c.baseCtx, caller, kind)
		if err != nil {
			return AgentReply{Err: err}
		}
		return AgentReply{Result: res}
	default:
		return AgentReply{Err: ErrUnsupportedRequest}
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

// serveRoster is the roster: the caller's children from the roster/runs
// folds (single state, N transports — consumer.go's listRunsSnapshot is the
// shared projection; the ConsumerService is a further transport onto the
// same state).
func (c *Coordinator) serveRoster(caller Identity, req RosterRequest) AgentReply {
	if caller.IsChild() {
		return AgentReply{Err: ErrRosterIsTheOwners}
	}
	return AgentReply{Result: c.listRunsSnapshot(req.IncludeTerminal, req.Role)}
}

// serveStopRun is agent_stop on the wire, in its two shapes. With a run_id
// the target is resolved to its harp and ownership-checked against the
// REQUESTER's lineage — only the run's parent may stop it. With NO run_id it
// is the bulk sweep, whose reason the Stop verb requires.
func (c *Coordinator) serveStopRun(ctx context.Context, caller Identity, req StopRun) AgentReply {
	sr := StopRequest{Reason: req.Reason}
	if runID := req.RunID; runID != "" {
		var rec *RunRecord
		c.runs.View(func() {
			if r := c.runsF.run(runID); r != nil {
				cp := *r
				rec = &cp
			}
		})
		if rec == nil || rec.ParentHarp != caller.Harp {
			return AgentReply{Err: Refusal(ErrNotAChild, "agent_stop: run %q is not a child of this session", runID)}
		}
		sr.Harp = rec.Harp
	}
	out, err := c.Stop(ctx, caller, sr)
	if err != nil {
		return AgentReply{Err: err}
	}
	return AgentReply{Message: out.Disposition, Result: out}
}
