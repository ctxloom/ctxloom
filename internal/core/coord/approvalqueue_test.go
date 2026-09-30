package coord

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// grantPush records every pushGrants call the queue makes.
type grantPush struct {
	mu    sync.Mutex
	calls []pushedGrants
	err   error
}

type pushedGrants struct {
	harp  string
	rules []string
}

func (g *grantPush) push(harp string, rules []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, pushedGrants{harp: harp, rules: append([]string(nil), rules...)})
	return g.err
}

// openQueueStore opens a journal at path carrying the grants fold the queue
// reads — the shape the coordinator opens runs.jsonl in.
func openQueueStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := openStore(path, newGrantsFold())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestQueue(t *testing.T) (*ApprovalQueue, *grantPush, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	push := &grantPush{}
	return NewApprovalQueue(openQueueStore(t, path), time.Now, push.push), push, path
}

var askerID = Identity{Harp: "child-harp", RunID: "run-1", Depth: 1}

func toolAsk(tool string) PendingApproval {
	return PendingApproval{
		Kind:  ApprovalTool,
		Agent: "worker",
		Ask:   engine.PermissionAsk{Kind: engine.AskTool, Tool: tool, Input: json.RawMessage(`{"command":"ls"}`), ToolUseID: "toolu_1"},
	}
}

// parkAsync parks req on q from a goroutine and returns the decision channel.
// The caller learns the park landed from a subscription's QueueAdded event —
// a synchronised signal, never a poll.
func parkAsync(ctx context.Context, q *ApprovalQueue, req PendingApproval, timeout time.Duration) <-chan ApprovalDecision {
	out := make(chan ApprovalDecision, 1)
	go func() { out <- q.Park(ctx, askerID, req, timeout) }()
	return out
}

func awaitEvent(t *testing.T, ch <-chan QueueEvent, kind QueueEventKind) QueueEvent {
	t.Helper()
	for {
		select {
		case ev, ok := <-ch:
			require.True(t, ok, "subscription closed while waiting for event %d", kind)
			if ev.Kind == kind {
				return ev
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no queue event %d within 10s", kind)
		}
	}
}

func readFacts(t *testing.T, path string) []Fact {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out []Fact
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var fact Fact
		require.NoError(t, json.Unmarshal(sc.Bytes(), &fact))
		out = append(out, fact)
	}
	require.NoError(t, sc.Err())
	return out
}

func factKinds(facts []Fact) []string {
	kinds := make([]string, 0, len(facts))
	for _, f := range facts {
		kinds = append(kinds, f.Kind)
	}
	return kinds
}

// TestApprovalQueue_AnswerResolvesTheParkOnce: the human's answer reaches the
// parked request with the human as decider, and an id answers exactly once —
// a late or duplicate answer can never resolve anything (R4 correctness).
func TestApprovalQueue_AnswerResolvesTheParkOnce(t *testing.T) {
	q, _, path := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)

	got := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
	added := awaitEvent(t, events, QueueAdded)
	assert.Equal(t, 1, added.Pending)

	pending := q.Pending()
	require.Len(t, pending, 1)
	p := pending[0]
	assert.Equal(t, added.ID, p.ID, "the event names the id the presenter answers by")
	assert.Equal(t, askerID, p.From, "the queue stamps the asker's identity")
	assert.Equal(t, time.Minute, p.Deadline.Sub(p.Since))

	require.NoError(t, q.Answer(p.ID, ApprovalDecision{Allow: true, Decider: agent.DeciderTimeout}))
	d := <-got
	assert.True(t, d.Allow)
	assert.Equal(t, agent.DeciderHuman, d.Decider, "the queue sets the decider; a presenter cannot claim another")

	resolved := awaitEvent(t, events, QueueResolved)
	assert.Equal(t, p.ID, resolved.ID)
	assert.Equal(t, agent.DeciderHuman, resolved.Decider)
	assert.Zero(t, resolved.Pending)

	assert.ErrorIs(t, q.Answer(p.ID, ApprovalDecision{Allow: false}), ErrApprovalResolved)
	assert.ErrorIs(t, q.Answer("apv-never-issued", ApprovalDecision{}), ErrNoSuchApproval)
	assert.Empty(t, q.Pending())

	facts := readFacts(t, path)
	assert.Equal(t, []string{factApprovalParked, factApprovalDecided}, factKinds(facts))
	var decided map[string]any
	require.NoError(t, json.Unmarshal(facts[1].Data, &decided))
	assert.Equal(t, "human", decided["decider"])
	assert.Equal(t, true, decided["allow"])
	assert.Equal(t, string(p.ID), decided["id"])
}

// TestApprovalQueue_TimeoutDenies: nobody answers, so the request is denied at
// its deadline with the timeout as decider — no presence tracking, every
// request waits out its time (R3).
func TestApprovalQueue_TimeoutDenies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, path := newTestQueue(t)
		start := time.Now()
		d := q.Park(context.Background(), askerID, toolAsk("Bash"), 3*time.Minute)
		assert.False(t, d.Allow)
		assert.Equal(t, agent.DeciderTimeout, d.Decider)
		assert.Equal(t, 3*time.Minute, time.Since(start), "denied AT the deadline, not before")
		assert.Empty(t, q.Pending())
		assert.Equal(t, []string{factApprovalParked, factApprovalDecided}, factKinds(readFacts(t, path)))
	})
}

// TestApprovalQueue_TimeoutBounds: an unset timeout is the ruled 15-minute
// default; a longer one is capped at the ruled 60 minutes.
func TestApprovalQueue_TimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset-is-the-default", 0, DefaultApprovalTimeout},
		{"negative-is-the-default", -time.Second, DefaultApprovalTimeout},
		{"within-the-cap-is-kept", 20 * time.Minute, 20 * time.Minute},
		{"at-the-cap-is-kept", MaxApprovalTimeout, MaxApprovalTimeout},
		{"over-the-cap-is-capped", 2 * time.Hour, MaxApprovalTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, _, _ := newTestQueue(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := parkAsync(ctx, q, toolAsk("Bash"), tc.in)
				synctest.Wait()
				pending := q.Pending()
				require.Len(t, pending, 1)
				assert.Equal(t, tc.want, pending[0].Deadline.Sub(pending[0].Since))
				cancel()
				<-done
			})
		})
	}
	assert.Equal(t, 15*time.Minute, DefaultApprovalTimeout)
	assert.Equal(t, 60*time.Minute, MaxApprovalTimeout)
}

// TestApprovalQueue_CancelDenies: the asker's context ending (its run gone)
// denies with the cancellation as decider.
func TestApprovalQueue_CancelDenies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, _ := newTestQueue(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
		synctest.Wait()
		require.Len(t, q.Pending(), 1)
		cancel()
		d := <-done
		assert.False(t, d.Allow)
		assert.Equal(t, agent.DeciderCancelled, d.Decider)
		assert.Empty(t, q.Pending())
	})
}

// TestApprovalQueue_CancelFromEndsOnlyThatHarp: a run's terminal withdraws the
// requests that run parked, and nobody else's.
func TestApprovalQueue_CancelFromEndsOnlyThatHarp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, _ := newTestQueue(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mine := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
		other := make(chan ApprovalDecision, 1)
		go func() {
			other <- q.Park(ctx, Identity{Harp: "sibling", RunID: "run-2", Depth: 1}, toolAsk("Edit"), time.Minute)
		}()
		synctest.Wait()
		require.Len(t, q.Pending(), 2)

		q.cancelFrom(askerID.Harp)
		d := <-mine
		assert.False(t, d.Allow)
		assert.Equal(t, agent.DeciderCancelled, d.Decider)
		pending := q.Pending()
		require.Len(t, pending, 1)
		assert.Equal(t, "sibling", pending[0].From.Harp)
		cancel()
		<-other
	})
}

// TestApprovalQueue_AnswerRacesTheDeadline forces the answer and the timeout
// onto the same instant: exactly one of them decides, and Answer's return says
// which — nil means the human's decision reached the asker, ErrApprovalResolved
// means the timeout's did.
func TestApprovalQueue_AnswerRacesTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, path := newTestQueue(t)
		done := parkAsync(context.Background(), q, toolAsk("Bash"), time.Minute)
		synctest.Wait()
		id := q.Pending()[0].ID
		time.Sleep(time.Minute) // the deadline instant: the timer is due now
		err := q.Answer(id, ApprovalDecision{Allow: true})
		d := <-done
		if err == nil {
			assert.Equal(t, agent.DeciderHuman, d.Decider)
			assert.True(t, d.Allow)
		} else {
			require.ErrorIs(t, err, ErrApprovalResolved)
			assert.Equal(t, agent.DeciderTimeout, d.Decider)
			assert.False(t, d.Allow)
		}
		assert.Equal(t, []string{factApprovalParked, factApprovalDecided}, factKinds(readFacts(t, path)),
			"one decision journaled, whichever won")
	})
}

// TestApprovalQueue_PendingInDeadlineOrder: the soonest deadline first, so a
// presenter shows the request closest to its automatic deny.
func TestApprovalQueue_PendingInDeadlineOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, _ := newTestQueue(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var dones []<-chan ApprovalDecision
		for _, tc := range []struct {
			tool    string
			timeout time.Duration
		}{{"Late", 30 * time.Minute}, {"Soon", 2 * time.Minute}, {"Mid", 10 * time.Minute}} {
			dones = append(dones, parkAsync(ctx, q, toolAsk(tc.tool), tc.timeout))
			synctest.Wait()
		}
		var order []string
		for _, p := range q.Pending() {
			order = append(order, p.Ask.Tool)
		}
		assert.Equal(t, []string{"Soon", "Mid", "Late"}, order)
		cancel()
		for _, d := range dones {
			<-d
		}
	})
}

// TestApprovalQueue_SubscriptionClosesWithItsContext: a presenter's
// subscription ends when its context does.
func TestApprovalQueue_SubscriptionClosesWithItsContext(t *testing.T) {
	q, _, _ := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	events := q.Subscribe(ctx)
	cancel()
	select {
	case _, ok := <-events:
		assert.False(t, ok, "the channel closes, carrying nothing")
	case <-time.After(10 * time.Second):
		t.Fatal("subscription did not close with its context")
	}
}

// TestApprovalQueue_SessionGrantsFoldAndRevoke: an allow-for-session journals
// one grant per rule, the grants are a fold over the journal (a restarted
// coordinator rebuilds them), and a revoke pushes the remaining rules to the
// run before it is journaled.
func TestApprovalQueue_SessionGrantsFoldAndRevoke(t *testing.T) {
	q, push, path := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)

	done := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, q.Answer(id, ApprovalDecision{Allow: true, SessionRules: []string{"Bash(ls:*)", "Bash(cat:*)"}}))
	d := <-done
	assert.Equal(t, []string{"Bash(ls:*)", "Bash(cat:*)"}, d.SessionRules)
	awaitEvent(t, events, QueueGrantsChanged)

	grants := q.Grants(askerID.Harp)
	require.Len(t, grants, 2)
	for i, rule := range []string{"Bash(ls:*)", "Bash(cat:*)"} {
		assert.Equal(t, rule, grants[i].Rule)
		assert.Equal(t, askerID.Harp, grants[i].Harp)
		assert.Equal(t, id, grants[i].From)
		assert.NotEmpty(t, grants[i].ID)
	}
	assert.Empty(t, q.Grants("sibling"), "grants are per harp")
	assert.Empty(t, push.calls, "a grant rides the decision itself; only a revoke pushes")

	// The fold survives a restart: a fresh store over the same journal.
	reopened := NewApprovalQueue(openQueueStore(t, path), time.Now, push.push)
	assert.Equal(t, grants, reopened.Grants(askerID.Harp))

	require.NoError(t, q.Revoke(askerID.Harp, grants[0].ID))
	require.Len(t, push.calls, 1)
	assert.Equal(t, pushedGrants{harp: askerID.Harp, rules: []string{"Bash(cat:*)"}}, push.calls[0])
	assert.Equal(t, []Grant{grants[1]}, q.Grants(askerID.Harp))
	assert.ErrorIs(t, q.Revoke(askerID.Harp, grants[0].ID), ErrNoSuchGrant)

	again := NewApprovalQueue(openQueueStore(t, path), time.Now, push.push)
	assert.Equal(t, []Grant{grants[1]}, again.Grants(askerID.Harp), "the revoke is journaled too")
	assert.Equal(t,
		[]string{factApprovalParked, factApprovalDecided, factGrantAdded, factGrantAdded, factGrantRevoked},
		factKinds(readFacts(t, path)))
}

// TestApprovalQueue_RevokeRefusedByTheRunKeepsTheGrant: a revoke the run could
// not take is not journaled — the record never claims a rule is gone while the
// run still applies it.
func TestApprovalQueue_RevokeRefusedByTheRunKeepsTheGrant(t *testing.T) {
	q, push, _ := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)
	done := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, q.Answer(id, ApprovalDecision{Allow: true, SessionRules: []string{"Bash(ls:*)"}}))
	<-done

	push.err = errors.New("runner refused")
	grant := q.Grants(askerID.Harp)[0]
	assert.ErrorContains(t, q.Revoke(askerID.Harp, grant.ID), "runner refused")
	assert.Equal(t, []Grant{grant}, q.Grants(askerID.Harp))
}

// TestApprovalQueue_DeniedSessionRulesGrantNothing: rules riding a DENY are
// not grants.
func TestApprovalQueue_DeniedSessionRulesGrantNothing(t *testing.T) {
	q, _, path := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)
	done := parkAsync(ctx, q, toolAsk("Bash"), time.Minute)
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, q.Answer(id, ApprovalDecision{Allow: false, SessionRules: []string{"Bash(ls:*)"}}))
	<-done
	assert.Empty(t, q.Grants(askerID.Harp))
	assert.Equal(t, []string{factApprovalParked, factApprovalDecided}, factKinds(readFacts(t, path)))
}

// TestApprovalQueue_ApprovedPlanIsJournaled: approving a plan records the
// posture it executes under and the plan's digest and path.
func TestApprovalQueue_ApprovedPlanIsJournaled(t *testing.T) {
	q, _, path := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)
	req := PendingApproval{Kind: ApprovalPlan, Ask: engine.PermissionAsk{
		Kind: engine.AskPlan, Tool: "ExitPlanMode",
		Plan: &engine.PlanProposal{Markdown: "# plan\n1. do it", Path: "/home/x/.claude/plans/p.md"},
	}}
	done := parkAsync(ctx, q, req, time.Minute)
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, q.Answer(id, ApprovalDecision{Allow: true, SetMode: engine.Provide(engine.PermissionAcceptEdits)}))
	<-done

	planSum := sha256.Sum256([]byte("# plan\n1. do it"))
	facts := readFacts(t, path)
	require.Equal(t, []string{factApprovalParked, factApprovalDecided, factPlanApproved}, factKinds(facts))
	var plan map[string]any
	require.NoError(t, json.Unmarshal(facts[2].Data, &plan))
	assert.Equal(t, "acceptEdits", plan["posture"])
	assert.Equal(t, "/home/x/.claude/plans/p.md", plan["path"])
	assert.Equal(t, "sha256:"+hex.EncodeToString(planSum[:]), plan["digest"])
	assert.Equal(t, string(id), plan["id"])
}

// TestApprovalQueue_RejectedPlanIsNotApproved: a rejected plan journals its
// decision and nothing else.
func TestApprovalQueue_RejectedPlanIsNotApproved(t *testing.T) {
	q, _, path := newTestQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := q.Subscribe(ctx)
	req := PendingApproval{Kind: ApprovalPlan, Ask: engine.PermissionAsk{Kind: engine.AskPlan, Plan: &engine.PlanProposal{Markdown: "x"}}}
	done := parkAsync(ctx, q, req, time.Minute)
	id := awaitEvent(t, events, QueueAdded).ID
	require.NoError(t, q.Answer(id, ApprovalDecision{Allow: false, Message: "split step 2"}))
	d := <-done
	assert.Equal(t, "split step 2", d.Message)
	assert.Equal(t, []string{factApprovalParked, factApprovalDecided}, factKinds(readFacts(t, path)))
}

// TestApprovalQueue_UnjournaledParkFailsClosed: a request the journal cannot
// record is denied at once — every request is journaled or it is not asked.
func TestApprovalQueue_UnjournaledParkFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := openQueueStore(t, path)
	q := NewApprovalQueue(store, time.Now, (&grantPush{}).push)
	require.NoError(t, store.Close())
	d := q.Park(context.Background(), askerID, toolAsk("Bash"), time.Minute)
	assert.False(t, d.Allow)
	assert.Equal(t, agent.DeciderRefused, d.Decider)
	assert.Empty(t, q.Pending())
}

// TestApprovalQueue_IsTheSource: the queue is what a presenter consumes.
func TestApprovalQueue_IsTheSource(t *testing.T) {
	var _ ApprovalSource = (*ApprovalQueue)(nil)
}
