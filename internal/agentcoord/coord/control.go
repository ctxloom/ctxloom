package coord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Plane 2, the DOWN direction: coordinator→agent control requests (steer,
// question, summarize, pause, resume). This file holds the coordinator half —
// the guard chain every verb runs, the request tracker that survives a
// RunChannel reconnect, and the DRAIN SEAM where a request queued under one run
// is re-validated before it is handed to another.
//
// ONE OBJECT, ONE LIFECYCLE (human ruling, 2026-07-30, findings F1/F2). The
// instruction body rides the REQUEST — it is not also queued as coordinator
// mail. An earlier design had one steer produce two in-flight objects (a
// request on the wire and a durable mail record) with no ordering or atomicity
// between them, which made five distinct desynchronisations reachable, two of
// which reported success on a failure. Collapsing to one object is the point:
// a body that only exists as part of the request cannot outlive the request's
// own rejection, cannot be delivered twice, and cannot arrive after the caller
// was told the steer failed.
//
// The accepted cost, stated rather than discovered: the body does NOT survive a
// runner PROCESS restart. It is parked in the runner's existing Home buffer,
// which dies with the process. That is judged correct for a ~60-second control
// action against an attached target — a steer whose target has been replaced by
// a fresh process is a steer whose premise is gone.

// ControlInitiator names who asked for a control action: the audit identity,
// the privilege discriminator, and the mirror-notice policy key.
//
// Kind is a CLOSED ENUM, not a bool. This value decides whether the caller may
// control a run the coordinator holds, so an unrecognised initiator must fail
// closed rather than fall into whichever branch a bool happened to select.
type ControlInitiator struct {
	Kind agentcoordpb.ControlInitiatorKind
	// Harp MUST be set iff Kind == AGENT, and MUST be empty otherwise. The
	// human has no harp; an agent that does not name itself cannot be
	// ownership-checked.
	Harp string
}

// Validate enforces the Kind/Harp pairing, so the invalid combinations a bool
// made representable cannot be constructed silently.
func (i ControlInitiator) Validate() error {
	switch i.Kind {
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN:
		if i.Harp != "" {
			return fmt.Errorf("control initiator: HUMAN carries no harp, but %q was set", i.Harp)
		}
		return nil
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT:
		if i.Harp == "" {
			return errors.New("control initiator: AGENT must name the initiating harp (it is the ownership check's whole input)")
		}
		return nil
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_UNSPECIFIED:
		return errors.New("control initiator: UNSPECIFIED is not an initiator; a control action must say who asked for it")
	default:
		return fmt.Errorf("control initiator: unrecognised kind %d — refused rather than defaulted, "+
			"because an initiator this build does not know must not inherit another's privileges", int32(i.Kind))
	}
}

// auditName is the identity a control action is journaled under.
func (i ControlInitiator) auditName() string {
	if i.Kind == agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT {
		return i.Harp
	}
	return UserSender
}

// SteerOutcome reports how a steer actually landed. Exactly one of the two is
// meaningful: Applied when the steer rode plane-2 to an attached run, Fallback
// (a Delivery* mode) when §5.6's mailbox route carried it instead.
type SteerOutcome struct {
	Applied  agentcoordpb.SteerResult_Applied
	Fallback string
	// MessageID is the WITHDRAW HANDLE, set only on the spool route
	// (spoolcontrol.go): under the cutover a steer is a durable file, and this
	// is the id WithdrawSteer takes to retract it before the target reads it.
	// Empty on the plane-2 route, where the body rides the request and there
	// is nothing durable to retract.
	MessageID string
}

// ErrCapabilityUnavailable marks every refusal whose CAUSE is that the target
// run does not (or no longer does) advertise a capability the request needs.
//
// It is typed because §5.6's fallback selection has to key on it: "this run
// cannot do steer" routes to the mailbox, while "the runner answered an error"
// or "the request timed out" must not. Matching on a gRPC code alone cannot
// draw that line — FAILED_PRECONDITION is answered for several reasons — and
// matching on prose is not a contract.
var ErrCapabilityUnavailable = errors.New("the target run does not advertise a capability this request requires")

// ErrControlTargetEnded answers an outstanding control request whose target run
// reached its terminal while the request was in flight. Distinct from a
// capability refusal: nothing about the request was wrong, the addressee is
// simply gone.
var ErrControlTargetEnded = errors.New("the target run ended before it answered this control request")

// capUnavailableError is a capability refusal that is BOTH a gRPC status (so a
// wire caller keeps FAILED_PRECONDITION and the prose naming the gap and the
// advertisement) and errors.Is-able against ErrCapabilityUnavailable (so an
// in-process caller with a fallback routes on the cause, not on the code).
type capUnavailableError struct{ st *status.Status }

func (e capUnavailableError) Error() string              { return e.st.Message() }
func (e capUnavailableError) GRPCStatus() *status.Status { return e.st }
func (e capUnavailableError) Is(target error) bool       { return target == ErrCapabilityUnavailable }
func capUnavailable(format string, a ...any) capUnavailableError {
	return capUnavailableError{st: status.Newf(codes.FailedPrecondition, format, a...)}
}

// downReq tracks ONE outstanding coordinator→agent request. It lives on the
// Coordinator rather than the runChan because it must survive that channel's
// death: the request is reissued with the SAME request_id on the role's next
// attach, and the runner's own responder idempotency makes that safe.
type downReq struct {
	req  *agentcoordpb.CoordinatorRequest
	done chan downResult // buffered 1; written exactly once
}

type downResult struct {
	resp *agentcoordpb.AgentResponse
	err  error
}

// settle completes the waiter at most once. Every terminal path (response,
// drain-seam rejection, run terminal) funnels through here so a request cannot
// be answered twice.
func (d *downReq) settle(res downResult) {
	select {
	case d.done <- res:
	default:
	}
}

// controlRequestBudget bounds one plane-2 down request when the caller's ctx
// carries no deadline. A control action is a foreground command against an
// attached target: the caller is waiting, and §5.6 has a fallback that is only
// useful if the answer arrives while it still matters.
const controlRequestBudget = 60 * time.Second

// requestRun runs ONE plane-2 down request to completion against harp's live
// RunChannel: correlated by request_id, tracked on downTrack so it survives a
// reconnect (reissued with the same id on the role's next attach), and bounded
// by ctx. It is the mirror of Home.Request with the roles swapped.
func (c *Coordinator) requestRun(ctx context.Context, harp string, req *agentcoordpb.CoordinatorRequest) (*agentcoordpb.AgentResponse, error) {
	if req.GetRequestId() == "" {
		req.RequestId = randID("ctl-", 12)
	}
	key := reqKey{role: harp, reqID: req.GetRequestId()}
	dr := &downReq{req: req, done: make(chan downResult, 1)}

	c.mu.Lock()
	ch := c.chans[harp]
	if ch == nil {
		c.mu.Unlock()
		return nil, capUnavailable("run %q has no attached run channel, so no control request can reach it", harp)
	}
	if c.downTrack == nil {
		c.downTrack = make(map[reqKey]*downReq)
	}
	c.downTrack[key] = dr
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.downTrack, key)
		c.mu.Unlock()
	}()

	c.sendRequest(ch, req)

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, controlRequestBudget)
		defer cancel()
	}
	select {
	case res := <-dr.done:
		return res.resp, res.err
	case <-ctx.Done():
		// The request may still be executing at the runner. Say so rather than
		// implying withdrawal: there is no CancelRequest producer, and the
		// runner's responder idempotency means a retry with the same id joins
		// the original dispatch instead of starting a second one.
		return nil, fmt.Errorf("control request %s to %s did not answer within its budget "+
			"(it may still be executing at the runner; a retry with the same request id joins that dispatch rather than starting a second): %w",
			req.GetRequestId(), harp, ctx.Err())
	case <-c.baseCtx.Done():
		return nil, errors.New("coordinator is shutting down")
	}
}

// sendRequest queues one down-request frame on the channel's writer pump,
// never blocking the caller on a saturated pump — the same discipline respond
// follows, and for the same reason: a full pump must not stall the command that
// is waiting on this request, which has its own budget and its own fallback.
func (c *Coordinator) sendRequest(ch *runChan, req *agentcoordpb.CoordinatorRequest) {
	frame := &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Request{Request: req}}
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
			clidiag.Warn("ctxloom", "coordinator: control request to %s found its send pump full for %s and was dropped; the caller fails at its own budget and the next reconnect reissues it",
				role, responseQueueWindow)
		}
	})
}

// handleAgentResponse resolves one plane-2 down response against downTrack.
// Unknown or duplicate request ids are ignored: a response whose waiter is gone
// (timed out, or already settled by the drain seam) has nowhere to land, and
// settle's single-write discipline makes the duplicate harmless either way.
func (c *Coordinator) handleAgentResponse(ch *runChan, resp *agentcoordpb.AgentResponse) {
	c.mu.Lock()
	dr := c.downTrack[reqKey{role: ch.role, reqID: resp.GetRequestId()}]
	c.mu.Unlock()
	if dr == nil {
		return
	}
	dr.settle(downResult{resp: resp})
}

// clearDownTrack settles a role's outstanding down requests at the TERMINAL
// seam (terminateRun). A request whose target ended has no addressee and no
// prospect of one: the run record is closed, so it must fail now rather than
// wait out its budget against a run that will never answer.
func (c *Coordinator) clearDownTrack(role string) {
	c.mu.Lock()
	var settle []*downReq
	for k, dr := range c.downTrack {
		if k.role == role {
			settle = append(settle, dr)
			delete(c.downTrack, k)
		}
	}
	c.mu.Unlock()
	for _, dr := range settle {
		dr.settle(downResult{err: fmt.Errorf("%w: %s", ErrControlTargetEnded, role)})
	}
}

// redrainDownRequests is the DRAIN SEAM: the receiver-normative capability
// check, run at the one moment where a queued request can meet a run that is
// not the run it was addressed to.
//
// Requests outlive channels. A request is reissued with its original request_id
// when the role next attaches — and the attaching run may not be the run that
// was addressed: a runner that dies WITHOUT a terminal (crash, kill -9,
// container eviction) leaves the request tracked while a relaunch brings up a
// fresh run whose Hello advertises whatever THAT process can do. Capabilities
// are per-Hello, which is to say per-RUN, so this reattachment is the moment of
// maximum staleness and the only place the check can be made against a truth
// that has actually changed.
//
// Survivors are reissued. Everything else is TERMINALLY rejected — never held
// for later, which is a liveness hazard: a request parked against a capability
// that may never return is a caller blocked forever with a fallback it was
// never allowed to take. Rejecting BEFORE delivery is what leaves the action
// unconsumed, so §5.6's fallback can still route it.
//
// The check is set containment — declared ⊆ advertised — over the request's own
// self-describing required_capabilities. It deliberately holds NO kind→capability
// table: this seam should not have to be edited every time a request kind is
// added, and a table here that drifted from the receiver's would be a check that
// looked present and guarded the wrong thing. The declared field is not the
// boundary; the runner's own backstop keys on the ACTUAL request kind, so a
// request declaring less than it carries is still refused at the far end.
func (c *Coordinator) redrainDownRequests(ch *runChan) {
	c.mu.Lock()
	type pending struct {
		key reqKey
		dr  *downReq
	}
	var queued []pending
	for k, dr := range c.downTrack {
		if k.role == ch.role {
			queued = append(queued, pending{key: k, dr: dr})
		}
	}
	advertised := make(map[string]bool, len(ch.caps))
	for name := range ch.caps {
		advertised[name] = true
	}
	c.mu.Unlock()
	if len(queued) == 0 {
		return
	}
	// Stable order so a reissue burst replays in request-id order rather than
	// map order — reconnect behaviour that differs run to run is not testable.
	sort.Slice(queued, func(i, j int) bool { return queued[i].key.reqID < queued[j].key.reqID })

	for _, p := range queued {
		missing := missingCapabilities(p.dr.req.GetRequiredCapabilities(), advertised)
		if len(missing) == 0 {
			c.sendRequest(ch, p.dr.req)
			continue
		}
		c.mu.Lock()
		delete(c.downTrack, p.key)
		c.mu.Unlock()
		c.audit("control_reject", ch.role, map[string]string{
			"request_id": p.key.reqID,
			"missing":    strings.Join(missing, ","),
			"advertised": strings.Join(sortedKeys(advertised), ","),
			"at":         "drain_seam",
		})
		p.dr.settle(downResult{err: capUnavailable(
			"run %q no longer offers %s, so a control request queued for an earlier run is refused rather than delivered to this one "+
				"(it advertised: %s; capabilities ride each run's Hello, so a relaunched or resumed run may advertise differently)",
			ch.role, strings.Join(missing, ", "), strings.Join(sortedKeys(advertised), ", "))})
	}
}

// missingCapabilities is the whole drain-seam predicate: which declared
// capabilities the advertisement does not contain. Empty result = deliver.
func missingCapabilities(declared []string, advertised map[string]bool) []string {
	var missing []string
	for _, want := range declared {
		if want == "" || advertised[want] {
			continue
		}
		missing = append(missing, want)
	}
	sort.Strings(missing)
	return missing
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// controlTarget runs guards 1–4 shared by every control verb and returns the
// target's run record. The ORDER is load-bearing and the switch is exhaustive
// on purpose — see each guard's comment.
func (c *Coordinator) controlTarget(by ControlInitiator, harp string) (*RunRecord, error) {
	if err := by.Validate(); err != nil {
		return nil, err
	}
	// Guard 1 — target exists.
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil {
		return nil, fmt.Errorf("control: %q is not a child of this coordinator: %w", harp, ErrNotInjectable)
	}
	// Guard 2 — self-target refused. In the owner-run topology the owned run
	// reuses the coordinating session's own harp as BOTH its own harp and its
	// journaled parent harp (StartOwnedRun, owner_run.go) — a self-loop by
	// construction, independent of depth (the owned run is depth 0, same as
	// the session owner it IS, not a child of it). Without this fence a
	// session would pass its own ownership check via that self-loop and could
	// steer, pause or question ITSELF — a loop with no floor.
	if harp == by.Harp {
		return nil, fmt.Errorf("control: %q cannot control itself", harp)
	}
	// Guard 3 — ownership. Exhaustive, with no default-allow arm: an initiator
	// this build does not recognise must be REFUSED, not quietly handed the
	// narrower branch's privileges. A future initiator whose policy has not
	// been designed would otherwise inherit child-control by default.
	switch by.Kind {
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN:
		// The human may control any run this coordinator holds — Inject's
		// existing rule, unchanged.
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT:
		if rec.ParentHarp != by.Harp {
			return nil, fmt.Errorf("control: %q is not the parent of %q; a coordinating agent controls only its own children", by.Harp, harp)
		}
	default:
		return nil, fmt.Errorf("control: initiator kind %d is refused", int32(by.Kind))
	}
	return rec, nil
}

// ControlSteer delivers an instruction into a running target.
//
// It is ONE object end to end (ruling F1/F2): the body rides the request, the
// runner parks it in its own recv buffer and injects only a reminder, and the
// agent pulls the body with agent_recv. There is no second in-flight copy, so
// there is nothing that can still be delivered after this call reports a
// failure, and nothing to withdraw when it does.
//
// Sequence, fixed: guards → capability → requestRun → mirror. The capability
// check precedes the send precisely so a refusal happens before anything is
// queued anywhere.
//
// A target that cannot take a plane-2 steer — not attached, not migrated, or
// attached but not advertising `steer` — takes §5.6's mailbox fallback, which
// is today's Inject behaviour unchanged. Steer is the one verb with a genuine
// fallback, so a capability refusal ROUTES rather than fails: that is what
// runCapability exists for ("a caller with a fallback learns now that it needs
// one"). Verbs with no fallback refuse instead.
func (c *Coordinator) ControlSteer(ctx context.Context, by ControlInitiator, harp, text string) (SteerOutcome, error) {
	rec, err := c.controlTarget(by, harp)
	if err != nil {
		return SteerOutcome{}, err
	}
	c.audit("agent_steer", by.auditName(), map[string]string{"harp": harp})

	sender := by.auditName()
	var outcome SteerOutcome
	// THE CUTOVER (spoolcontrol.go). A steer to a spool-delivered target is a
	// DURABLE FILE, not a wire request: it survives a relaunch, an unread one
	// is visible in in/, and it can be withdrawn. The branch is taken BEFORE
	// plane 2 — a cut-over child is migrated and attached, so plane 2 would
	// otherwise always win and the durable route would be unreachable — and it
	// uses the SAME predicate the mail plane does, because a steer written
	// where nothing sweeps is an instruction that silently never arrives.
	if c.spoolDeliverTo(harp) {
		if outcome, err = c.steerViaSpool(sender, harp, text); err != nil {
			return SteerOutcome{}, err
		}
	} else {
		out, planeTwo, perr := c.steerPlaneTwo(ctx, by, sender, harp, text)
		if perr != nil {
			return SteerOutcome{}, perr
		}
		outcome = out
		if !planeTwo {
			if outcome, err = c.steerFallback(sender, harp, text); err != nil {
				return SteerOutcome{}, err
			}
		}
	}
	// Mirror notice (decision O3), preserved verbatim across BOTH routes: a
	// human injection always tells the target's parent, so a coordinator's
	// picture of its child never diverges without a trace. An AGENT initiator
	// gets no mirror — the parent IS the initiator.
	if by.Kind == agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN {
		if _, _, merr := c.queueMail(harp, rec.ParentHarp, KindUserInjected, injectDigest(text)); merr != nil {
			clidiag.Warn("ctxloom", "steer %s: mirror notice: %v", harp, merr)
		}
	}
	return outcome, nil
}

// steerPlaneTwo attempts the plane-2 route. The bool reports whether plane-2
// OWNED the outcome; false means "route to the fallback", and only a genuine
// error (the runner answered a failure, or the request never answered) returns
// one.
func (c *Coordinator) steerPlaneTwo(ctx context.Context, by ControlInitiator, sender, harp, text string) (SteerOutcome, bool, error) {
	c.mu.Lock()
	rt := c.byHarp[harp]
	migrated := rt != nil && rt.viaStartRun
	c.mu.Unlock()
	if !migrated {
		return SteerOutcome{}, false, nil // legacy child: mailbox, today's semantics
	}
	if err := c.runCapability(harp, CapSteer); err != nil {
		return SteerOutcome{}, false, nil // unattached or not advertising: mailbox
	}

	meta, merr := structpb.NewStruct(map[string]any{
		"initiator": initiatorName(by.Kind),
		"from":      sender,
	})
	if merr != nil { // impossible for two strings; never swallow it silently
		return SteerOutcome{}, false, fmt.Errorf("steer: build metadata: %w", merr)
	}
	req := &agentcoordpb.CoordinatorRequest{
		RequestId:            randID("ctl-", 12),
		RequiredCapabilities: []string{CapSteer},
		Kind: &agentcoordpb.CoordinatorRequest_Steer{Steer: &agentcoordpb.SteerRequest{
			Text:     text,
			Metadata: meta,
		}},
	}
	resp, err := c.requestRun(ctx, harp, req)
	if err != nil {
		if errors.Is(err, ErrCapabilityUnavailable) {
			// Refused at the drain seam (or the channel went away before the
			// send): the request was never handed over, so the action is
			// unconsumed and §5.6 can still route it.
			return SteerOutcome{}, false, nil
		}
		return SteerOutcome{}, true, err
	}
	if st := resp.GetStatus(); st != nil && st.GetCode() != 0 {
		if codes.Code(st.GetCode()) == codes.Unimplemented {
			// The receiver's own backstop refused the kind. Receiver-normative:
			// its answer is the authority, and steer has a fallback.
			return SteerOutcome{}, false, nil
		}
		return SteerOutcome{}, true, fmt.Errorf("steer %s: %s", harp, st.GetMessage())
	}
	return SteerOutcome{Applied: resp.GetSteer().GetApplied()}, true, nil
}

// steerFallback is §5.6's mailbox route: today's Inject behaviour, unchanged
// and unconditional, so initiator B loses nothing on any target. The body is
// queued as ordinary mail HERE and only here — the plane-2 route never queues
// mail, so the two can never both hold a copy.
func (c *Coordinator) steerFallback(sender, harp, text string) (SteerOutcome, error) {
	_, completed, err := c.queueMail(sender, harp, "", text)
	if err != nil {
		return SteerOutcome{}, err
	}
	if completed {
		return SteerOutcome{Fallback: DeliveryCompletedRecv}, nil
	}
	mode, _ := deliveryDisposition(c.driveQueued(harp))
	return SteerOutcome{Fallback: mode}, nil
}

func initiatorName(k agentcoordpb.ControlInitiatorKind) string {
	switch k {
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN:
		return "human"
	case agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT:
		return "agent"
	default:
		return "unspecified"
	}
}
