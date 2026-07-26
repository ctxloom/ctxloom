package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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

	t.Run("canonical transcript present, vendor transcript pruned -> recoverable", func(t *testing.T) {
		// U087-F05: ctxloom's own capture is a full fallback. This is the
		// predicate half of the fix; TestListSessions_KeepsSessionWithOnly
		// ACanonicalTranscript covers the Reconcile half that makes the field
		// non-empty in the first place.
		assert.False(t, isUnrecoverable(sessions.Entry{
			TranscriptPath:          missing,
			CanonicalTranscriptPath: present,
		}))
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
			{HarpName: "pending", SessionID: ""}, // bound harp, no session AND no canonical yet
			{HarpName: "prev", SessionID: "s-prev", Backend: "claude-code"},
		}, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "s-prev", ref.SessionID)
	})

	t.Run("selects canonical-only (ACP) entry and carries its harp", func(t *testing.T) {
		// An ACP-launched session never binds a backend SessionID; its only
		// materialization key is the harp's own canonical transcript. Such an
		// entry must be selectable (not treated as still-pending), and the harp
		// must ride through so the caller can distill by harp.
		ref := selectPreviousEntry([]sessions.Entry{
			{HarpName: "self", SessionID: "s-self"},
			{HarpName: "acp-prev", SessionID: "", Backend: "opencode",
				CanonicalTranscriptPath: "/home/u/.ctxloom/sessions/acp-prev/transcript.jsonl"},
		}, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "acp-prev", ref.Harp, "canonical entry must carry its harp for by-harp distillation")
		assert.Empty(t, ref.SessionID, "ACP entry has no backend session id")
		assert.Equal(t, "opencode", ref.Backend)
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

// TestListSessions_KeepsSessionWithOnlyACanonicalTranscript is U087-F05's
// end-to-end red: a session whose VENDOR transcript was pruned but whose
// ctxloom-captured canonical transcript.jsonl survives must not be reaped.
// The reap is a saveLocked — irreversible — so this exercises the real
// ListSessions path against a real HOME rather than calling isUnrecoverable
// directly: the defect was structural (Reconcile judged RAW entries, and
// CanonicalTranscriptPath is computed-on-read), so a unit test on the
// predicate alone would have stayed green while the session still vanished.
func TestListSessions_KeepsSessionWithOnlyACanonicalTranscript(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	e, err := mgr.AssignHarp("/proj", "claude")
	require.NoError(t, err)

	// Vendor transcript: bound, then pruned by the vendor.
	vendor := filepath.Join(t.TempDir(), "vendor-transcript.jsonl")
	require.NoError(t, mgr.BindSession(e.HarpName, "sess-1", vendor))

	// ctxloom's OWN capture survives.
	canonical, err := paths.ResolveHarpCanonicalTranscriptPath(e.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonical), 0o755))
	require.NoError(t, os.WriteFile(canonical, []byte("{}\n"), 0o644))

	got, err := ListSessions()
	require.NoError(t, err)

	var names []string
	for _, s := range got {
		names = append(names, s.HarpName)
	}
	assert.Contains(t, names, e.HarpName,
		"a session with a surviving canonical transcript must not be reaped")

	// And the reap is persisted, so re-reading the index must agree.
	idx, err := mgr.Load()
	require.NoError(t, err)
	var onDisk []string
	for _, s := range idx.Sessions {
		onDisk = append(onDisk, s.HarpName)
	}
	assert.Contains(t, onDisk, e.HarpName, "the index on disk must still hold the entry")
}
