package coord

import (
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// parkFrom parks req from asker on a goroutine, returning its decision, and
// waits for the park to land (a QueueAdded event — never a poll).
func parkFrom(t *testing.T, q *ApprovalQueue, events <-chan QueueEvent, asker Identity, req PendingApproval) (ApprovalID, <-chan ApprovalDecision) {
	t.Helper()
	out := make(chan ApprovalDecision, 1)
	go func() { out <- q.Park(t.Context(), asker, req, time.Hour) }()
	return awaitEvent(t, events, QueueAdded).ID, out
}

func decided(t *testing.T, ch <-chan ApprovalDecision) ApprovalDecision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the request was never decided")
		return ApprovalDecision{}
	}
}

func pendingIDs(q *ApprovalQueue) []ApprovalID {
	var ids []ApprovalID
	for _, p := range q.Pending() {
		ids = append(ids, p.ID)
	}
	return ids
}

// grantedDecision is how a request a grant covers is resolved: allowed,
// granting nothing further, decided by the grant.
func assertGrantDecided(t *testing.T, d ApprovalDecision) {
	t.Helper()
	assert.True(t, d.Allow)
	assert.Equal(t, agent.DeciderGrant, d.Decider)
	assert.Empty(t, d.SessionRules, "a covered request grants nothing of its own")
}

// TestApprovalQueue_AGrantResolvesTheAskersCoveredRequests: the human's
// allow-for-session resolves the same child's other parked requests the new
// rule covers — as the grant's decision — and leaves the rest: a call the
// rule does not cover, another child's call, and a request that is not a
// tool call.
func TestApprovalQueue_AGrantResolvesTheAskersCoveredRequests(t *testing.T) {
	q, _, path := newTestQueue(t)
	events := q.Subscribe(t.Context())
	other := Identity{Harp: "other-harp", RunID: "run-9", Depth: 1}
	question := toolAsk("Bash")
	question.Kind, question.Ask.Kind = ApprovalQuestion, engine.AskQuestion

	asked, askedDone := parkFrom(t, q, events, askerID, toolAsk("Bash"))
	_, sibling := parkFrom(t, q, events, askerID, toolAsk("Bash"))
	write, _ := parkFrom(t, q, events, askerID, toolAsk("Write"))
	elsewhere, _ := parkFrom(t, q, events, other, toolAsk("Bash"))
	asking, _ := parkFrom(t, q, events, askerID, question)

	require.NoError(t, q.Answer(asked, ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}}))
	assert.Equal(t, agent.DeciderHuman, decided(t, askedDone).Decider)
	assertGrantDecided(t, decided(t, sibling))
	assert.ElementsMatch(t, []ApprovalID{write, elsewhere, asking}, pendingIDs(q))

	var deciders []string
	for _, f := range readFacts(t, path) {
		if f.Kind == factApprovalDecided {
			applyDecoded(f, func(p approvalDecided, _ time.Time) { deciders = append(deciders, p.Decider.String()) })
		}
	}
	assert.Equal(t, []string{"human", "grant"}, deciders, "the covered request's decision is journaled as the grant's")
}

// TestApprovalQueue_ARequestParkingWhileAGrantLandsIsCovered forces the
// race: a request of the same child parks after the grant is journaled and
// before the queue looks for what it covers. It is covered all the same.
func TestApprovalQueue_ARequestParkingWhileAGrantLandsIsCovered(t *testing.T) {
	q, _, _ := newTestQueue(t)
	events := q.Subscribe(t.Context())
	asked, _ := parkFrom(t, q, events, askerID, toolAsk("Bash"))
	var late <-chan ApprovalDecision
	q.grantHook = func() { _, late = parkFrom(t, q, events, askerID, toolAsk("Bash")) }

	require.NoError(t, q.Answer(asked, ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}}))
	require.NotNil(t, late, "the hook ran")
	assertGrantDecided(t, decided(t, late))
	assert.Empty(t, q.Pending())
}

// TestApprovalQueue_ARequestParkingAfterAGrantIsCovered: the run's grant
// stands for the rest of the run — a later request it covers never waits on
// the human.
func TestApprovalQueue_ARequestParkingAfterAGrantIsCovered(t *testing.T) {
	q, _, _ := newTestQueue(t)
	events := q.Subscribe(t.Context())
	asked, _ := parkFrom(t, q, events, askerID, toolAsk("Bash"))
	require.NoError(t, q.Answer(asked, ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}}))

	parkExpectingGrant(t, q, events)
}

// parkExpectingGrant parks a Bash request from askerID that a grant should
// cover: it is decided by the grant and never put to the human.
func parkExpectingGrant(t *testing.T, q *ApprovalQueue, events <-chan QueueEvent) {
	t.Helper()
	out := make(chan ApprovalDecision, 1)
	go func() { out <- q.Park(t.Context(), askerID, toolAsk("Bash"), time.Hour) }()
	for {
		select {
		case d := <-out:
			assertGrantDecided(t, d)
			assert.Empty(t, q.Pending())
			return
		case ev := <-events:
			require.NotEqual(t, QueueAdded, ev.Kind, "a covered request is never put to the human")
		case <-time.After(10 * time.Second):
			t.Fatal("the request was never decided")
		}
	}
}

// TestApprovalQueue_WhatAGrantNoLongerCovers: a revoked rule, a rule of the
// asker's ended run, and a deny's rules cover nothing — the request waits on
// the human.
func TestApprovalQueue_WhatAGrantNoLongerCovers(t *testing.T) {
	grant := func(t *testing.T, q *ApprovalQueue, events <-chan QueueEvent, d ApprovalDecision) {
		t.Helper()
		asked, done := parkFrom(t, q, events, askerID, toolAsk("Bash"))
		require.NoError(t, q.Answer(asked, d))
		decided(t, done)
	}
	for name, tc := range map[string]struct {
		decision ApprovalDecision
		after    func(t *testing.T, q *ApprovalQueue)
		asker    Identity
	}{
		"revoked": {
			decision: ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}},
			after: func(t *testing.T, q *ApprovalQueue) {
				gs := q.Grants(askerID.Harp)
				require.Len(t, gs, 1)
				require.NoError(t, q.Revoke(askerID.Harp, gs[0].ID))
			},
			asker: askerID,
		},
		"the run ended": {
			decision: ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}},
			after: func(t *testing.T, q *ApprovalQueue) {
				q.cancelFrom(askerID.Harp, askerID.RunID)
				q.mu.Lock()
				defer q.mu.Unlock()
				assert.Empty(t, q.runGrants, "the run's set goes with the run")
			},
			asker: Identity{Harp: askerID.Harp, RunID: "run-2", Depth: 1},
		},
		"denied": {
			decision: ApprovalDecision{Allow: false, SessionRules: []string{"Bash"}},
			asker:    askerID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			q, _, _ := newTestQueue(t)
			events := q.Subscribe(t.Context())
			grant(t, q, events, tc.decision)
			if tc.after != nil {
				tc.after(t, q)
			}
			id, _ := parkFrom(t, q, events, tc.asker, toolAsk("Bash"))
			assert.Equal(t, []ApprovalID{id}, pendingIDs(q), "the request waits on the human")
		})
	}
}

// revokeQueue is a queue whose run-side push is push, holding one grant of
// "Bash" for askerID's run.
func revokeQueue(t *testing.T, push func(q *ApprovalQueue) error) (*ApprovalQueue, <-chan QueueEvent, Grant) {
	t.Helper()
	var q *ApprovalQueue
	q = NewApprovalQueue(openQueueStore(t, filepath.Join(t.TempDir(), "runs.jsonl")), time.Now,
		func(string, engine.Name, []string) error { return push(q) }, noRunGone, coversTool)
	events := q.Subscribe(t.Context())
	asked, done := parkFrom(t, q, events, askerID, toolAsk("Bash"))
	require.NoError(t, q.Answer(asked, ApprovalDecision{Allow: true, SessionRules: []string{"Bash"}}))
	decided(t, done)
	gs := q.Grants(askerID.Harp)
	require.Len(t, gs, 1)
	return q, events, gs[0]
}

// TestApprovalQueue_ARequestParkingDuringARevokeIsNotCovered forces the
// race: the request parks while the revoke is being handed to the run. The
// rule already covers nothing — the request waits on the human.
func TestApprovalQueue_ARequestParkingDuringARevokeIsNotCovered(t *testing.T) {
	var during ApprovalID
	var events <-chan QueueEvent
	q, ev, g := revokeQueue(t, func(q *ApprovalQueue) error {
		during, _ = parkFrom(t, q, events, askerID, toolAsk("Bash"))
		return nil
	})
	events = ev
	require.NoError(t, q.Revoke(askerID.Harp, g.ID))
	assert.Equal(t, []ApprovalID{during}, pendingIDs(q))
}

// TestApprovalQueue_ARefusedRevokeStillCovers: the run refused the revoke,
// so the grant stands — and still covers the run's requests.
func TestApprovalQueue_ARefusedRevokeStillCovers(t *testing.T) {
	q, events, g := revokeQueue(t, func(*ApprovalQueue) error { return errors.New("the run refused") })
	require.Error(t, q.Revoke(askerID.Harp, g.ID))
	parkExpectingGrant(t, q, events)
}

// TestApprovalQueue_ARefusedRevokeRevivesNoEndedRun: the run ends while its
// revoke is being pushed, and the push fails. Putting the rule back must
// not resurrect the ended run's set.
func TestApprovalQueue_ARefusedRevokeRevivesNoEndedRun(t *testing.T) {
	q, _, g := revokeQueue(t, func(q *ApprovalQueue) error {
		q.cancelFrom(askerID.Harp, askerID.RunID)
		return errors.New("the run is gone")
	})
	require.Error(t, q.Revoke(askerID.Harp, g.ID))
	q.mu.Lock()
	defer q.mu.Unlock()
	assert.Empty(t, q.runGrants)
}

// onEngine is req as asked by a run of engine e.
func onEngine(req PendingApproval, e engine.Name) PendingApproval {
	req.engine = e
	return req
}

// seeded is what Seed delivers to run for eng.
func seeded(t *testing.T, q *ApprovalQueue, run Identity, eng engine.Name) []string {
	t.Helper()
	var got []string
	require.NoError(t, q.Seed(run, eng, func(rules []string) error {
		got = rules
		return nil
	}))
	return got
}

// grantedOn has askerID's run, on engine e, granted rules for the session,
// and then ends that run.
func grantedOn(t *testing.T, q *ApprovalQueue, e engine.Name, rules ...string) {
	t.Helper()
	events := q.Subscribe(t.Context())
	asked, done := parkFrom(t, q, events, askerID, onEngine(toolAsk("Bash"), e))
	require.NoError(t, q.Answer(asked, ApprovalDecision{Allow: true, SessionRules: rules}))
	decided(t, done)
	q.cancelFrom(askerID.Harp, askerID.RunID)
}

var resumedID = Identity{Harp: askerID.Harp, RunID: "run-2", Depth: 1}

// TestApprovalQueue_AResumedRunIsSeededWithItsHarpsGrants: a grant made in
// one run carries to the harp's next run — the journal's grants, minus what
// was revoked — and covers that run's requests from their arrival.
func TestApprovalQueue_AResumedRunIsSeededWithItsHarpsGrants(t *testing.T) {
	q, _, _ := newTestQueue(t)
	grantedOn(t, q, "mock", "Bash", "Read")
	for _, g := range q.Grants(askerID.Harp) {
		if g.Rule == "Read" {
			require.NoError(t, q.Revoke(askerID.Harp, g.ID))
		}
	}

	assert.Equal(t, []string{"Bash"}, seeded(t, q, resumedID, "mock"), "the harp's grants, less the revoked one")
	d := q.Park(t.Context(), resumedID, onEngine(toolAsk("Bash"), "mock"), time.Hour)
	assertGrantDecided(t, d)
}

// TestApprovalQueue_ARunOnAnotherEngineIsSeededNothing: grants are rules in
// the granting engine's syntax, so a run of the harp on another engine holds
// none of them — and they cover none of its requests.
func TestApprovalQueue_ARunOnAnotherEngineIsSeededNothing(t *testing.T) {
	q, _, _ := newTestQueue(t)
	grantedOn(t, q, "mock", "Bash")
	assert.Empty(t, seeded(t, q, resumedID, "claude"))

	events := q.Subscribe(t.Context())
	id, _ := parkFrom(t, q, events, resumedID, onEngine(toolAsk("Bash"), "claude"))
	assert.Equal(t, []ApprovalID{id}, pendingIDs(q), "the request waits on the human")
}

// TestApprovalQueue_AGrantIsHeldPerEngine: the same rule granted on another
// engine is a grant of its own, not one the harp already holds.
func TestApprovalQueue_AGrantIsHeldPerEngine(t *testing.T) {
	q, _, _ := newTestQueue(t)
	grantedOn(t, q, "mock", "Bash")
	grantedOn(t, q, "claude", "Bash")
	assert.Equal(t, []string{"Bash"}, seeded(t, q, resumedID, "claude"))
}

// TestApprovalQueue_ASeedNotDeliveredCoversNothing: a run that never took
// its seed holds none of it.
func TestApprovalQueue_ASeedNotDeliveredCoversNothing(t *testing.T) {
	q, _, _ := newTestQueue(t)
	grantedOn(t, q, "mock", "Bash")
	require.Error(t, q.Seed(resumedID, "mock", func([]string) error { return errors.New("StartRun refused") }))
	q.mu.Lock()
	defer q.mu.Unlock()
	assert.Empty(t, q.runGrants)
}

// TestApprovalQueue_ARevokeWaitsForTheSeedToBeDelivered forces the race:
// the harp's grant is revoked while its resumed run is being handed the
// seed. Were the revoke to land then, the run would start on a rule the
// journal no longer holds, and the revoke's push — to a run not yet driving
// — would never reach it. So the revoke waits for the delivery, and its
// push then takes the rule off the run.
func TestApprovalQueue_ARevokeWaitsForTheSeedToBeDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, push, _ := newTestQueue(t)
		grantedOn(t, q, "mock", "Bash")
		g := q.Grants(askerID.Harp)[0]

		revoked := make(chan error, 1)
		require.NoError(t, q.Seed(resumedID, "mock", func(rules []string) error {
			go func() { revoked <- q.Revoke(askerID.Harp, g.ID) }()
			synctest.Wait()
			push.mu.Lock()
			defer push.mu.Unlock()
			assert.Empty(t, push.calls, "the revoke reached the run while its seed was in flight")
			assert.Equal(t, []string{"Bash"}, rules)
			return nil
		}))
		require.NoError(t, <-revoked)
		push.mu.Lock()
		defer push.mu.Unlock()
		require.Len(t, push.calls, 1)
		assert.Empty(t, push.calls[0].rules)
		q.mu.Lock()
		defer q.mu.Unlock()
		assert.Empty(t, q.runGrants[keyOf(resumedID)], "the revoked rule no longer covers the run")
	})
}

// TestApprovalQueue_ARevokePushesOnlyItsEnginesRules: a revoke hands the
// run the rest of the set in the revoked grant's engine, never another
// engine's rules.
func TestApprovalQueue_ARevokePushesOnlyItsEnginesRules(t *testing.T) {
	q, push, _ := newTestQueue(t)
	grantedOn(t, q, "claude", "Read")
	grantedOn(t, q, "mock", "Bash", "Edit")
	for _, g := range q.Grants(askerID.Harp) {
		if g.Rule == "Edit" {
			require.NoError(t, q.Revoke(askerID.Harp, g.ID))
		}
	}
	require.Len(t, push.calls, 1)
	assert.Equal(t, pushedGrants{harp: askerID.Harp, engine: "mock", rules: []string{"Bash"}}, push.calls[0])
}
