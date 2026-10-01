package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ApprovalHost is the approval route as the session's endpoint serves it:
// the approval hook's POST (Hook: the engine's native payload in, its native
// decision out) and the engine's permission host tool (Host: always a deny,
// returned only once the hook has decided or failed).
type ApprovalHost interface {
	Hook(ctx context.Context, event string, payload []byte) ([]byte, error)
	Host(ctx context.Context, args json.RawMessage) (string, error)
}

// The refusals the route answers without asking anyone. Each one reaches the
// engine as a deny whose message is the refusal.
var (
	// errUncorrelated: the request names no tool call this turn made, or
	// one whose result already arrived.
	errUncorrelated = errors.New("ctxloom: the permission request names no open tool call of this turn")
	// errMismatch: the request names a call of this turn, but not the tool
	// or the input that call carried.
	errMismatch = errors.New("ctxloom: the permission request does not match the tool call it names")
	// errHostCalled: the permission host itself is never a tool a request
	// may ask about — a model calling it gets nothing.
	errHostCalled = errors.New("ctxloom: the permission host is not a tool anyone may be granted")
	// errNoHostAnchor: an ask that carries no call id binds only to a
	// permission host call holding the same request open; there is none.
	errNoHostAnchor = errors.New("ctxloom: no permission host call is holding this request open")
	// errHookSilent: the permission host held the request, and no approval
	// hook carried a decision for it.
	errHookSilent = errors.New("ctxloom: the approval hook did not answer")
	// errSuperseded: the host's own answer, given after the hook decided —
	// the engine has already applied the hook's decision.
	errSuperseded = errors.New("ctxloom: superseded — the approval hook decided this request")
	// errTurnEnded: the turn's engine process ended with the request open.
	errTurnEnded = errors.New("ctxloom: the turn ended before the request was decided")
	// errNoDecision: the coordinator gave no decision for the request.
	errNoDecision = errors.New("ctxloom: the coordinator gave no decision")
)

// The bounds of the hold (approval route design, §2.2). anchorWait covers the
// engine's events reaching the ledger after the engine already acted on them
// (the hook and the host call race the stream); arrivalGrace is how long a
// held request waits for its hook to bind.
const (
	approvalAnchorWait   = 5 * time.Second
	approvalArrivalGrace = 30 * time.Second
	// approvalRequestSlack bounds the runner's wait on the coordinator past
	// the approval timeout the coordinator enforces itself: it guards a dead
	// coordinator, not the human.
	approvalRequestSlack = 30 * time.Second
	// approvalHoldSlack bounds the host's hold past the approval timeout:
	// beyond the hook's own timeout (agent.ApprovalHookSlack), so the host
	// never answers while a hook may still carry the decision.
	approvalHoldSlack = 90 * time.Second
)

// approvalBounds are one route's waits, derived from the approval timeout.
type approvalBounds struct {
	anchorWait, arrivalGrace, hold, request time.Duration
}

func boundsFor(timeout time.Duration) approvalBounds {
	return approvalBounds{
		anchorWait:   approvalAnchorWait,
		arrivalGrace: approvalArrivalGrace,
		hold:         timeout + approvalHoldSlack,
		request:      timeout + approvalRequestSlack,
	}
}

// approvalSpec is what a launch hands the engine host to serve the route:
// the engine's codec, the postures an approval may move the session to, and
// the approval timeout. nil when the launch's approver is not the human.
type approvalSpec struct {
	codec       engine.ApprovalCodec
	transitions []engine.PostureTransition
	timeout     time.Duration
}

// afterFunc is time.AfterFunc's shape, injected so tests drive the bounds.
type afterFunc func(d time.Duration, f func()) (stop func() bool)

func realAfter(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// approvals is the route's runner half for one run: a ledger of the current
// turn's tool calls (fed from the engine's stream), the permission host's
// slots (one per held call), and the decision each correlated request waits
// on. The host is an ANCHOR: it opens a slot for a call the ledger knows and
// holds it, and it never answers allow — the hook carries the decision.
type approvals struct {
	codec  engine.ApprovalCodec
	decide func(ctx context.Context, ask engine.PermissionAsk) (engine.PermissionAnswer, error)
	after  afterFunc
	bounds approvalBounds
	// armed, when set, is told each time a bounded wait has armed its
	// timer — the point after which advancing the clock expires it. Tests
	// only.
	armed func()

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
	turn    *approvalTurn
}

// approvalTurn is one engine process's state: everything correlated within
// it ends with it.
type approvalTurn struct {
	ended     chan struct{}
	calls     map[string]*toolCall
	slots     []*hostSlot // open, oldest first
	decisions map[string]*decision
}

func newApprovalTurn() *approvalTurn {
	return &approvalTurn{ended: make(chan struct{}), calls: map[string]*toolCall{}, decisions: map[string]*decision{}}
}

// toolCall is one tool call the engine announced this turn.
type toolCall struct {
	tool     string
	input    []byte // agent.CanonicalJSON of the call's input
	resulted bool
}

// hostSlot is one permission host call holding a request open.
type hostSlot struct {
	id    string
	call  *toolCall
	bound *decision
}

// decision is the one decision every request correlated to a call waits on.
type decision struct {
	done chan struct{}
	ans  engine.PermissionAnswer
	err  error
}

func newApprovals(spec approvalSpec, decide func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error)) *approvals {
	return &approvals{
		codec: spec.codec, decide: decide, after: realAfter, bounds: boundsFor(spec.timeout),
		changed: make(chan struct{}), turn: newApprovalTurn(),
	}
}

// broadcastLocked wakes every waiter. Call with a.mu held.
func (a *approvals) broadcastLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// observe feeds the ledger from the engine's stream: a tool call opens an
// entry, its result closes it.
func (a *approvals) observe(e *agent.SessionEntry) {
	if e == nil || e.ToolCallID == "" {
		return
	}
	switch e.Type {
	case agent.EntryTypeToolUse:
		input, err := canonicalInput(e.ToolInput)
		if err != nil {
			return // an input the ledger cannot canonicalise anchors nothing
		}
		a.mu.Lock()
		a.turn.calls[e.ToolCallID] = &toolCall{tool: e.ToolName, input: input}
		a.broadcastLocked()
		a.mu.Unlock()
	case agent.EntryTypeToolResult:
		a.mu.Lock()
		if c := a.turn.calls[e.ToolCallID]; c != nil {
			c.resulted = true
			a.broadcastLocked()
		}
		a.mu.Unlock()
	}
}

// endTurn ends the turn's correlation: every held request is released (the
// engine process that asked is gone) and the ledger starts empty.
func (a *approvals) endTurn() {
	a.mu.Lock()
	close(a.turn.ended)
	a.turn = newApprovalTurn()
	a.broadcastLocked()
	a.mu.Unlock()
}

// await blocks until cond — evaluated under a.mu, and free to mutate — holds,
// ctx ends, or d elapses; it reports whether cond held.
func (a *approvals) await(ctx context.Context, d time.Duration, cond func() bool) bool {
	expired := make(chan struct{})
	stop := a.after(d, func() { close(expired) })
	defer stop()
	if a.armed != nil {
		a.armed()
	}
	for {
		a.mu.Lock()
		ok := cond()
		ch := a.changed
		a.mu.Unlock()
		if ok {
			return true
		}
		select {
		case <-ch:
		case <-expired:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// Host serves the permission host: it holds the request open and answers,
// always, with a deny — superseded once the hook has decided, or the reason
// the request could not be held or was never decided.
func (a *approvals) Host(ctx context.Context, args json.RawMessage) (string, error) {
	return a.codec.HostDeny(a.hold(ctx, args).Error())
}

// hold is Host's body: the reason the host answers.
func (a *approvals) hold(ctx context.Context, args json.RawMessage) error {
	call, err := a.codec.HostCall(args)
	if err != nil {
		return fmt.Errorf("%w: %v", errUncorrelated, err)
	}
	if isHostTool(call.Tool) {
		return errHostCalled
	}
	input, err := canonicalInput(call.Input)
	if err != nil {
		return fmt.Errorf("%w: %v", errMismatch, err)
	}
	var turn *approvalTurn
	var tc *toolCall
	if !a.await(ctx, a.bounds.anchorWait, func() bool {
		turn = a.turn
		tc = turn.calls[call.ToolUseID]
		return tc != nil
	}) {
		return errUncorrelated
	}
	slot, err := a.openSlot(turn, call.ToolUseID, tc, call.Tool, input)
	if err != nil {
		return err
	}
	defer a.closeSlot(turn, slot)

	ended := func() bool { return isClosed(turn.ended) }
	if !a.await(ctx, a.bounds.arrivalGrace, func() bool { return slot.bound != nil || ended() }) {
		return errHookSilent
	}
	if ended() {
		return errTurnEnded
	}
	d := slot.bound
	a.await(ctx, a.bounds.hold, func() bool {
		return tc.resulted || ended() || (isClosed(d.done) && d.err != nil)
	})
	if isClosed(d.done) && d.err != nil {
		return errHookSilent
	}
	return errSuperseded
}

// openSlot opens the host's slot for a call the ledger knows, refusing one
// whose result already arrived, one whose tool or input differs from the
// call's, a call to the host itself, and a second host call for the same id.
// A call a pre-tool hook already correlated by its id joins that decision.
func (a *approvals) openSlot(turn *approvalTurn, id string, tc *toolCall, tool string, input []byte) (*hostSlot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case tc.resulted:
		return nil, errUncorrelated
	case isHostTool(tc.tool):
		return nil, errHostCalled
	case tc.tool != tool || !bytes.Equal(tc.input, input):
		return nil, errMismatch
	}
	for _, s := range turn.slots {
		if s.id == id {
			return nil, errUncorrelated
		}
	}
	s := &hostSlot{id: id, call: tc, bound: turn.decisions[id]}
	turn.slots = append(turn.slots, s)
	a.broadcastLocked()
	return s, nil
}

func (a *approvals) closeSlot(turn *approvalTurn, s *hostSlot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, open := range turn.slots {
		if open == s {
			turn.slots = append(turn.slots[:i], turn.slots[i+1:]...)
			break
		}
	}
	a.broadcastLocked()
}

// Hook serves the approval hook: the ask is correlated to a call of this
// turn, decided once by the root (every correlated POST waits on the same
// decision), and written back as the engine's native answer. A payload the
// codec cannot read is an error — no decision, so the held host denies.
func (a *approvals) Hook(ctx context.Context, event string, payload []byte) ([]byte, error) {
	ask, err := a.codec.DecodeAsk(event, payload)
	if err != nil {
		return nil, err
	}
	d, owner, err := a.bind(ctx, ask)
	if err != nil {
		return a.codec.EncodeAnswer(event, ask, denial(err))
	}
	if owner {
		ans, derr := a.decide(ctx, ask)
		a.settle(d, ans, derr)
	}
	select {
	case <-d.done:
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		// The hook that asked is gone: nobody reads an answer.
		return nil, err
	}
	if d.err != nil {
		return a.codec.EncodeAnswer(event, ask, denial(d.err))
	}
	return a.codec.EncodeAnswer(event, ask, d.ans)
}

// bind correlates an ask to its decision, reporting whether this caller
// owns it (and so must have it decided). An ask carrying the call's id is
// correlated by the ledger; one without is bound to the oldest host slot
// holding the same tool and input — the oldest UNBOUND one, so identical
// parallel calls are taken in order, else the oldest bound one, which a
// repeated POST for the same request joins.
func (a *approvals) bind(ctx context.Context, ask engine.PermissionAsk) (*decision, bool, error) {
	if isHostTool(ask.Tool) {
		return nil, false, errHostCalled
	}
	input, err := canonicalInput(ask.Input)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errMismatch, err)
	}
	if ask.ToolUseID != "" {
		return a.bindByID(ctx, ask.ToolUseID, ask.Tool, input)
	}
	var d *decision
	owner := false
	if !a.await(ctx, a.bounds.anchorWait, func() bool {
		d, owner = a.turn.bindSlotLocked(ask.Tool, input)
		if d != nil {
			a.broadcastLocked()
		}
		return d != nil
	}) {
		return nil, false, errNoHostAnchor
	}
	return d, owner, nil
}

// bindSlotLocked binds the oldest matching slot (see bind). Call with a.mu
// held.
func (t *approvalTurn) bindSlotLocked(tool string, input []byte) (*decision, bool) {
	var joined *hostSlot
	for _, s := range t.slots {
		if s.call.tool != tool || !bytes.Equal(s.call.input, input) {
			continue
		}
		if s.bound == nil {
			s.bound = &decision{done: make(chan struct{})}
			t.decisions[s.id] = s.bound
			return s.bound, true
		}
		if joined == nil {
			joined = s
		}
	}
	if joined != nil {
		return joined.bound, false
	}
	return nil, false
}

// bindByID correlates an ask that names its call: the call must be this
// turn's, still open, and carry the ask's tool and input.
func (a *approvals) bindByID(ctx context.Context, id, tool string, input []byte) (*decision, bool, error) {
	var tc *toolCall
	var turn *approvalTurn
	if !a.await(ctx, a.bounds.anchorWait, func() bool {
		turn = a.turn
		tc = turn.calls[id]
		return tc != nil
	}) {
		return nil, false, errUncorrelated
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case tc.resulted || isClosed(turn.ended):
		return nil, false, errUncorrelated
	case tc.tool != tool || !bytes.Equal(tc.input, input):
		return nil, false, errMismatch
	}
	if d := turn.decisions[id]; d != nil {
		return d, false, nil
	}
	d := &decision{done: make(chan struct{})}
	turn.decisions[id] = d
	for _, s := range turn.slots {
		if s.id == id && s.bound == nil {
			s.bound = d
		}
	}
	a.broadcastLocked()
	return d, true, nil
}

func (a *approvals) settle(d *decision, ans engine.PermissionAnswer, err error) {
	a.mu.Lock()
	d.ans, d.err = ans, err
	close(d.done)
	a.broadcastLocked()
	a.mu.Unlock()
}

// denial is the answer a refusal reaches the engine as.
func denial(err error) engine.PermissionAnswer {
	return engine.PermissionAnswer{Allow: false, Message: err.Error()}
}

// isHostTool reports whether tool is the permission host itself, under its
// own name or the name an engine qualifies it with.
func isHostTool(tool string) bool {
	return tool == engine.PermissionHostTool || strings.HasSuffix(tool, "__"+engine.PermissionHostTool)
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// canonicalInput is a tool input in one spelling, so the stream's input, a
// host call's and a hook's compare equal; absent input is the empty object.
func canonicalInput(raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return agent.CanonicalJSON(raw)
}

// askTheRoot is the route's decision: the ask parked in the root's approval
// queue over the run's reach-back link, until the human answers or the
// coordinator's approval timeout denies it. The wait is bounded past that
// timeout, which guards a dead coordinator, not the human.
func askTheRoot(home engineHome, spec approvalSpec, bound time.Duration) func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
	return func(ctx context.Context, ask engine.PermissionAsk) (engine.PermissionAnswer, error) {
		ctx, cancel := context.WithTimeout(ctx, bound)
		defer cancel()
		req := coordgrpc.ApprovalRequestToWire(coord.ApprovalRequest{Ask: ask, Transitions: spec.transitions, Timeout: spec.timeout})
		resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Approval{Approval: req}})
		if err != nil {
			return engine.PermissionAnswer{}, fmt.Errorf("%w: %v", errNoDecision, err)
		}
		if st := resp.GetStatus(); st.GetCode() != int32(codes.OK) {
			return engine.PermissionAnswer{}, fmt.Errorf("%w: %s", errNoDecision, st.GetMessage())
		}
		if resp.GetApproval() == nil {
			return engine.PermissionAnswer{}, errNoDecision
		}
		d, err := coordgrpc.ApprovalDecisionFromWire(resp.GetApproval())
		if err != nil {
			return engine.PermissionAnswer{}, fmt.Errorf("%w: %v", errNoDecision, err)
		}
		return engine.PermissionAnswer{Allow: d.Allow, SessionRules: d.SessionRules, SetMode: d.SetMode, Answers: d.Answers, Message: d.Message}, nil
	}
}

// SetApprovalHost binds the hosted run's approval route, which the session's
// endpoint then serves. One per Home, like the turn sink: a second binding is
// a wiring bug and is refused.
func (h *Home) SetApprovalHost(ah ApprovalHost) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.approvalHost != nil {
		h.rep.Warnf("runner: an approval route is already bound for this run; the second binding is refused")
		return
	}
	h.approvalHost = ah
}

// ApprovalHost is the bound approval route; nil when the run's approver is
// not the human (or nothing is hosted yet), in which case the endpoint
// decides nothing.
func (h *Home) ApprovalHost() ApprovalHost {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.approvalHost
}

// mcpStatusConnected is the status an engine reports for an MCP server it
// connected at session start (agent.MCPStatus).
const mcpStatusConnected = "connected"

// checkApprovalHost is the capabilities gate on a turn's session start: the
// route anchors every ask on the permission host, which the session's own
// endpoint serves, so an engine that did not connect that endpoint can hold
// nothing open — every ask is denied this turn. An engine that reports no
// server statuses is not judged.
func (eh *EngineHost) checkApprovalHost(s *agent.ChatSessionInfo) {
	if len(s.MCPServers) == 0 {
		return
	}
	for _, m := range s.MCPServers {
		if m.Name == wire.CtxloomServerName && m.Status == mcpStatusConnected {
			return
		}
	}
	eh.rep.Warnf("approval host not connected: the engine did not connect ctxloom's %q server this turn, so nothing can hold a permission request open — every request the human would decide is denied, and questions and plans are unavailable (check the session's MCP endpoint: ctxloom doctor)", wire.CtxloomServerName)
}
