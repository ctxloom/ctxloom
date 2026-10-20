package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// rosterOrder is rows as harp@depth.
func rosterOrder(rows []RosterRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Harp + "@" + string(rune('0'+r.Depth))
	}
	return out
}

// The tree is ordered by activity, most recent first, level by level:
// children stay under their parent, siblings ordered among themselves, ties
// by name. An index session's activity is the index's last-worked time; a
// held run's is the later of that and the coordinator's last event. The
// running session stays pinned on top.
func TestBuildRoster_OrdersEachLevelByActivity(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	run := func(harp, parent string, last time.Time) coord.RunInfo {
		r := held(harp, "", coord.StateIdle, parent)
		r.LastEventAt = last
		return r
	}
	rows := BuildRoster(
		[]frontend.Session{
			{Harp: "self-run", State: frontend.SessionLive, LastActivity: at(1)},
			{Harp: "old-root", State: frontend.SessionEnded, LastActivity: at(5)},
			{Harp: "new-root", State: frontend.SessionEnded, LastActivity: at(9)},
			{Harp: "kid-quiet", LastActivity: at(2)},
			{Harp: "kid-busy", LastActivity: at(3)},
		},
		[]coord.RunInfo{
			run("kid-quiet", "self-run", at(4)), // the run's event is later: 4 beats kid-busy's 3
			run("kid-busy", "self-run", at(1)),
			run("grand-b", "kid-quiet", at(6)),
			run("grand-a", "kid-quiet", at(6)), // a tie: by name
			run("held-root", "", at(7)),
		},
		"self-run")

	assert.Equal(t, []string{
		"self-run@0", "kid-quiet@1", "grand-a@2", "grand-b@2", "kid-busy@1",
		"new-root@0", "held-root@0", "old-root@0",
	}, rosterOrder(rows))
}

// Re-ordering the tree never moves the selection: it stays on its harp, and
// the pane keeps that harp's feed.
func TestSplit_SelectionSurvivesAReorder(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateIdle).LastEventAt = t0.Add(2 * time.Minute)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateIdle).LastEventAt = t0.Add(time.Minute)
	h := newSplitHarness(t, port).sized(100, 10)
	require.Equal(t, []string{selfHarp + "@0", "brave-heron@1", "sleepy-ibis@1"}, rosterOrder(h.s.rows))

	h.typeBytes("\x1d")
	h.keys("j", "j")
	require.Equal(t, "sleepy-ibis", h.s.sel)
	port.setRuns(func(runs []coord.RunInfo) { runs[1].LastEventAt = t0.Add(3 * time.Minute) })
	h.send(rosterRefreshMsg{debounced: true})

	require.Equal(t, []string{selfHarp + "@0", "sleepy-ibis@1", "brave-heron@1"}, rosterOrder(h.s.rows))
	assert.Equal(t, "sleepy-ibis", h.s.sel, "the selection is its harp, not its index")
	selected := panelLinesWith(h, "sleepy-ibis")
	require.Len(t, selected, 1)
	assert.Equal(t, 1, h.s.visibleSel(h.s.visibleRows()), "drawn where the harp now is")
	assert.Equal(t, []string{"brave-heron", "sleepy-ibis"}, port.watchedHarps(), "the pane keeps its feed: no new watch")
	h.keys("j")
	assert.Equal(t, "brave-heron", h.s.sel, "moving on goes from where the harp now is")
}

// The running session is the first row however it is known: from the
// coordinator alone it is moved up, and known to neither it is made.
func TestBuildRoster_SelfIsAlwaysTheFirstRow(t *testing.T) {
	index := []frontend.Session{{Harp: "older-run", State: frontend.SessionEnded}}
	rows := BuildRoster(index, []coord.RunInfo{held("kid", "", coord.StateIdle, "self-run"), held("self-run", "lead", coord.StateExecuting, "")}, "self-run")
	assert.Equal(t, []string{"self-run@0", "kid@1", "older-run@0"}, rosterOrder(rows))
	assert.Equal(t, "lead", rows[0].Agent, "the coordinator's row, moved")

	rows = BuildRoster(index, nil, "self-run")
	assert.Equal(t, []string{"self-run@0", "older-run@0"}, rosterOrder(rows))
	assert.Equal(t, StateLive, rows[0].State, "this run is live")
}

// The coordinator journals the session's own run with its own harp as its
// parent (coord.RunRecord.TopLevel): that is a root, not a cycle — the
// session stays the first row, its children under it.
func TestBuildRoster_SelfParentedOwnerRunIsTheRoot(t *testing.T) {
	rows := BuildRoster(
		[]frontend.Session{{Harp: "older-run", State: frontend.SessionEnded}, {Harp: "self-run", State: frontend.SessionLive}},
		[]coord.RunInfo{held("self-run", "self-run", coord.StateExecuting, "self-run"), held("kid", "coder", coord.StateIdle, "self-run")},
		"self-run")
	assert.Equal(t, []string{"self-run@0", "kid@1", "older-run@0"}, rosterOrder(rows))
}

// Entering the list over a live session lands on its own row, which the
// coordinator lists as its own parent.
func TestSplit_EnteringTheListOverASelfParentedOwnerRun(t *testing.T) {
	port := newSplitPort()
	port.sessions = append(port.sessions, frontend.Session{Harp: "older-run", State: frontend.SessionEnded, LastActivity: time.Now()})
	port.child(selfHarp, selfHarp, selfHarp, coord.StateExecuting)
	port.child("brave-heron", "coder", selfHarp, coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 12)
	h.typeBytes("\x1d")
	assert.Equal(t, selfHarp, selectedHarp(h))
	assert.Equal(t, []string{selfHarp + "@0", "brave-heron@1", "older-run@0"}, rosterOrder(h.s.rows))
}

// An orphan (its parent unknown to the roster) is a root like any other,
// ordered by activity among them.
func TestBuildRoster_AnOrphanIsOrderedAmongTheRoots(t *testing.T) {
	kid := held("lost-kid", "", coord.StateIdle, "gone-parent")
	kid.LastEventAt = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	rows := BuildRoster([]frontend.Session{{Harp: "quiet-root", State: frontend.SessionEnded}}, []coord.RunInfo{kid}, "")
	assert.Equal(t, []string{"lost-kid@0", "quiet-root@0"}, rosterOrder(rows))
}
