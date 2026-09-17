package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectPreviousEntry(t *testing.T) {
	// Entries arrive most-recent-first; the active harp ("self") is index 0.
	entries := []sessions.Entry{
		{HarpName: "self", SessionID: "s-self", Backend: "claude-code"},
		{HarpName: "prev", SessionID: "s-prev", Backend: "mock"},
		{HarpName: "old", SessionID: "s-old", Backend: "claude-code"},
	}

	t.Run("skips active harp, returns most-recent prior with its backend", func(t *testing.T) {
		ref := selectPreviousEntry(entries, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "s-prev", ref.SessionID)
		assert.Equal(t, "mock", ref.Backend, "agent-of-origin must come through for cross-agent handoff")
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
			{HarpName: "acp-prev", SessionID: "", Backend: "claude-code",
				CanonicalTranscriptPath: "/home/u/.ctxloom/sessions/acp-prev/transcript.jsonl"},
		}, "self")
		require.NotNil(t, ref)
		assert.Equal(t, "acp-prev", ref.Harp, "canonical entry must carry its harp for by-harp distillation")
		assert.Empty(t, ref.SessionID, "ACP entry has no backend session id")
		assert.Equal(t, "claude-code", ref.Backend)
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

// TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently:
// BindSession used to discard mgr.Find's error entirely, so a
// transient read failure (a malformed sidecar, here standing in for any
// read/parse fault) was indistinguishable from "no entry for this
// harp" — both took the same silent no-op. First-bind-wins never retries, so
// a harp that misses its bind this way never gets a session id again. The
// SessionStart hook must still never fail the host backend (CLAUDE.md fault
// tolerance), so the fix is a warning, not a returned error.
func TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently(t *testing.T) {
	testsupport.Isolate(t)

	sidecar, err := paths.HarpSidecarPath("some-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	// Malformed YAML (unterminated quote) makes the sidecar parse fail, so
	// mgr.Find returns a genuine parse error, not "absent".
	require.NoError(t, os.WriteFile(sidecar, []byte(`project_dir: ["unterminated`), 0o644))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	err = BindSession("some-harp", "sess-1", "/tmp/transcript.jsonl")
	require.NoError(t, err, "the SessionStart hook must never fail the host backend")
	assert.Contains(t, buf.String(), "some-harp", "the failure must be warned, naming the harp")
	assert.Contains(t, buf.String(), "session record", "the warning must say what failed")
}

// The sibling of the case above, and the reason both need a test: BindSession
// distinguishes "the index could not be read" (warn, above) from "this harp has
// no entry" (silent no-op, here), and the two used to be the same branch. A
// mutation run found this one NOT COVERED — no test executed the `entry == nil`
// guard at all, so nothing would have noticed it inverting and letting an
// unknown harp through to mgr.BindSession.
//
// Asserts the EFFECT rather than the nil error: a no-op that still wrote a
// binding would satisfy `require.NoError` perfectly well.
func TestBindSession_UnknownHarpWritesNothing(t *testing.T) {
	testsupport.Isolate(t)

	// A VALID store that simply does not hold the harp being bound — so
	// mgr.Find returns (nil, nil), the branch under test, rather than an error.
	mgr, err := sessions.Open()
	require.NoError(t, err)
	other, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	before, err := os.ReadDir(root)
	require.NoError(t, err)
	require.NotEmpty(t, before, "an empty fixture would make the comparison below vacuous")

	require.NoError(t, BindSession("absent-harp", "sess-1", "/tmp/transcript.jsonl"),
		"the SessionStart hook must never fail the host backend")

	after, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after),
		"binding a harp with no record must write nothing at all")
	assert.NoDirExists(t, filepath.Join(root, "absent-harp"),
		"no session may be minted for a harp the store never knew")
	got, err := mgr.Find(other.HarpName)
	require.NoError(t, err)
	assert.Empty(t, got.SessionID, "and no session id may be recorded against any other session")
}

// TestHarpForSession_ResolvesRotatedAwaySessionID pins the lineage lookup: a
// backend session id a /clear has since rotated PAST — no longer any entry's
// live SessionID, but preserved in that entry's Rotations by the store's
// displacing-rebind rule — must still resolve to the harp that owns it.
// HarpForSession's previous body (a linear scan matching only e.SessionID)
// could never see it: once BindSession re-pointed the entry to the
// post-clear id, the pre-clear id named no harp at all, and every caller that
// spells a session by its (now superseded) id — a stale hook payload, an old
// vendor transcript, recover_session's own id-resolution path — silently
// failed to attribute it.
func TestHarpForSession_ResolvesRotatedAwaySessionID(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "pre-clear-id", "/pre-clear.jsonl"))
	require.NoError(t, mgr.BindSession(entry.HarpName, "post-clear-id", "/post-clear.jsonl"))

	got, err := HarpForSession("pre-clear-id")
	require.NoError(t, err)
	assert.Equal(t, entry.HarpName, got, "a rotated-away session id must still resolve to its harp")

	got, err = HarpForSession("post-clear-id")
	require.NoError(t, err)
	assert.Equal(t, entry.HarpName, got, "the current binding must still resolve too")

	got, err = HarpForSession("never-bound-id")
	require.NoError(t, err)
	assert.Empty(t, got, "an id the index never saw resolves to nothing")
}

// A plan or report under persist/ is authored content that nothing can
