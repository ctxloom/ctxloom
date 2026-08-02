package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

func TestBuildRoster_SelfFirstAndIndexOrder(t *testing.T) {
	ended := time.Now()
	rows := BuildRoster([]sessions.Entry{
		{HarpName: "older-oak-hen", Backend: "codex", EndedAt: &ended},
		{HarpName: "perky-same-chevy", Backend: "claude-code"},
	}, nil, "perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "perky-same-chevy", rows[0].Harp, "the running session pins to the top")
	assert.Equal(t, "live", rows[0].State)
	assert.Equal(t, "older-oak-hen", rows[1].Harp)
	assert.Equal(t, "ended", rows[1].State)
}

func TestBuildRoster_ChildrenNestUnderParent(t *testing.T) {
	rows := BuildRoster(
		[]sessions.Entry{
			{HarpName: "perky-same-chevy", Backend: "claude-code"},
			{HarpName: "unrelated-flat-owl", Backend: "codex"},
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
	assert.Equal(t, "executing", rows[1].State, "bus state wins over index live/ended")
	assert.Equal(t, "claude-code", rows[1].Engine, "index engine survives the merge")
	assert.Equal(t, "deep-oak-hen", rows[2].Harp, "bus-only child (no index entry) still shows")
	assert.Equal(t, 1, rows[2].Depth)
	assert.Equal(t, "unrelated-flat-owl", rows[3].Harp)
	assert.Equal(t, 0, rows[3].Depth)
}

// The merge is "the held row is RICHER", not "the held row replaces". A
// coordinator entry whose state is empty — a journal whose runState fact
// decoded without one — must not erase the index's own live/ended reading,
// which would repaint a running session as the waiting glyph.
func TestBuildRoster_EmptyHeldFieldsDoNotBlankTheIndexRow(t *testing.T) {
	ended := time.Now()
	rows := BuildRoster(
		[]sessions.Entry{
			{HarpName: "perky-same-chevy", Backend: "claude-code"},
			{HarpName: "older-oak-hen", Backend: "codex", EndedAt: &ended},
		},
		[]coord.RosterEntry{
			{Harp: "perky-same-chevy", Agent: "", State: "", Parent: ""},
			{Harp: "older-oak-hen", State: ""},
		},
		"perky-same-chevy")

	require.Len(t, rows, 2)
	assert.Equal(t, "live", rows[0].State, "an empty held state leaves the index reading alone")
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

func TestStateGlyphs(t *testing.T) {
	assert.Equal(t, "●", stateGlyph("executing"))
	assert.Equal(t, "●", stateGlyph("live"))
	assert.Equal(t, "✓", stateGlyph("ended"))
	assert.Equal(t, "◐", stateGlyph("queued"))
	assert.Equal(t, "◐", stateGlyph("parked"))
	assert.Equal(t, "◐", stateGlyph("idle"))
}
