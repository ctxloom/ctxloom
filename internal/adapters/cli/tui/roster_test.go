package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// The session states BuildRoster is handed are the port's (the lock's
// verdict per session, probed by the port: operations'
// TestFrontendPort_ListSessions_StateIsTheLocksVerdict); these tests state
// them directly.

// held is the coordinator's run for harp as the port lists it.
func held(harp, agent, state, parent string) coord.RunInfo {
	return coord.RunInfo{Agent: &coord.AgentIdentity{AgentID: harp, Role: agent}, Phase: state, ParentHarp: parent}
}

func TestBuildRoster_SelfFirstAndIndexOrder(t *testing.T) {
	rows := BuildRoster([]frontend.Session{
		{Harp: "older-oak-hen", Engine: "claude-code", State: frontend.SessionEnded},
		{Harp: "perky-same-chevy", Engine: "claude-code", State: frontend.SessionLive},
	}, nil, "perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "perky-same-chevy", rows[0].Harp, "the running session pins to the top")
	assert.Equal(t, "live", rows[0].State)
	assert.Equal(t, "older-oak-hen", rows[1].Harp)
	assert.Equal(t, "ended", rows[1].State)
}

func TestBuildRoster_ChildrenNestUnderParent(t *testing.T) {
	rows := BuildRoster(
		[]frontend.Session{
			{Harp: "perky-same-chevy", Engine: "claude-code", State: frontend.SessionLive},
			{Harp: "unrelated-flat-owl", Engine: "claude-code", State: frontend.SessionUnknown},
			{Harp: "swift-elm-fox", Engine: "claude-code", State: frontend.SessionUnknown},
		},
		[]coord.RunInfo{
			held("swift-elm-fox", "developer", "executing", "perky-same-chevy"),
			held("deep-oak-hen", "finder", "ended", "perky-same-chevy"),
		},
		"perky-same-chevy")

	require.Len(t, rows, 4)
	assert.Equal(t, "perky-same-chevy", rows[0].Harp)
	assert.Equal(t, 0, rows[0].Depth)
	assert.Equal(t, "swift-elm-fox", rows[1].Harp, "children directly under their parent")
	assert.Equal(t, 1, rows[1].Depth)
	assert.Equal(t, "developer", rows[1].Agent, "the coordinator's run enriches the index row")
	assert.Equal(t, "executing", rows[1].State, "the coordinator's state wins over the lock's verdict")
	assert.Equal(t, "claude-code", rows[1].Engine, "index engine survives the merge")
	assert.Equal(t, "deep-oak-hen", rows[2].Harp, "a run with no index entry still shows")
	assert.Equal(t, 1, rows[2].Depth)
	assert.Equal(t, "unrelated-flat-owl", rows[3].Harp)
	assert.Equal(t, 0, rows[3].Depth)
}

// The merge is "the held row is RICHER", not "the held row replaces". A
// coordinator run whose state is empty — a journal whose runState fact
// decoded without one — has nothing to say about liveness, so the lock's
// verdict stands as if the row were not held; blanking the state would
// repaint a running session as the waiting glyph.
func TestBuildRoster_EmptyHeldFieldsDoNotBlankTheIndexRow(t *testing.T) {
	rows := BuildRoster(
		[]frontend.Session{
			{Harp: "perky-same-chevy", Engine: "claude-code", State: frontend.SessionLive},
			{Harp: "older-oak-hen", Engine: "claude-code", State: frontend.SessionEnded},
		},
		[]coord.RunInfo{
			held("perky-same-chevy", "", "", ""),
			held("older-oak-hen", "", "", ""),
		},
		"perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "live", rows[0].State, "an empty held state defers to the lock")
	assert.Equal(t, "ended", rows[1].State)
	assert.Equal(t, "●", stateGlyph(rows[0].State))
}

// A run the port lists without an agent identity names no harp, so it makes
// no row.
func TestBuildRoster_RunWithoutAnIdentityIsSkipped(t *testing.T) {
	rows := BuildRoster(nil, []coord.RunInfo{{RunID: "r1", Phase: "queued"}, held("kid", "finder", "queued", "")}, "")
	require.Len(t, rows, 1)
	assert.Equal(t, "kid", rows[0].Harp)
}

func TestBuildRoster_OrphanChildPlacesFlat(t *testing.T) {
	rows := BuildRoster(nil, []coord.RunInfo{
		held("lost-kid", "finder", "queued", "gone-parent"),
	}, "")
	require.Len(t, rows, 1)
	assert.Equal(t, 0, rows[0].Depth, "an orphan (parent unknown) shows at the root")
}

// Characterization of the lineage walk before a later change replaces its
// scan-every-row-per-node placement with a children index: depth accumulates
// down the chain, siblings keep the order the roster produced them in, and a
// subtree is emitted immediately after its parent rather than after its
// parent's siblings.
func TestBuildRoster_GrandchildrenNestAndSiblingsKeepRosterOrder(t *testing.T) {
	rows := BuildRoster(nil, []coord.RunInfo{
		held("root", "", "live", ""),
		held("kid-b", "", "executing", "root"),
		held("kid-a", "", "queued", "root"),
		held("grandkid", "", "queued", "kid-b"),
		held("other-root", "", "live", ""),
	}, "")

	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s@%d", r.Harp, r.Depth))
	}
	assert.Equal(t, []string{"root@0", "kid-b@1", "grandkid@2", "kid-a@1", "other-root@0"}, got)
}

// A parent cycle cannot arise from the coordinator, but the walk must still
// emit every row exactly once rather than dropping the cycle's members.
func TestBuildRoster_ParentCycleIsPlacedFlatNotLost(t *testing.T) {
	rows := BuildRoster(nil, []coord.RunInfo{
		held("a", "", "live", "b"),
		held("b", "", "live", "a"),
		held("c", "", "live", ""),
	}, "")

	require.Len(t, rows, 3, "no row is lost to the cycle")
	assert.Equal(t, "c", rows[0].Harp, "the real root places first")
	assert.Equal(t, []string{"a", "b"}, []string{rows[1].Harp, rows[2].Harp})
	assert.Equal(t, 0, rows[1].Depth)
	assert.Equal(t, 1, rows[2].Depth, "the cycle's second member still nests under the first")
}

func TestStateGlyphs(t *testing.T) {
	assert.Equal(t, "●", stateGlyph("executing"))
	assert.Equal(t, "●", stateGlyph("live"))
	assert.Equal(t, "✓", stateGlyph("ended"))
	assert.Equal(t, "?", stateGlyph("unknown"))
	assert.Equal(t, "◐", stateGlyph("queued"))
	assert.Equal(t, "◐", stateGlyph("parked"))
	assert.Equal(t, "◐", stateGlyph("idle"))
}

// TestBuildRoster_CarriesTheHold: a held child's hold reaches its row whether
// the row came from the index or from the coordinator alone.
func TestBuildRoster_CarriesTheHold(t *testing.T) {
	hold := &coord.RunHold{Kind: "rate_limited", Source: "env:X", Until: time.Date(2026, 10, 1, 17, 30, 0, 0, time.UTC)}
	withHold := func(r coord.RunInfo, h *coord.RunHold) coord.RunInfo { r.Hold = h; return r }
	rows := BuildRoster(
		[]frontend.Session{{Harp: "indexed-kid", Engine: "claude-code", State: frontend.SessionUnknown}},
		[]coord.RunInfo{
			withHold(held("indexed-kid", "", coord.StateIdle, ""), hold),
			withHold(held("bus-only-kid", "", coord.StateIdle, ""), hold),
			held("free-kid", "", coord.StateIdle, ""),
		}, "")
	byHarp := map[string]RosterRow{}
	for _, r := range rows {
		byHarp[r.Harp] = r
	}
	assert.Equal(t, hold, byHarp["indexed-kid"].Hold)
	assert.Equal(t, hold, byHarp["bus-only-kid"].Hold)
	assert.Nil(t, byHarp["free-kid"].Hold)
}

// TestBuildRoster_CarriesTheTerminalCause: an ended child's cause and detail
// reach its row whether the row came from the index or from the coordinator
// alone.
func TestBuildRoster_CarriesTheTerminalCause(t *testing.T) {
	const detail = "the engine binary is not on PATH"
	ended := func(harp, cause string) coord.RunInfo {
		r := held(harp, "", coord.StateEnded, "")
		r.Cause, r.Detail = cause, detail
		return r
	}
	rows := BuildRoster(
		[]frontend.Session{{Harp: "indexed-kid", Engine: "claude-code", State: frontend.SessionEnded}},
		[]coord.RunInfo{ended("indexed-kid", coord.CauseLaunchFailed), ended("bus-only-kid", coord.CauseRunnerLoss)}, "")
	byHarp := map[string]RosterRow{}
	for _, r := range rows {
		byHarp[r.Harp] = r
	}
	assert.Equal(t, coord.CauseLaunchFailed, byHarp["indexed-kid"].Cause)
	assert.Equal(t, detail, byHarp["indexed-kid"].Detail)
	assert.Equal(t, coord.CauseRunnerLoss, byHarp["bus-only-kid"].Cause)
	assert.Equal(t, detail, byHarp["bus-only-kid"].Detail)
}

// TestBuildRoster_CarriesTheUnreadCount: a child's unread mail count reaches
// its row whether the row came from the index or from the coordinator alone.
func TestBuildRoster_CarriesTheUnreadCount(t *testing.T) {
	unread := func(harp string, n int) coord.RunInfo {
		r := held(harp, "", coord.StateExecuting, "")
		r.Unread = n
		return r
	}
	rows := BuildRoster(
		[]frontend.Session{{Harp: "indexed-kid", Engine: "claude-code", State: frontend.SessionLive}},
		[]coord.RunInfo{unread("indexed-kid", 2), unread("bus-only-kid", 5)}, "")
	byHarp := map[string]RosterRow{}
	for _, r := range rows {
		byHarp[r.Harp] = r
	}
	assert.Equal(t, 2, byHarp["indexed-kid"].Unread)
	assert.Equal(t, 5, byHarp["bus-only-kid"].Unread)
}

// TestBuildRoster_CarriesActivityAndTheContextGauge: when a run last
// changed state and how full its context is reach its row, from the index or
// from the coordinator alone.
func TestBuildRoster_CarriesActivityAndTheContextGauge(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gauge := func(harp string, used, window uint64, d time.Duration) coord.RunInfo {
		r := held(harp, "", coord.StateIdle, "")
		r.ContextTokens, r.ContextWindow, r.LastEventAt = used, window, at.Add(d)
		return r
	}
	rows := BuildRoster(
		[]frontend.Session{{Harp: "indexed-kid", Engine: "claude-code", State: frontend.SessionLive}},
		[]coord.RunInfo{gauge("indexed-kid", 1000, 200_000, time.Second), gauge("bus-only-kid", 7, 9, time.Minute)}, "")
	byHarp := map[string]RosterRow{}
	for _, r := range rows {
		byHarp[r.Harp] = r
	}
	assert.Equal(t, [3]any{uint64(1000), uint64(200_000), at.Add(time.Second)},
		[3]any{byHarp["indexed-kid"].ContextTokens, byHarp["indexed-kid"].ContextWindow, byHarp["indexed-kid"].LastActivity})
	assert.Equal(t, [3]any{uint64(7), uint64(9), at.Add(time.Minute)},
		[3]any{byHarp["bus-only-kid"].ContextTokens, byHarp["bus-only-kid"].ContextWindow, byHarp["bus-only-kid"].LastActivity})
}

// TestRosterStates_AreTheListingsWords: the roster and `ctxloom session list`
// say a session's state in one vocabulary, the port's — and the overlay's
// ended word is the coordinator's, so that must be the port's too.
func TestRosterStates_AreTheListingsWords(t *testing.T) {
	assert.Equal(t, frontend.SessionLive, StateLive)
	assert.Equal(t, frontend.SessionEnded, StateEnded)
	assert.Equal(t, frontend.SessionUnknown, StateUnknown)
}
