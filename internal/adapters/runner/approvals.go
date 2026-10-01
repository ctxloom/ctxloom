package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// ApprovalRoute is the approval route as the session's endpoint serves it:
// the approval hook's POST, the engine's native payload in and its native
// decision out.
type ApprovalRoute interface {
	Hook(ctx context.Context, event string, payload []byte) ([]byte, error)
}

// The refusals the route answers without asking anyone. Each one reaches the
// engine as a deny whose message is the refusal.
var (
	// errUncorrelated: the ask matches no call the engine's own stream
	// announced this turn and has not yet closed, by exact tool name and
	// canonical input. It is what a forged POST gets: the route never puts a
	// request in front of the human that the engine did not make.
	errUncorrelated = errors.New("ctxloom: the permission request matches no open tool call of this turn")
	// errTurnEnded: the turn's engine process ended with the request open.
	errTurnEnded = errors.New("ctxloom: the turn ended before the request was decided")
	// errNoDecision: the coordinator gave no decision for the request.
	errNoDecision = errors.New("ctxloom: the coordinator gave no decision")
)

const (
	// approvalAnchorWait is how long an ask waits for the engine's stream
	// to announce the call it matches: the engine runs the hook as soon as
	// it emits the tool_use, so the POST can reach the runner before the
	// frame does.
	approvalAnchorWait = 5 * time.Second
	// approvalRequestSlack bounds the runner's wait on the coordinator past
	// the approval timeout the coordinator enforces itself: it guards a dead
	// coordinator, not the human.
	approvalRequestSlack = 30 * time.Second
)

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

// approvals is the route's runner half for one run: a LEDGER of the current
// turn's tool calls, fed from the engine's own stream, and the one decision
// each call's asks share. An ask is put to the root only when it matches an
// open call of the ledger; everything resets when the turn ends.
type approvals struct {
	codec  engine.ApprovalCodec
	decide func(ctx context.Context, ask engine.PermissionAsk) (engine.PermissionAnswer, error)
	after  afterFunc
	// armed, when set, is told each time a bounded wait has armed its
	// timer — the point after which advancing the clock expires it. Tests
	// only.
	armed func()

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
	turn    *approvalTurn
}

// approvalTurn is one engine process's ledger. Its context ends with the
// turn, abandoning every decision still being asked for.
type approvalTurn struct {
	ctx   context.Context
	end   context.CancelFunc
	calls map[string]*toolCall
	order []*toolCall // as announced, oldest first
}

func newApprovalTurn() *approvalTurn {
	ctx, end := context.WithCancel(context.Background())
	return &approvalTurn{ctx: ctx, end: end, calls: map[string]*toolCall{}}
}

// toolCall is one tool call the engine announced this turn.
type toolCall struct {
	id, tool string
	input    []byte // canonicalInput of the call's input
	resulted bool
	decision *decision // the one decision its asks share; nil until asked
}

// decision is the one decision every ask bound to a call waits on.
type decision struct {
	done chan struct{}
	ans  engine.PermissionAnswer
	err  error
}

func newApprovals(spec approvalSpec, decide func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error)) *approvals {
	return &approvals{
		codec: spec.codec, decide: decide, after: realAfter,
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
			return // an input the ledger cannot canonicalise matches no ask
		}
		a.mu.Lock()
		a.turn.announceLocked(e.ToolCallID, e.ToolName, input)
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

// announceLocked records a call; a call announced again keeps its place and
// decision and takes the later tool and input. Call with a.mu held.
func (t *approvalTurn) announceLocked(id, tool string, input []byte) {
	if c := t.calls[id]; c != nil {
		c.tool, c.input = tool, input
		return
	}
	c := &toolCall{id: id, tool: tool, input: input}
	t.calls[id] = c
	t.order = append(t.order, c)
}

// endTurn ends the turn's ledger: every decision still being asked for is
// abandoned (the engine process that asked is gone) and the next turn's
// ledger starts empty.
func (a *approvals) endTurn() {
	a.mu.Lock()
	a.turn.end()
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

// Hook serves the approval hook: the ask is matched against the ledger,
// decided once per call by the root (every ask bound to the call waits on
// the same decision), and written back as the engine's native answer. A
// payload the codec cannot read is an error — no decision at all.
func (a *approvals) Hook(ctx context.Context, event string, payload []byte) ([]byte, error) {
	ask, err := a.codec.DecodeAsk(event, payload)
	if err != nil {
		return nil, err
	}
	turn, d, err := a.bind(ctx, ask)
	if err != nil {
		return a.codec.EncodeAnswer(event, ask, denial(err))
	}
	select {
	case <-d.done:
	case <-turn.ctx.Done():
		return a.codec.EncodeAnswer(event, ask, denial(errTurnEnded))
	case <-ctx.Done():
		// The hook that asked is gone: nobody reads an answer.
		return nil, ctx.Err()
	}
	if d.err != nil {
		return a.codec.EncodeAnswer(event, ask, denial(d.err))
	}
	return a.codec.EncodeAnswer(event, ask, d.ans)
}

// bind matches an ask to an open call of the ledger, waiting (bounded) for
// the stream to announce one, and returns the decision it waits on. The
// first ask bound to a call has it decided — on the turn's context, so the
// decision outlives the POST that started it.
func (a *approvals) bind(ctx context.Context, ask engine.PermissionAsk) (*approvalTurn, *decision, error) {
	input, err := canonicalInput(ask.Input)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errUncorrelated, err)
	}
	var (
		turn  *approvalTurn
		call  *toolCall
		fresh bool
	)
	if !a.await(ctx, approvalAnchorWait, func() bool {
		turn = a.turn
		call, fresh = turn.matchLocked(ask.Tool, input)
		return call != nil
	}) {
		return nil, nil, errUncorrelated
	}
	if fresh {
		ask.ToolUseID = call.id
		go a.settle(turn, call.decision, ask)
	}
	return turn, call.decision, nil
}

// matchLocked binds an ask for tool and input to the OLDEST open call
// carrying exactly them that no ask has bound yet — so identical parallel
// calls are taken in order — else to the oldest bound one, whose decision a
// repeated ask joins. fresh reports a new binding. Call with a.mu held.
func (t *approvalTurn) matchLocked(tool string, input []byte) (call *toolCall, fresh bool) {
	var joined *toolCall
	for _, c := range t.order {
		if c.resulted || c.tool != tool || !bytes.Equal(c.input, input) {
			continue
		}
		if c.decision == nil {
			c.decision = &decision{done: make(chan struct{})}
			return c, true
		}
		if joined == nil {
			joined = c
		}
	}
	return joined, false
}

// settle has the root decide the ask on the turn's context and releases
// every ask waiting on d.
func (a *approvals) settle(turn *approvalTurn, d *decision, ask engine.PermissionAsk) {
	ans, err := a.decide(turn.ctx, ask)
	if turn.ctx.Err() != nil {
		err = errTurnEnded
	}
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

// canonicalInput is a tool input in one spelling, so the stream's input and
// a hook's compare equal; absent input is the empty object.
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

// SetApprovalRoute binds the hosted run's approval route, which the session's
// endpoint then serves. One per Home, like the turn sink: a second binding is
// a wiring bug and is refused.
func (h *Home) SetApprovalRoute(ar ApprovalRoute) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.approvalRoute != nil {
		h.rep.Warnf("runner: an approval route is already bound for this run; the second binding is refused")
		return
	}
	h.approvalRoute = ar
}

// ApprovalRoute is the bound approval route; nil when the run's approver is
// not the human (or nothing is hosted yet), in which case the endpoint
// decides nothing.
func (h *Home) ApprovalRoute() ApprovalRoute {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.approvalRoute
}

