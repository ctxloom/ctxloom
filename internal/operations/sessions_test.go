package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
		// ctxloom's own capture is a full fallback. This is the predicate
		// half of the fix; TestListSessions_KeepsSessionWithOnlyACanonicalTranscript
		// covers the Reconcile half that makes the field non-empty in the
		// first place.
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

// TestListSessions_KeepsSessionWithOnlyACanonicalTranscript is an
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

// TestListSessions_PurgedSessionSurvivesReconcile is the mutation target for
// isUnrecoverable's PurgedAt guard (j001300 close-out area 2's must-fix,
// docs/design/j001300-closeout-surfaces.design.md §4.5). Without that guard —
// checked FIRST, ahead of the Summary/Detail/CanonicalTranscriptPath checks —
// a session `ctxloom session purge` destroyed the transcript of, and that was
// never distilled (no Summary, no Detail, no CanonicalTranscriptPath), is
// judged unrecoverable by every OTHER branch of the predicate and silently
// dropped by the very next listing: the exact "vanishes from the index,
// indistinguishable from one that never existed" outcome PurgedAt exists to
// prevent.
//
// This exercises the real end-to-end path (a real *Manager over a real
// index.yaml, not isUnrecoverable called directly with a hand-built Entry) so
// a defect anywhere in the wiring — MarkPurged not persisting, Reconcile not
// seeing the persisted field — would be caught here, the same reasoning
// TestListSessions_KeepsSessionWithOnlyACanonicalTranscript documents for its
// sibling fix.
//
// MUTATION PROOF: deleting the `if e.PurgedAt != nil { return false }` guard
// from isUnrecoverable (internal/operations/sessions.go) turns this red —
// ListSessions() no longer contains the harp, and the reap persists (the
// on-disk index.yaml loses the row too). Restoring the guard turns it green
// again. Verified by hand while writing this test (see the agent's report).
func TestListSessions_PurgedSessionSurvivesReconcile(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	e, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	// A real, now-deleted transcript: exactly what `session purge` leaves
	// behind. No Summary, no Detail, no CanonicalTranscriptPath — this entry
	// was NEVER distilled, so every other recoverability branch reads false.
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o644))
	require.NoError(t, mgr.BindSession(e.HarpName, "sess-1", transcript))
	now := time.Now()
	require.NoError(t, mgr.MarkEnded(e.HarpName, now))

	// Mark-before-destroy, exactly as PurgeSession orders it.
	require.NoError(t, mgr.MarkPurged(e.HarpName, now))
	require.NoError(t, os.Remove(transcript))

	got, err := ListSessions()
	require.NoError(t, err)
	var names []string
	for _, s := range got {
		names = append(names, s.HarpName)
	}
	assert.Contains(t, names, e.HarpName,
		"a purged session's row must survive reconciliation even though its transcript is gone and it was never distilled")

	// The reap Reconcile performs is a saveLocked — persisted, not just
	// returned — so the on-disk index must agree.
	idx, err := mgr.Load()
	require.NoError(t, err)
	var onDisk []string
	var purgedAt *sessions.Entry
	for i := range idx.Sessions {
		onDisk = append(onDisk, idx.Sessions[i].HarpName)
		if idx.Sessions[i].HarpName == e.HarpName {
			purgedAt = &idx.Sessions[i]
		}
	}
	assert.Contains(t, onDisk, e.HarpName, "the index on disk must still hold the purged entry")
	if assert.NotNil(t, purgedAt, "the purged entry must still be findable in the on-disk index") {
		assert.NotNil(t, purgedAt.PurgedAt, "PurgedAt itself must have persisted, not just survived in memory")
	}
}

// TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently:
// BindSession used to discard mgr.Find's error entirely, so a
// transient index-read failure (a malformed on-disk index.yaml, here standing
// in for any read/parse fault) was indistinguishable from "no entry for this
// harp" — both took the same silent no-op. First-bind-wins never retries, so
// a harp that misses its bind this way never gets a session id again. The
// SessionStart hook must still never fail the host backend (CLAUDE.md fault
// tolerance), so the fix is a warning, not a returned error.
func TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently(t *testing.T) {
	home := testsupport.Isolate(t)

	indexPath := filepath.Join(home, ".ctxloom", "sessions", "index.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(indexPath), 0o755))
	// Malformed YAML (unterminated quote) makes loadLocked's yaml.Unmarshal
	// fail, so mgr.Find returns a genuine parse error, not "absent".
	require.NoError(t, os.WriteFile(indexPath, []byte(`sessions: ["unterminated`), 0o644))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	err := BindSession("some-harp", "sess-1", "/tmp/transcript.jsonl")
	require.NoError(t, err, "the SessionStart hook must never fail the host backend")
	assert.Contains(t, buf.String(), "some-harp", "the failure must be warned, naming the harp")
	assert.Contains(t, buf.String(), "session index", "the warning must say what failed")
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
	home := testsupport.Isolate(t)

	indexPath := filepath.Join(home, ".ctxloom", "sessions", "index.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(indexPath), 0o755))
	// A VALID index that simply does not mention the harp being bound — so
	// mgr.Find returns (nil, nil), the branch under test, rather than an error.
	require.NoError(t, os.WriteFile(indexPath, []byte("sessions: []\n"), 0o644))

	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.NotEmpty(t, before, "a zero-length fixture would make the comparison below vacuous")

	require.NoError(t, BindSession("absent-harp", "sess-1", "/tmp/transcript.jsonl"),
		"the SessionStart hook must never fail the host backend")

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"binding a harp with no index entry must write nothing at all")
	assert.NotContains(t, string(after), "absent-harp",
		"no entry may be minted for a harp the index never knew")
	assert.NotContains(t, string(after), "sess-1",
		"and no session id may be recorded against one")
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

	mgr, err := sessions.Open("")
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
// regenerate, and it must outlive the vendor transcript it happened to be
// written beside. Measured on a real box: 22 indexed harps carried plans with no
// transcript and were one reap away, because the predicate judged recoverability
// on the log alone.
//
// Every arm redirects HOME to a temp dir. paths.HomeSessionsDir resolves through
// os.UserHomeDir, so this exercises the real path-building code while never
// reading or writing the user's actual ~/.ctxloom.
func TestIsUnrecoverable_AuthoredPersistContentOutlivesAMissingTranscript(t *testing.T) {
	seed := func(t *testing.T, harp string, persistFiles ...string) string {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		persist := filepath.Join(home, ".ctxloom", "sessions", harp, "persist")
		require.NoError(t, os.MkdirAll(persist, 0o755))
		for _, name := range persistFiles {
			require.NoError(t, os.WriteFile(filepath.Join(persist, name), []byte("x\n"), 0o600))
		}
		// A transcript path that is BOUND but GONE — the only shape that reaches
		// the new check, since an unbound entry is already treated as pending.
		return filepath.Join(home, "vendor-transcript-that-was-pruned.jsonl")
	}

	t.Run("a plan keeps the session", func(t *testing.T) {
		missing := seed(t, "tidy-plump-reeds", "v1-removal.plan.md")
		assert.False(t, isUnrecoverable(sessions.Entry{HarpName: "tidy-plump-reeds", TranscriptPath: missing}),
			"a session whose transcript the vendor pruned still holds a plan nobody can regenerate; reaping it to reclaim a replaceable log is the loss this predicate exists to prevent")
	})

	t.Run("a report keeps the session", func(t *testing.T) {
		missing := seed(t, "brave-lucky-stone", "overnight.report.md")
		assert.False(t, isUnrecoverable(sessions.Entry{HarpName: "brave-lucky-stone", TranscriptPath: missing}))
	})

	t.Run("ctxloom's own machine files do NOT keep it", func(t *testing.T) {
		missing := seed(t, "muddy-plain-cider", "transcript.jsonl", "diagnostics.log", "context-metrics.jsonl")
		assert.True(t, isUnrecoverable(sessions.Entry{HarpName: "muddy-plain-cider", TranscriptPath: missing}),
			"persist/ is never empty for a session that merely RAN — keeping a row on machine-written logs alone would turn pruning off rather than making it correct, and the ruling is explicit that pruning stays")
	})

	t.Run("an empty persist dir does NOT keep it", func(t *testing.T) {
		missing := seed(t, "silly-grand-manor")
		assert.True(t, isUnrecoverable(sessions.Entry{HarpName: "silly-grand-manor", TranscriptPath: missing}))
	})

	t.Run("a purged row is never resurrected by its leftovers", func(t *testing.T) {
		missing := seed(t, "royal-eager-vault", "v1-removal.plan.md")
		purged := time.Now()
		assert.False(t, isUnrecoverable(sessions.Entry{HarpName: "royal-eager-vault", TranscriptPath: missing, PurgedAt: &purged}),
			"purge is a deliberate destruction and the row IS the record; the plan check must not be what decides this")
	})
}
