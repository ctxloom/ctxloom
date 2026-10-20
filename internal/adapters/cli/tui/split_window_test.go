package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// windowHarness is a session with rows of every age and kind under a
// seven-day window, the clock at t0.
func windowHarness(t *testing.T) (*splitHarness, *splitPort) {
	t.Helper()
	port := newSplitPort()
	q := newFakeQueue()
	port.queue = q
	h := newSplitHarness(t, port, func(o *SplitOptions) { o.ListWindow = 7 * 24 * time.Hour })
	now := h.clk.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	port.sessions = []frontend.Session{
		{Harp: selfHarp, State: frontend.SessionLive, LastActivity: ago(30 * day)},
		{Harp: "fresh-ended", State: frontend.SessionEnded, LastActivity: ago(day)},
		{Harp: "stale-ended", State: frontend.SessionEnded, LastActivity: ago(9 * day)},
		{Harp: "stale-live", State: frontend.SessionLive, LastActivity: ago(9 * day)},
		{Harp: "stale-unknown", State: frontend.SessionUnknown, LastActivity: ago(20 * day)},
	}
	idle := func(harp string, parent string, last time.Time) *coord.RunInfo {
		r := port.child(harp, "", parent, coord.StateIdle)
		r.LastEventAt = last
		return r
	}
	idle("stale-kid", selfHarp, ago(8*day))
	idle("stale-mailed", selfHarp, ago(8*day)).Unread = 2
	idle("stale-parent", selfHarp, ago(10*day))
	port.child("busy-grandkid", "", "stale-parent", coord.StateExecuting).LastEventAt = ago(10 * day)
	idle("stale-asker", selfHarp, ago(8*day))
	q.add(bashReq("r1", "stale-asker", "ls", time.Hour))
	h.sized(100, 20)
	h.send(rosterRefreshMsg{debounced: true})
	h.send(approvalsCountMsg{n: 1})
	return h, port
}

func rowHarps(rows []RosterRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Harp
	}
	return out
}

// Only rows last active within ui.list_window show — always the run's own
// session, anything running, anything waiting on an approval or holding
// unread mail, and the parent of a row that shows — and a dim footer line
// says how many are hidden and the key that sets it.
func TestSplit_ListWindowHidesOldRows(t *testing.T) {
	h, _ := windowHarness(t)
	assert.ElementsMatch(t, []string{
		selfHarp, "stale-asker", "stale-mailed", "stale-parent", "busy-grandkid", "fresh-ended", "stale-live",
	}, rowHarps(h.s.rows))
	assert.Equal(t, selfHarp, h.s.rows[0].Harp)
	assert.Equal(t, "stale-asker", h.s.rows[1].Harp, "the waiting row first")
	assert.Equal(t, []string{" +3 older than 7d"}, panelLinesWith(h, "older"))
	assert.Equal(t, 3, h.s.hiddenRows)
}

// A hidden row that comes back to life — active again, mail, an approval —
// shows again; the footer line goes once nothing is hidden.
func TestSplit_ListWindowRowsReappear(t *testing.T) {
	h, port := windowHarness(t)
	q := port.queue.(*fakeQueue)
	port.setRuns(func(runs []coord.RunInfo) {
		runs[0].LastEventAt = h.clk.Now() // stale-kid: active again
	})
	port.mu.Lock()
	port.sessions[2].LastActivity = h.clk.Now().Add(-time.Hour) // stale-ended worked again
	port.mu.Unlock()
	h.send(rosterRefreshMsg{debounced: true})
	assert.Contains(t, rowHarps(h.s.rows), "stale-kid")
	assert.Contains(t, rowHarps(h.s.rows), "stale-ended")
	assert.Equal(t, []string{" +1 older than 7d"}, panelLinesWith(h, "older"))

	q.add(bashReq("r2", "stale-unknown", "ls", time.Hour))
	h.send(approvalsCountMsg{n: 2})
	assert.Contains(t, rowHarps(h.s.rows), "stale-unknown", "an approval brings a row back")
	assert.Empty(t, panelLinesWith(h, "older"), "nothing hidden: no line")
}

// Ageing out happens on the clock: a row past the window at the next
// roster read goes, unless it is the selection.
func TestSplit_ListWindowAgesRowsOutButNotTheSelection(t *testing.T) {
	h, _ := windowHarness(t)
	h.typeBytes("\x1d")
	for range h.s.rows {
		if selectedHarp(h) == "fresh-ended" {
			break
		}
		h.keys("j")
	}
	require.Equal(t, "fresh-ended", selectedHarp(h))
	h.clk.Advance(7 * 24 * time.Hour)
	h.send(rosterRefreshMsg{debounced: true})
	assert.Equal(t, "fresh-ended", selectedHarp(h), "the selection stays, aged or not")
	h.keys("k")
	h.send(rosterRefreshMsg{debounced: true})
	assert.NotContains(t, rowHarps(h.s.rows), "fresh-ended", "once left, an aged row goes")
}

// A zero window shows everything and says nothing.
func TestSplit_ZeroListWindowShowsEverything(t *testing.T) {
	port := newSplitPort()
	port.sessions = append(port.sessions, frontend.Session{Harp: "ancient", State: frontend.SessionEnded, LastActivity: time.Unix(0, 0)})
	h := newSplitHarness(t, port).sized(100, 12)
	assert.Equal(t, []string{selfHarp, "ancient"}, rowHarps(h.s.rows))
	assert.Empty(t, panelLinesWith(h, "older"))
}
