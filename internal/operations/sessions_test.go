package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectPreviousEntry(t *testing.T) {
	// Entries arrive most-recent-first; the active harp ("self") is index 0.
	entries := []sessions.Entry{
		{HarpName: "self", SessionID: "s-self", Backend: "claude-code"},
		{HarpName: "prev", SessionID: "s-prev", Backend: "antigravity"},
		{HarpName: "old", SessionID: "s-old", Backend: "claude-code"},
	}

	t.Run("skips active harp, returns most-recent prior with its backend", func(t *testing.T) {
		ref := selectPreviousEntry(entries, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "s-prev", ref.SessionID)
		assert.Equal(t, "antigravity", ref.Backend, "agent-of-origin must come through for cross-agent handoff")
	})

	t.Run("skips entries not yet bound to a session id", func(t *testing.T) {
		ref := selectPreviousEntry([]sessions.Entry{
			{HarpName: "self", SessionID: "s-self"},
			{HarpName: "pending", SessionID: ""}, // bound harp, no session yet
			{HarpName: "prev", SessionID: "s-prev", Backend: "claude-code"},
		}, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "s-prev", ref.SessionID)
	})

	t.Run("nil when only the active harp exists", func(t *testing.T) {
		ref := selectPreviousEntry([]sessions.Entry{
			{HarpName: "self", SessionID: "s-self"},
		}, "self")
		assert.Nil(t, ref)
	})

	t.Run("nil for empty index", func(t *testing.T) {
		assert.Nil(t, selectPreviousEntry(nil, "self"))
	})

	t.Run("unknown active harp still returns most-recent bound prior", func(t *testing.T) {
		ref := selectPreviousEntry(entries, "")
		require.NotNil(t, ref)
		assert.Equal(t, "s-self", ref.SessionID, "no active harp to skip → newest bound entry")
	})
}
