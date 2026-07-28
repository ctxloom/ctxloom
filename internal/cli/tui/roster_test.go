package tui

import (
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

func TestBuildRoster_OrphanChildPlacesFlat(t *testing.T) {
	rows := BuildRoster(nil, []coord.RosterEntry{
		{Harp: "lost-kid", Agent: "finder", State: "queued", Parent: "gone-parent"},
	}, "")
	require.Len(t, rows, 1)
	assert.Equal(t, 0, rows[0].Depth, "an orphan (parent unknown) shows at the root")
}

func TestStateGlyphs(t *testing.T) {
	assert.Equal(t, "●", stateGlyph("executing"))
	assert.Equal(t, "●", stateGlyph("live"))
	assert.Equal(t, "✓", stateGlyph("ended"))
	assert.Equal(t, "◐", stateGlyph("queued"))
	assert.Equal(t, "◐", stateGlyph("parked"))
	assert.Equal(t, "◐", stateGlyph("idle"))
}
