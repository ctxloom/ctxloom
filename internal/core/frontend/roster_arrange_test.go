package frontend_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/testsupport/frontendfake"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// arrangeRows is a lineage-ordered tree with rows of every age and status,
// the clock at t0.
func arrangeRows() []frontend.RosterRow {
	ago := func(d time.Duration) time.Time { return t0.Add(-d) }
	day := 24 * time.Hour
	return []frontend.RosterRow{
		{Harp: "self", State: frontend.SessionLive, SessionActivity: ago(30 * day)},
		{Harp: "fresh-kid", Parent: "self", Depth: 1, State: coord.StateIdle, LastActivity: ago(time.Hour)},
		{Harp: "stale-parent", Parent: "self", Depth: 1, State: coord.StateIdle, LastActivity: ago(10 * day)},
		{Harp: "busy-grandkid", Parent: "stale-parent", Depth: 2, State: coord.StateExecuting, LastActivity: ago(10 * day)},
		{Harp: "stale-asker", Parent: "self", Depth: 1, State: coord.StateIdle, LastActivity: ago(8 * day)},
		{Harp: "stale-mailed", Parent: "self", Depth: 1, State: coord.StateIdle, LastActivity: ago(8 * day), Unread: 1},
		{Harp: "fresh-ended", State: frontend.SessionEnded, SessionActivity: ago(day)},
		{Harp: "stale-ended", State: frontend.SessionEnded, SessionActivity: ago(9 * day)},
	}
}

func harps(r frontend.Roster) []string {
	out := []string{}
	for _, row := range r.Rows {
		out = append(out, row.Harp)
	}
	return out
}

func contextHarps(r frontend.Roster) []string {
	out := []string{}
	for _, row := range r.Rows {
		if row.Context {
			out = append(out, row.Harp)
		}
	}
	return out
}

// With no filter every row shows, each carrying its approvals, and a level's
// waiting rows go first — the running session excepted.
func TestArrangeRoster_NoFilterShowsEveryRowWaitingFirst(t *testing.T) {
	r := frontend.ArrangeRoster(arrangeRows(), map[string]int{"stale-asker": 2}, frontend.RosterQuery{Self: "self", Now: t0})
	assert.Equal(t, []string{"self", "stale-asker", "fresh-kid", "stale-parent", "busy-grandkid", "stale-mailed", "fresh-ended", "stale-ended"}, harps(r))
	assert.Equal(t, 2, r.Rows[1].Pending)
	assert.Zero(t, r.Hidden)
	assert.Empty(t, contextHarps(r))
}

// The window hides a row last active before it unless it matters now —
// running, waiting on an approval, holding mail — and the parent of a shown
// row stays, as context.
func TestArrangeRoster_TheWindow(t *testing.T) {
	r := frontend.ArrangeRoster(arrangeRows(), map[string]int{"stale-asker": 1},
		frontend.RosterQuery{Self: "self", Now: t0, Window: 7 * 24 * time.Hour})
	assert.Equal(t, []string{"self", "stale-asker", "fresh-kid", "stale-parent", "busy-grandkid", "stale-mailed", "fresh-ended"}, harps(r))
	assert.Equal(t, []string{"stale-parent"}, contextHarps(r), "kept only as a running row's parent")
	assert.Equal(t, 1, r.Hidden)
}

// Each status filter shows its rows; the running session and the selection
// always show, as context when they do not pass.
func TestArrangeRoster_StatusFilters(t *testing.T) {
	pending := map[string]int{"stale-asker": 1}
	arrange := func(q frontend.RosterQuery) frontend.Roster {
		q.Self, q.Now = "self", t0
		return frontend.ArrangeRoster(arrangeRows(), pending, q)
	}

	r := arrange(frontend.RosterQuery{Status: frontend.StatusActive})
	assert.Equal(t, []string{"self", "stale-asker", "fresh-kid", "stale-parent", "busy-grandkid", "stale-mailed"}, harps(r))
	assert.Empty(t, contextHarps(r))
	assert.Equal(t, 2, r.Hidden)

	r = arrange(frontend.RosterQuery{Status: frontend.StatusWaiting})
	assert.Equal(t, []string{"self", "stale-asker"}, harps(r))
	assert.Equal(t, []string{"self"}, contextHarps(r))

	r = arrange(frontend.RosterQuery{Status: frontend.StatusEnded, Selected: "busy-grandkid"})
	assert.Equal(t, []string{"self", "stale-parent", "busy-grandkid", "fresh-ended", "stale-ended"}, harps(r))
	assert.Equal(t, []string{"self", "stale-parent", "busy-grandkid"}, contextHarps(r), "the selection and its parent, kept")
}

// A re-arrangement starts clean: Context and Pending are the query's, not
// what an earlier arrangement left on the rows.
func TestArrangeRoster_RecomputesContextAndPending(t *testing.T) {
	rows := arrangeRows()
	rows[1].Context, rows[1].Pending = true, 5
	r := frontend.ArrangeRoster(rows, nil, frontend.RosterQuery{Self: "self", Now: t0})
	assert.Empty(t, contextHarps(r))
	for _, row := range r.Rows {
		assert.Zero(t, row.Pending, row.Harp)
	}
}

// A zero Now counts the window back from the wall clock.
func TestArrangeRoster_ZeroNowIsTheWallClock(t *testing.T) {
	rows := []frontend.RosterRow{
		{Harp: "self", State: frontend.SessionLive},
		{Harp: "recent", State: frontend.SessionEnded, SessionActivity: time.Now().Add(-time.Minute)},
		{Harp: "old", State: frontend.SessionEnded, SessionActivity: time.Now().Add(-2 * time.Hour)},
	}
	r := frontend.ArrangeRoster(rows, nil, frontend.RosterQuery{Self: "self", Window: time.Hour})
	assert.Equal(t, []string{"self", "recent"}, harps(r))
}

type queue struct{ coord.ApprovalSource }

func (queue) Pending() []coord.PendingApproval {
	return []coord.PendingApproval{{From: coord.Identity{Harp: "kid"}}, {From: coord.Identity{Harp: "kid"}}}
}

// ComposeRoster merges the port's index and runs and reads each agent's
// approvals off its queue.
func TestComposeRoster_MergesTheIndexRunsAndApprovals(t *testing.T) {
	p := &frontendfake.Port{
		ListSessionsFn: func(context.Context) ([]frontend.Session, error) {
			return []frontend.Session{{Harp: "older", State: frontend.SessionEnded}}, nil
		},
		ListRunsFn: func(context.Context) (coord.RunsSnapshot, error) {
			return coord.RunsSnapshot{Runs: []coord.RunInfo{{Agent: &coord.AgentIdentity{AgentID: "kid", Role: "worker"}, ParentHarp: "self", Phase: coord.StateIdle}}}, nil
		},
		ApprovalSource: queue{},
	}
	r, err := p.Roster(context.Background(), frontend.RosterQuery{Self: "self"})
	require.NoError(t, err)
	assert.Equal(t, []string{"self", "kid", "older"}, harps(r))
	assert.Equal(t, 2, r.Rows[1].Pending)
	assert.Equal(t, 1, r.Rows[1].Depth)
}

// The runs are enrichment: a port that cannot list them still has a roster.
// The index is not: its failure is the roster's.
func TestComposeRoster_Failures(t *testing.T) {
	p := &frontendfake.Port{
		ListSessionsFn: func(context.Context) ([]frontend.Session, error) {
			return []frontend.Session{{Harp: "older", State: frontend.SessionEnded}}, nil
		},
		ListRunsFn: func(context.Context) (coord.RunsSnapshot, error) {
			return coord.RunsSnapshot{}, errors.New("no coordinator")
		},
	}
	r, err := frontend.ComposeRoster(context.Background(), p, frontend.RosterQuery{Self: "self"})
	require.NoError(t, err)
	assert.Equal(t, []string{"self", "older"}, harps(r))

	broken := errors.New("index unreadable")
	p.ListSessionsFn = func(context.Context) ([]frontend.Session, error) { return nil, broken }
	_, err = frontend.ComposeRoster(context.Background(), p, frontend.RosterQuery{Self: "self"})
	assert.ErrorIs(t, err, broken)
}
