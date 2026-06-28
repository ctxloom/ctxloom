package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.WriteFile(present, []byte("{}"), 0o644))
	missing := filepath.Join(dir, "gone.jsonl")

	t.Run("bound transcript missing, not distilled -> unrecoverable", func(t *testing.T) {
		assert.True(t, isUnrecoverable(sessions.Entry{TranscriptPath: missing}))
	})

	t.Run("bound transcript present -> recoverable", func(t *testing.T) {
		assert.False(t, isUnrecoverable(sessions.Entry{TranscriptPath: present}))
	})

	t.Run("pending entry with no transcript -> recoverable (still in flight)", func(t *testing.T) {
		assert.False(t, isUnrecoverable(sessions.Entry{}))
	})

	t.Run("distilled with missing transcript -> recoverable (essence stays)", func(t *testing.T) {
		assert.False(t, isUnrecoverable(sessions.Entry{TranscriptPath: missing, Summary: "did things"}))
		assert.False(t, isUnrecoverable(sessions.Entry{TranscriptPath: missing, Detail: []string{"open item"}}))
	})

	t.Run("non-ENOENT stat error -> recoverable (transient hiccup, don't forget it)", func(t *testing.T) {
		// A path whose parent component is a regular file makes os.Stat fail
		// with ENOTDIR — a non-ENOENT error standing in for any transient I/O
		// failure (permission denied, network-mount hiccup). Only genuine
		// absence (ENOENT) may mark a bound transcript unrecoverable, so this
		// must stay recoverable rather than be silently forgotten.
		notDir := filepath.Join(present, "child.jsonl")
		assert.False(t, isUnrecoverable(sessions.Entry{TranscriptPath: notDir}))
	})
}

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
