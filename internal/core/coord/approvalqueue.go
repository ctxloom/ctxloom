package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// ApprovalID names one parked request. The queue mints it; a presenter only
// ever answers an id it was handed.
type ApprovalID string

// ApprovalKind classifies a parked request.
type ApprovalKind int

const (
	ApprovalTool ApprovalKind = iota
	ApprovalQuestion
	ApprovalPlan
)

var approvalKindNames = [...]string{ApprovalTool: "tool", ApprovalQuestion: "question", ApprovalPlan: "plan"}

// String renders the kind's journal spelling.
func (k ApprovalKind) String() string {
	if k >= 0 && int(k) < len(approvalKindNames) {
		return approvalKindNames[k]
	}
	return fmt.Sprintf("approvalKind(%d)", int(k))
}

// MarshalText writes the kind by name, so the journal stays jq-legible.
func (k ApprovalKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// ApprovalKindOf is the kind a parked request takes from what the engine
// asked.
func ApprovalKindOf(k engine.AskKind) ApprovalKind {
	switch k {
	case engine.AskQuestion:
		return ApprovalQuestion
	case engine.AskPlan:
		return ApprovalPlan
	default:
		return ApprovalTool
	}
}

// PendingApproval is one request parked for the root human's decision, as a
// presenter shows it.
type PendingApproval struct {
	ID    ApprovalID
	Kind  ApprovalKind
	From  Identity
	Agent string
	// Lineage is the asker's delegation chain, root → … → the asking harp.
	Lineage []string
	// WorkDir is for display only.
	WorkDir string
	Ask     engine.PermissionAsk
	// Transitions are the postures an allow may move the asker to, as its
	// engine offers them (PermissionModel.Transitions), exactly one the
	// default; none offers no posture change.
	Transitions     []engine.PostureTransition
	Since, Deadline time.Time
	// turn is the asking run's turn when the coordinator received the
	// request (turnOf); a request whose turn has since ended never parks.
	turn uint64
}

// ApprovalDecision resolves one parked request. Presenters fill everything
// but Decider, which the queue sets: whoever resolved the request is a fact
// the queue knows, not a claim a presenter makes.
type ApprovalDecision struct {
	Allow        bool
	SessionRules []string
	// SetMode is a posture change riding the allow (plan posture, or the
	// engine's own set-mode suggestion); one of the request's Transitions.
	SetMode engine.Declared[string]
	Answers []engine.QuestionAnswer
	Message string
	Decider agent.Decider
}

// Grant is one allow-for-session rule a harp holds.
type Grant struct {
	ID   string
	Harp string
	Rule string
	From ApprovalID
	At   time.Time
}

// QueueEventKind says what changed in the queue.
type QueueEventKind int

const (
	QueueAdded QueueEventKind = iota
	QueueResolved
	QueueGrantsChanged
)

// QueueEvent tells a subscriber the queue changed. Pending is the count of
// requests still parked after the change.
type QueueEvent struct {
	Kind    QueueEventKind
	ID      ApprovalID
	Decider agent.Decider
	Pending int
}

var (
	ErrApprovalResolved = errors.New("coord: approval already resolved")
	ErrNoSuchApproval   = errors.New("coord: no such approval")
	ErrNoSuchGrant      = errors.New("coord: no such grant")
)

// ApprovalRequest is a run asking the root human to decide: the plane-2
// request the coordinator parks in its queue and answers with the
// ApprovalDecision. Timeout is the asker's declared hold; the queue bounds it.
type ApprovalRequest struct {
	Ask         engine.PermissionAsk
	Transitions []engine.PostureTransition
	Timeout     time.Duration
	// turn is set by the coordinator as the request arrives; never on the wire.
	turn uint64
}

// Clock is the queue's command time.
type Clock func() time.Time

// ApprovalSource is what a presenter consumes; *ApprovalQueue is the one
// implementation. Answer is reachable only through this in-process seam —
// no wire request, tool or consumer RPC reaches it — which is what makes the
// root human the only approver.
type ApprovalSource interface {
	Pending() []PendingApproval
	Subscribe(ctx context.Context) <-chan QueueEvent
	Answer(id ApprovalID, d ApprovalDecision) error
	Grants(harp string) []Grant
	Revoke(harp, grantID string) error
}

// ApprovalPresenter shows the source's requests to the human and answers
// them; it runs until ctx ends.
type ApprovalPresenter interface {
	Present(ctx context.Context, src ApprovalSource) error
}

// ApprovalQueue parks runs' requests for the root human. One per coordinator,
// so requests from every depth of the run tree land in the root's queue.
type ApprovalQueue struct {
	store      *Store
	grants     *grantsFold
	now        Clock
	pushGrants func(harp string, rules []string) error
	// runGone reports that an asker's run can take no decision: it has
	// ended, or its harp has moved on to a newer run. Read inside Park's
	// insert window, after the run's end is journaled and before its
	// withdrawal, so a request never outlives its run.
	runGone func(Identity) bool

	mu       sync.Mutex
	pending  map[ApprovalID]*parkedApproval
	resolved map[ApprovalID]struct{}
	subs     map[chan QueueEvent]struct{}
	// turns counts each live run's ended turns: a request stamped with an
	// older count was asked by a turn that is already over.
	turns map[string]uint64

	// grantsLock is held by a revoke from reading the set it pushes until
	// the revoke is journaled, and by a decision that grants while it is
	// journaled and delivered: the run never takes a grant from a decision
	// only to have an older pushed set drop it. A one-slot channel rather
	// than a mutex, so a test can see a waiter durably blocked (synctest).
	grantsLock chan struct{}

	// expireHook, when set (tests), runs after a park's timer or context has
	// fired and before it claims the request — where an answer can still win.
	expireHook func(ApprovalID)
}

type parkedApproval struct {
	req PendingApproval
	// answer carries the one decision; buffered so whoever settles the
	// request never waits on the asker.
	answer chan ApprovalDecision
}

// NewApprovalQueue binds a queue to the journal holding its grants fold.
// pushGrants hands a harp's full remaining grant set to its live run when a
// grant is revoked; runGone says an asker's run can no longer be answered.
func NewApprovalQueue(store *Store, clock Clock, pushGrants func(harp string, rules []string) error, runGone func(Identity) bool) *ApprovalQueue {
	return &ApprovalQueue{
		store:      store,
		grants:     storeFold[*grantsFold](store),
		now:        clock,
		pushGrants: pushGrants,
		runGone:    runGone,
		pending:    make(map[ApprovalID]*parkedApproval),
		resolved:   make(map[ApprovalID]struct{}),
		subs:       make(map[chan QueueEvent]struct{}),
		turns:      make(map[string]uint64),
		grantsLock: make(chan struct{}, 1),
	}
}

func (q *ApprovalQueue) lockGrants()   { q.grantsLock <- struct{}{} }
func (q *ApprovalQueue) unlockGrants() { <-q.grantsLock }

// storeFold finds the fold of type F a store was opened with. A store opened
// without it is a composition bug, and panics at construction rather than
// losing every fact the fold would have kept.
func storeFold[F fold](s *Store) F {
	for _, f := range s.folds {
		if typed, ok := f.(F); ok {
			return typed
		}
	}
	panic(fmt.Sprintf("coord: store %s carries no %T fold", s.path, *new(F)))
}

// approvalTimeout bounds a requested timeout: unset is the default, and no
// request holds longer than the cap.
func approvalTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return engine.DefaultApprovalTimeout
	}
	return min(d, engine.MaxApprovalTimeout)
}

// Park holds from's request until the human answers it, its timeout elapses
// (deny), or ctx ends (deny). The decision always comes back; a request the
// journal cannot record is denied at once.
func (q *ApprovalQueue) Park(ctx context.Context, from Identity, req PendingApproval, timeout time.Duration) ApprovalDecision {
	timeout = approvalTimeout(timeout)
	now := q.now()
	req.ID = ApprovalID(RandID("apv-", 12))
	req.From = from
	req.Since = now
	req.Deadline = now.Add(timeout)
	if err := q.store.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factApprovalParked, now, approvalParked{
			ID: req.ID, Harp: from.Harp, RunID: from.RunID, Agent: req.Agent, Kind: req.Kind,
			Tool: req.Ask.Tool, ToolUseID: req.Ask.ToolUseID, Input: req.Ask.Input, Deadline: req.Deadline,
		})}, nil
	}); err != nil {
		return ApprovalDecision{Decider: agent.DeciderRefused, Message: fmt.Sprintf("the request could not be recorded: %v", err)}
	}
	p := &parkedApproval{req: req, answer: make(chan ApprovalDecision, 1)}
	q.mu.Lock()
	if gone := q.runGone(from); gone || q.turns[from.RunID] != req.turn {
		// The turn or the run ended between the request's arrival and this
		// park: it was dropped with them, only later than the ones already
		// parked.
		q.resolved[req.ID] = struct{}{}
		q.mu.Unlock()
		d := droppedWithTurn
		if gone {
			d = droppedWithRun
		}
		_ = q.settle(p, d)
		return <-p.answer
	}
	q.pending[req.ID] = p
	n := len(q.pending)
	q.mu.Unlock()
	q.publish(QueueEvent{Kind: QueueAdded, ID: req.ID, Pending: n})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var d ApprovalDecision
	select {
	case d = <-p.answer:
		return d
	case <-timer.C:
		d = ApprovalDecision{Decider: agent.DeciderTimeout, Message: fmt.Sprintf("no decision within %s", timeout)}
	case <-ctx.Done():
		d = ApprovalDecision{Decider: agent.DeciderCancelled, Message: "the request was withdrawn"}
	}
	if q.expireHook != nil {
		q.expireHook(req.ID)
	}
	if q.claim(req.ID) {
		// A decision the journal refused reaches the asker as a refused
		// deny; there is nobody else to tell.
		_ = q.settle(p, d)
	}
	// Claimed or not, exactly one settle delivers on answer.
	return <-p.answer
}

// Answer resolves a parked request with the human's decision. It succeeds
// once per id: a request already resolved — answered, expired or withdrawn —
// is ErrApprovalResolved, and an id the queue never issued is
// ErrNoSuchApproval.
func (q *ApprovalQueue) Answer(id ApprovalID, d ApprovalDecision) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	q.mu.Unlock()
	if !ok || !q.claim(id) {
		return q.unanswerable(id)
	}
	d.Decider = agent.DeciderHuman
	return q.settle(p, d)
}

func (q *ApprovalQueue) unanswerable(id ApprovalID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, done := q.resolved[id]; done {
		return fmt.Errorf("%w: %s", ErrApprovalResolved, id)
	}
	return fmt.Errorf("%w: %s", ErrNoSuchApproval, id)
}

// cancelFrom withdraws every request harp has parked: its run, runID, has
// ended, and so has any count of its turns. A request of that run still on
// its way to Park is dropped there (runGone).
func (q *ApprovalQueue) cancelFrom(harp, runID string) {
	q.mu.Lock()
	delete(q.turns, runID)
	q.mu.Unlock()
	q.withdraw(func(from Identity) bool { return from.Harp == harp }, droppedWithRun)
}

// droppedWithTurn and droppedWithRun are the decisions on a request whose
// turn or run ended first.
var (
	droppedWithTurn = ApprovalDecision{Decider: agent.DeciderCancelled, Message: "the asking turn ended"}
	droppedWithRun  = ApprovalDecision{Decider: agent.DeciderCancelled, Message: "the asking run ended"}
)

// turnOf is runID's current turn, to stamp on a request as it arrives —
// in arrival order with the run's turn-end events, which endTurn counts.
func (q *ApprovalQueue) turnOf(runID string) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.turns[runID]
}

// endTurn drops what runID's ending turn asked: an interrupted or finished
// turn cannot take an answer, and asks are never carried into the next one.
// A request of that turn still on its way to Park is dropped there.
func (q *ApprovalQueue) endTurn(runID string) {
	q.mu.Lock()
	q.turns[runID]++
	q.mu.Unlock()
	q.withdraw(func(from Identity) bool { return from.RunID == runID }, droppedWithTurn)
}

// withdraw settles every parked request whose asker matches with d.
func (q *ApprovalQueue) withdraw(match func(Identity) bool, d ApprovalDecision) {
	q.mu.Lock()
	var mine []*parkedApproval
	for id, p := range q.pending {
		if match(p.req.From) {
			delete(q.pending, id)
			q.resolved[id] = struct{}{}
			mine = append(mine, p)
		}
	}
	q.mu.Unlock()
	for _, p := range mine {
		_ = q.settle(p, d)
	}
}

// claim takes id out of the pending set; only the one caller that does may
// settle it.
func (q *ApprovalQueue) claim(id ApprovalID) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[id]; !ok {
		return false
	}
	delete(q.pending, id)
	q.resolved[id] = struct{}{}
	return true
}

// settle journals a claimed request's decision and delivers it to the asker.
// Only an allow grants anything. A decision the journal cannot record is
// delivered as a deny: nothing is allowed off the record.
func (q *ApprovalQueue) settle(p *parkedApproval, d ApprovalDecision) error {
	if !d.Allow {
		d.SessionRules, d.SetMode = nil, engine.Declared[string]{}
	}
	if len(d.SessionRules) > 0 {
		q.lockGrants()
		defer q.unlockGrants()
	}
	var granted bool
	err := q.store.Exec(func() ([]Fact, error) {
		facts := q.decisionFacts(p.req, d)
		granted = len(facts) > 1 && facts[1].Kind == factGrantAdded
		return facts, nil
	})
	if err != nil {
		d = ApprovalDecision{Decider: agent.DeciderRefused, Message: fmt.Sprintf("the decision could not be recorded: %v", err)}
	}
	p.answer <- d
	q.mu.Lock()
	n := len(q.pending)
	q.mu.Unlock()
	q.publish(QueueEvent{Kind: QueueResolved, ID: p.req.ID, Decider: d.Decider, Pending: n})
	if granted && err == nil {
		q.publish(QueueEvent{Kind: QueueGrantsChanged, ID: p.req.ID, Pending: n})
	}
	return err
}

// decisionFacts is the facts one decision journals: the decision, then a
// grant per new session rule, then the plan's approval. Runs inside the
// journal's writer window, so the grants fold it reads is quiescent.
func (q *ApprovalQueue) decisionFacts(req PendingApproval, d ApprovalDecision) []Fact {
	now, harp := q.now(), req.From.Harp
	setMode, _ := d.SetMode.Get()
	facts := []Fact{factAt(factApprovalDecided, now, approvalDecided{
		ID: req.ID, Harp: harp, Decider: d.Decider, Allow: d.Allow, Rules: d.SessionRules,
		SetMode: setMode, Answers: d.Answers, Message: d.Message,
	})}
	for i, rule := range d.SessionRules {
		if q.grants.holds(harp, rule) {
			continue
		}
		facts = append(facts, factAt(factGrantAdded, now, grantAdded{
			ID: fmt.Sprintf("%s-%d", req.ID, i), Harp: harp, Rule: rule, From: req.ID,
		}))
	}
	if d.Allow && req.Kind == ApprovalPlan && req.Ask.Plan != nil {
		sum := sha256.Sum256([]byte(req.Ask.Plan.Markdown))
		facts = append(facts, factAt(factPlanApproved, now, planApproved{
			ID: req.ID, Harp: harp, Posture: setMode, Digest: "sha256:" + hex.EncodeToString(sum[:]), Path: req.Ask.Plan.Path,
		}))
	}
	return facts
}

// Pending lists the parked requests, soonest deadline first.
func (q *ApprovalQueue) Pending() []PendingApproval {
	q.mu.Lock()
	out := make([]PendingApproval, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p.req)
	}
	q.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Deadline.Equal(out[j].Deadline) {
			return out[i].Deadline.Before(out[j].Deadline)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// subscriberBuffer bounds each subscriber's backlog.
const subscriberBuffer = 64

// Subscribe streams queue changes until ctx ends, then closes the channel. A
// subscriber that falls behind loses events rather than stalling the queue;
// that is safe because every event means "re-read Pending", and a full
// backlog already says so.
func (q *ApprovalQueue) Subscribe(ctx context.Context) <-chan QueueEvent {
	ch := make(chan QueueEvent, subscriberBuffer)
	q.mu.Lock()
	q.subs[ch] = struct{}{}
	q.mu.Unlock()
	context.AfterFunc(ctx, func() {
		q.mu.Lock()
		delete(q.subs, ch)
		close(ch)
		q.mu.Unlock()
	})
	return ch
}

func (q *ApprovalQueue) publish(ev QueueEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for ch := range q.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Grants lists the session grants harp holds, oldest first.
func (q *ApprovalQueue) Grants(harp string) []Grant {
	var out []Grant
	q.store.View(func() { out = slices.Clone(q.grants.byHarp[harp]) })
	return out
}

// Revoke withdraws one grant. The run is told first — it is handed the rules
// that remain — and the revoke is journaled only once it has taken them, so
// the record never says a rule is gone while the run still applies it.
func (q *ApprovalQueue) Revoke(harp, grantID string) error {
	q.lockGrants()
	defer q.unlockGrants()
	var (
		found     bool
		remaining []string
	)
	q.store.View(func() {
		for _, g := range q.grants.byHarp[harp] {
			if g.ID == grantID {
				found = true
				continue
			}
			remaining = append(remaining, g.Rule)
		}
	})
	if !found {
		return fmt.Errorf("%w: %s on %s", ErrNoSuchGrant, grantID, harp)
	}
	if err := q.pushGrants(harp, remaining); err != nil {
		return fmt.Errorf("revoke %s on %s: %w", grantID, harp, err)
	}
	if err := q.store.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factGrantRevoked, q.now(), grantRevoked{ID: grantID, Harp: harp})}, nil
	}); err != nil {
		return fmt.Errorf("revoke %s on %s: %w", grantID, harp, err)
	}
	q.mu.Lock()
	n := len(q.pending)
	q.mu.Unlock()
	q.publish(QueueEvent{Kind: QueueGrantsChanged, Pending: n})
	return nil
}

// grantsFold is every harp's session grants, folded from the grant facts.
type grantsFold struct {
	byHarp map[string][]Grant
}

func newGrantsFold() *grantsFold { return &grantsFold{byHarp: make(map[string][]Grant)} }

func (f *grantsFold) apply(fact Fact) {
	switch fact.Kind {
	case factGrantAdded:
		applyDecoded(fact, func(p grantAdded, at time.Time) {
			// Wall time in UTC only: a live fact carries the clock's
			// monotonic reading and local zone, a replayed one carries
			// neither, and a restarted coordinator must hold the grants it
			// held before.
			f.byHarp[p.Harp] = append(f.byHarp[p.Harp], Grant{ID: p.ID, Harp: p.Harp, Rule: p.Rule, From: p.From, At: at.Round(0).UTC()})
		})
	case factGrantRevoked:
		applyDecoded(fact, func(p grantRevoked, _ time.Time) {
			f.byHarp[p.Harp] = slices.DeleteFunc(f.byHarp[p.Harp], func(g Grant) bool { return g.ID == p.ID })
		})
	}
}

// holds reports whether harp already holds rule.
func (f *grantsFold) holds(harp, rule string) bool {
	return slices.ContainsFunc(f.byHarp[harp], func(g Grant) bool { return g.Rule == rule })
}
