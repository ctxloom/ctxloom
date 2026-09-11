package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// holdLock mints harp's session lock under the test's isolated HOME and
// keeps it held until cleanup: the shape of a running session. freeLock
// mints it and lets go at once, leaving the file unlocked: the shape of a
// session that ended — or crashed, since the kernel drops the lock either
// way. A harp with neither has no lock file at all.
func holdLock(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
}

func freeLock(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
}

func TestBuildRoster_SelfFirstAndIndexOrder(t *testing.T) {
	testsupport.Isolate(t)
	holdLock(t, "perky-same-chevy")
	freeLock(t, "older-oak-hen")
	ended := time.Now()
	rows := BuildRoster([]sessions.Entry{
		{HarpName: "older-oak-hen", Backend: "claude-code", EndedAt: &ended},
		{HarpName: "perky-same-chevy", Backend: "claude-code"},
	}, nil, "perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "perky-same-chevy", rows[0].Harp, "the running session pins to the top")
	assert.Equal(t, "live", rows[0].State)
	assert.Equal(t, "older-oak-hen", rows[1].Harp)
	assert.Equal(t, "ended", rows[1].State)
}

func TestBuildRoster_ChildrenNestUnderParent(t *testing.T) {
	testsupport.Isolate(t)
	holdLock(t, "perky-same-chevy")
	rows := BuildRoster(
		[]sessions.Entry{
			{HarpName: "perky-same-chevy", Backend: "claude-code"},
			{HarpName: "unrelated-flat-owl", Backend: "claude-code"},
			{HarpName: "swift-elm-fox", Backend: "claude-code"},
		},
		[]coord.RosterEntry{
			{Harp: "swift-elm-fox", Agent: "developer", State: "executing", Parent: "perky-same-chevy"},
			{Harp: "deep-oak-hen", Agent: "finder", State: "ended", Parent: "perky-same-chevy"},
		},
		"perky-same-chevy")

	require.Len(t, rows, 4)
	assert.Equal(t, "perky-same-chevy", rows[0].Harp)
	assert.Equal(t, 0, rows[0].Depth)
	assert.Equal(t, "swift-elm-fox", rows[1].Harp, "children directly under their parent")
	assert.Equal(t, 1, rows[1].Depth)
	assert.Equal(t, "developer", rows[1].Agent, "bus roster enriches the index row")
	assert.Equal(t, "executing", rows[1].State, "the coordinator's state wins; its lock is never probed (there is none to find)")
	assert.Equal(t, "claude-code", rows[1].Engine, "index engine survives the merge")
	assert.Equal(t, "deep-oak-hen", rows[2].Harp, "bus-only child (no index entry) still shows")
	assert.Equal(t, 1, rows[2].Depth)
	assert.Equal(t, "unrelated-flat-owl", rows[3].Harp)
	assert.Equal(t, 0, rows[3].Depth)
}

// The merge is "the held row is RICHER", not "the held row replaces". A
// coordinator entry whose state is empty — a journal whose runState fact
// decoded without one — has nothing to say about liveness, so the lock is
// asked as if the row were not held; blanking the state would repaint a
// running session as the waiting glyph.
func TestBuildRoster_EmptyHeldFieldsDoNotBlankTheIndexRow(t *testing.T) {
	testsupport.Isolate(t)
	holdLock(t, "perky-same-chevy")
	freeLock(t, "older-oak-hen")
	ended := time.Now()
	rows := BuildRoster(
		[]sessions.Entry{
			{HarpName: "perky-same-chevy", Backend: "claude-code"},
			{HarpName: "older-oak-hen", Backend: "claude-code", EndedAt: &ended},
		},
		[]coord.RosterEntry{
			{Harp: "perky-same-chevy", Agent: "", State: "", Parent: ""},
			{Harp: "older-oak-hen", State: ""},
		},
		"perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "live", rows[0].State, "an empty held state defers to the lock")
	assert.Equal(t, "ended", rows[1].State)
	assert.Equal(t, "●", stateGlyph(rows[0].State))
}

func TestBuildRoster_OrphanChildPlacesFlat(t *testing.T) {
	rows := BuildRoster(nil, []coord.RosterEntry{
		{Harp: "lost-kid", Agent: "finder", State: "queued", Parent: "gone-parent"},
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
	rows := BuildRoster(nil, []coord.RosterEntry{
		{Harp: "root", State: "live"},
		{Harp: "kid-b", State: "executing", Parent: "root"},
		{Harp: "kid-a", State: "queued", Parent: "root"},
		{Harp: "grandkid", State: "queued", Parent: "kid-b"},
		{Harp: "other-root", State: "live"},
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
	rows := BuildRoster(nil, []coord.RosterEntry{
		{Harp: "a", State: "live", Parent: "b"},
		{Harp: "b", State: "live", Parent: "a"},
		{Harp: "c", State: "live"},
	}, "")

	require.Len(t, rows, 3, "no row is lost to the cycle")
	assert.Equal(t, "c", rows[0].Harp, "the real root places first")
	assert.Equal(t, []string{"a", "b"}, []string{rows[1].Harp, rows[2].Harp})
	assert.Equal(t, 0, rows[1].Depth)
	assert.Equal(t, 1, rows[2].Depth, "the cycle's second member still nests under the first")
}

// The state label is a CLAIM about now, and ended_at is a FACT about the
// past; the two disagree in both directions, so the label must come from the
// session lock alone. A crashed session never wrote ended_at, yet its lock
// is free.
func TestBuildRoster_CrashedSession_FreeLockAndNoEndedAt_ShowsEnded(t *testing.T) {
	testsupport.Isolate(t)
	freeLock(t, "crashed-oak-hen")

	rows := BuildRoster([]sessions.Entry{{HarpName: "crashed-oak-hen", Backend: "claude-code"}}, nil, "")

	require.Len(t, rows, 1)
	assert.Equal(t, "ended", rows[0].State, "a free lock is the end of the session, whether or not ended_at was written")
}

// A session resumed under its harp carries the earlier run's ended_at, but
// its lock is held again: it is running.
func TestBuildRoster_ResumedSession_HeldLockAndEndedAtSet_ShowsLive(t *testing.T) {
	testsupport.Isolate(t)
	holdLock(t, "resumed-oak-hen")
	ended := time.Now().Add(-time.Hour)

	rows := BuildRoster([]sessions.Entry{{HarpName: "resumed-oak-hen", Backend: "claude-code", EndedAt: &ended}}, nil, "")

	require.Len(t, rows, 1)
	assert.Equal(t, "live", rows[0].State, "a held lock is a running session, whatever ended_at says")
}

// No lock file (a session from before the lock existed, or whose Hold
// failed) is the third honest answer: the lock cannot say, so neither does
// the label — and ended_at does not get to fill the gap in either direction.
func TestBuildRoster_NoLockFile_ShowsUnknownWhateverEndedAtSays(t *testing.T) {
	testsupport.Isolate(t)
	ended := time.Now()

	rows := BuildRoster([]sessions.Entry{
		{HarpName: "unlocked-oak-hen", Backend: "claude-code"},
		{HarpName: "unlocked-elm-fox", Backend: "claude-code", EndedAt: &ended},
	}, nil, "")

	require.Len(t, rows, 2)
	assert.Equal(t, "unknown", rows[0].State, "no ended_at is not evidence of life")
	assert.Equal(t, "unknown", rows[1].State, "an ended_at is not evidence of death")
}

// BenchmarkBuildRoster_ProbesEveryIndexRow is the cost of the ruling that a
// roster render asks the lock once per index row, at the size of a real
// project index. The rows are a third each of held, free and never-locked,
// so all three probe paths are paid.
func BenchmarkBuildRoster_ProbesEveryIndexRow(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	const n = 650
	index := make([]sessions.Entry, 0, n)
	for i := range n {
		harp := fmt.Sprintf("bench-harp-%d", i)
		switch i % 3 {
		case 0:
			require.NoError(b, sessionlock.Hold(harp))
			b.Cleanup(func() { sessionlock.Release(harp) })
		case 1:
			require.NoError(b, sessionlock.Hold(harp))
			sessionlock.Release(harp)
		}
		index = append(index, sessions.Entry{HarpName: harp, Backend: "claude-code"})
	}
	b.ResetTimer()
	for range b.N {
		BuildRoster(index, nil, "")
	}
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
