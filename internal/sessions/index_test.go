package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	return m
}

func TestOpen_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "index.yaml")
	m, err := Open(path)
	require.NoError(t, err)
	idx, err := m.Load()
	require.NoError(t, err)
	assert.Empty(t, idx.Sessions, "fresh dir should yield empty index")
}

func TestAssignHarp_UniqueAcrossCalls(t *testing.T) {
	m := newManager(t)
	a, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	b, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	assert.NotEqual(t, a.HarpName, b.HarpName, "two AssignHarp calls must yield different names")

	idx, err := m.Load()
	require.NoError(t, err)
	require.Len(t, idx.Sessions, 2)
	assert.Equal(t, "/proj", idx.Sessions[0].ProjectDir)
	assert.Equal(t, "claude-code", idx.Sessions[0].Backend)
	assert.Empty(t, idx.Sessions[0].SessionID, "newly-assigned entry should have empty SessionID (pending)")
}

func TestBindSession_FillsPendingEntry(t *testing.T) {
	m := newManager(t)
	entry, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	require.NoError(t, m.BindSession(entry.HarpName, "session-uuid-123", "/tmp/transcript.jsonl"))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "session-uuid-123", found.SessionID)
	assert.Equal(t, "/tmp/transcript.jsonl", found.TranscriptPath)
}

func TestBindSession_Idempotent(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "uuid-1", "/t1"))
	// Re-binding with the same SessionID is a no-op (no error).
	require.NoError(t, m.BindSession(entry.HarpName, "uuid-1", "/t1"))
}

func harpNames(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.HarpName)
	}
	return names
}

func TestReconcile_DropsDeadEntriesAndPersists(t *testing.T) {
	m := newManager(t)
	a, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	b, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	c, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	dead := map[string]bool{b.HarpName: true}
	survivors, err := m.Reconcile(func(e Entry) bool { return dead[e.HarpName] })
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.HarpName, c.HarpName}, harpNames(survivors))

	// The drop is persisted: a fresh load no longer contains the dead harp.
	idx, err := m.Load()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.HarpName, c.HarpName}, harpNames(idx.Sessions))
}

func TestReconcile_NothingDeadIsANoop(t *testing.T) {
	m := newManager(t)
	a, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	survivors, err := m.Reconcile(func(Entry) bool { return false })
	require.NoError(t, err)
	require.Len(t, survivors, 1)
	assert.Equal(t, a.HarpName, survivors[0].HarpName)
}

// A new session ID that arrives WITH a transcript path is the engine rotating
// its transcript under a live process — claude-code's /clear starts a fresh
// UUID file and fires SessionStart again. The index has to follow it: pinned to
// the first bind, the entry goes on naming a file the session stopped growing
// hours ago, and recover/resume/watch all read that dead file and report
// success.
func TestBindSession_RotationRepointsTheBinding(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "first-id", "/orig"))

	require.NoError(t, m.BindSession(entry.HarpName, "second-id", "/new"))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "second-id", found.SessionID, "rotation re-points the session id")
	assert.Equal(t, "/new", found.TranscriptPath, "rotation re-points the transcript path")
	// The displaced binding is NOT discarded: commit af54e29f re-pointed the
	// binding but kept no history, so the pre-rotation vendor file (and
	// everything it recorded) became permanently unreachable the moment a
	// second BindSession call landed. Rotations is where it now survives.
	require.Len(t, found.Rotations, 1, "the displaced binding must be preserved in Rotations")
	assert.Equal(t, "first-id", found.Rotations[0].SessionID)
	assert.Equal(t, "/orig", found.Rotations[0].TranscriptPath)
	assert.False(t, found.Rotations[0].RotatedAt.IsZero(), "RotatedAt must be stamped")
}

// TestBindSession_MultipleRotationsAppendOldestFirst pins the ordering
// operations.RefreshVendorTranscript's lifetime rebuild depends on: Rotations
// accumulates one entry per displacement, oldest first, so concatenating
// segments in Rotations order reproduces the harp's conversation in the order
// it actually happened.
func TestBindSession_MultipleRotationsAppendOldestFirst(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "id-1", "/t1"))
	require.NoError(t, m.BindSession(entry.HarpName, "id-2", "/t2"))
	require.NoError(t, m.BindSession(entry.HarpName, "id-3", "/t3"))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "id-3", found.SessionID, "the current binding is the latest rotation")
	require.Len(t, found.Rotations, 2, "two displacements happened (id-1 and id-2)")
	assert.Equal(t, "id-1", found.Rotations[0].SessionID, "oldest rotation first")
	assert.Equal(t, "/t1", found.Rotations[0].TranscriptPath)
	assert.Equal(t, "id-2", found.Rotations[1].SessionID)
	assert.Equal(t, "/t2", found.Rotations[1].TranscriptPath)
}

// TestBindSession_IdempotentRebindNeverAppendsRotation pins that a repeat bind
// carrying the SAME session id (an idempotent hook re-run) never records a
// rotation — nothing was actually displaced, so a rotation entry here would
// fabricate lineage that never happened and would make a later rebuild
// convert the SAME vendor file twice.
func TestBindSession_IdempotentRebindNeverAppendsRotation(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "uuid-1", "/t1"))
	require.NoError(t, m.BindSession(entry.HarpName, "uuid-1", "/t1"))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Empty(t, found.Rotations, "an idempotent same-id rebind must never append a rotation")
}

// The counterweight to rotation: a binder that knows an ID but names no
// transcript file (the compactor's forward-bind backstop, and any future
// caller that forgets its own guard) must never displace a binding the
// SessionStart hook established. Without this, a stale ID can still overwrite
// a fresh one through a TOCTOU race between Find and BindSession.
func TestBindSession_IDOnlyBindNeverDisplacesALiveBinding(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "first-id", "/orig"))

	require.NoError(t, m.BindSession(entry.HarpName, "stale-id", ""))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "first-id", found.SessionID, "an id-only bind cannot re-point")
	assert.Equal(t, "/orig", found.TranscriptPath, "and cannot move the transcript path")
	assert.Empty(t, found.Rotations, "a bind that never displaces anything must never append a rotation")
}

func TestBindSession_UnknownHarpErrors(t *testing.T) {
	m := newManager(t)
	assert.Error(t, m.BindSession("nope-nope-nope", "uuid", ""))
}

// BindSession(harp, "", "") on a not-yet-bound entry used to
// acquire the file lock, load, assign SessionID = "" (no actual change),
// and perform a full index rewrite anyway — wasted I/O under lock for a
// call that changes nothing, on what should be the ordinary "the hook
// payload carried no session id" path (session_cmd.go's
// bindSessionFromPayload calls this for exactly that case whenever none of
// its three fallbacks find an id). Proven here by making the index's
// directory read-only: a real write attempt fails loud (the pre-fix
// behavior), while a genuine no-op must short-circuit before ever touching
// the filesystem lock/write path.
func TestBindSession_EmptyArgsIsANoOp_NoWriteAttempted(t *testing.T) {
	m := newManager(t)
	entry, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	dir := filepath.Dir(m.path)
	require.NoError(t, os.Chmod(dir, 0o555))
	defer func() { _ = os.Chmod(dir, 0o755) }()

	err = m.BindSession(entry.HarpName, "", "")
	assert.NoError(t, err, "empty sessionID/transcriptPath must be recognized as a no-op before any write is attempted, even when the index dir is unwritable")
}

func TestListForProject_FiltersAndSorts(t *testing.T) {
	m := newManager(t)
	a, _ := m.AssignHarp("/proj-a", "claude-code")
	time.Sleep(2 * time.Millisecond) // ensure StartedAt ordering
	b, _ := m.AssignHarp("/proj-a", "claude-code")
	_, _ = m.AssignHarp("/proj-b", "claude-code") // should be filtered out

	list, err := m.ListForProject("/proj-a")
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Most-recent first.
	assert.Equal(t, b.HarpName, list[0].HarpName)
	assert.Equal(t, a.HarpName, list[1].HarpName)
}

// TestListForProject_OrdersByLastActivityNotStartedAt pins the resume-picker
// ordering bug fix: a session that was CREATED earlier but has been RESUMED
// and WORKED since (newer transcript mtime) must rank above a session that
// was created more recently but never touched (StartedAt newer, but no
// activity beyond creation). Sorting on StartedAt alone (the old behavior)
// got this backwards.
func TestListForProject_OrdersByLastActivityNotStartedAt(t *testing.T) {
	testsupport.Isolate(t)
	m := newManager(t)
	dir := t.TempDir()

	workedTranscript := filepath.Join(dir, "worked.jsonl")
	require.NoError(t, os.WriteFile(workedTranscript, []byte("{}\n"), 0o644))

	now := time.Now().UTC().Truncate(time.Second)
	workedStarted := now.Add(-24 * time.Hour) // created a day ago...
	recentActivity := now.Add(-5 * time.Minute)
	require.NoError(t, os.Chtimes(workedTranscript, recentActivity, recentActivity)) // ...but worked 5 minutes ago

	untouchedStarted := now.Add(-1 * time.Hour) // created more recently, never opened since

	idx := &Index{Sessions: []Entry{
		{HarpName: "worked-session", ProjectDir: "/proj", StartedAt: workedStarted, TranscriptPath: workedTranscript},
		{HarpName: "untouched-session", ProjectDir: "/proj", StartedAt: untouchedStarted},
	}}
	require.NoError(t, m.saveLocked(idx))

	list, err := m.ListForProject("/proj")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "worked-session", list[0].HarpName,
		"resumed+worked session (newer transcript mtime) must rank above a newer-created but untouched session")
	assert.Equal(t, "untouched-session", list[1].HarpName)
}

// TestListForProject_FallsBackToStartedAt covers the two cases where no
// transcript-mtime signal is available: no transcript at all, and a
// transcript path that no longer stats (deleted/unreadable). Both must fall
// back to ordering by StartedAt rather than sorting a zero-value activity
// time to the bottom.
func TestListForProject_FallsBackToStartedAt(t *testing.T) {
	testsupport.Isolate(t)
	m := newManager(t)
	now := time.Now().UTC().Truncate(time.Second)

	idx := &Index{Sessions: []Entry{
		// No transcript at all.
		{HarpName: "newer-no-transcript", ProjectDir: "/proj", StartedAt: now},
		{HarpName: "older-no-transcript", ProjectDir: "/proj", StartedAt: now.Add(-time.Hour)},
		// A transcript path that doesn't exist on disk (stat fails).
		{HarpName: "newest-dangling-transcript", ProjectDir: "/proj", StartedAt: now.Add(time.Hour), TranscriptPath: "/nonexistent/gone.jsonl"},
	}}
	require.NoError(t, m.saveLocked(idx))

	list, err := m.ListForProject("/proj")
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "newest-dangling-transcript", list[0].HarpName)
	assert.Equal(t, "newer-no-transcript", list[1].HarpName)
	assert.Equal(t, "older-no-transcript", list[2].HarpName)
}

// TestListAll_SpansProjectsSortedByActivity pins the `session list --all`
// contract: ListAll returns every project's sessions (no project filter) and,
// like ListForProject, orders them by last-activity descending (transcript
// mtime, StartedAt fallback). The old --all path (operations.ListSessions →
// raw Reconcile) returned index order unsorted and unenriched; this locks the
// enriched+sorted behavior across project dirs. (Canonical-transcript mtime
// preference is a computed-on-read field that doesn't survive the disk
// round-trip, so it's pinned separately by TestActivityTime_Prefers…; here the
// activity signal is a persisted TranscriptPath.)
func TestListAll_SpansProjectsSortedByActivity(t *testing.T) {
	testsupport.Isolate(t)
	m := newManager(t)
	dir := t.TempDir()

	// proj-a: transcript worked 5 minutes ago.
	worked := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(worked, []byte("{}\n"), 0o644))
	recent := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	require.NoError(t, os.Chtimes(worked, recent, recent))

	now := time.Now().UTC().Truncate(time.Second)
	idx := &Index{Sessions: []Entry{
		// proj-b, created an hour ago, never touched since → activity = StartedAt
		// (an hour ago), which is OLDER than a-worked's 5-min-ago transcript.
		{HarpName: "b-untouched", ProjectDir: "/proj-b", StartedAt: now.Add(-time.Hour)},
		// proj-a, created a day ago but worked 5 min ago → transcript mtime wins,
		// so it must outrank the more-recently-created-but-untouched b-untouched.
		{HarpName: "a-worked", ProjectDir: "/proj-a", StartedAt: now.Add(-24 * time.Hour), TranscriptPath: worked},
		// proj-c, oldest, no activity signal.
		{HarpName: "c-old", ProjectDir: "/proj-c", StartedAt: now.Add(-48 * time.Hour)},
	}}
	require.NoError(t, m.saveLocked(idx))

	list, err := m.ListAll()
	require.NoError(t, err)
	require.Len(t, list, 3, "ListAll spans every project")
	assert.Equal(t, "a-worked", list[0].HarpName, "worked-5-min-ago (transcript mtime) ranks first across projects")
	assert.Equal(t, "b-untouched", list[1].HarpName)
	assert.Equal(t, "c-old", list[2].HarpName)
	assert.False(t, list[0].LastActivity.IsZero(), "ListAll enriches LastActivity like ListForProject")
}

// TestActivityTime_PrefersCanonicalTranscriptPath pins the ACP-resume
// ordering fix: an ACP/coordinator session records ONLY a canonical
// transcript (paths.HarpCanonicalTranscriptPath) and never a legacy
// TranscriptPath, so ActivityTime must read the canonical file's mtime for
// its last-activity signal. Statting only TranscriptPath (the old behavior)
// pinned every ACP session to StartedAt and mis-ranked it below stale legacy
// false-starts (an ordering bug). Mirrors
// SourceStale's canonical-first preference so ordering and staleness agree on
// which file is the source of truth.
func TestActivityTime_PrefersCanonicalTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().UTC().Add(-24 * time.Hour)

	canonicalPath := filepath.Join(dir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(canonicalPath, []byte("{}\n"), 0o644))
	activity := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	require.NoError(t, os.Chtimes(canonicalPath, activity, activity))

	// ACP session: only the canonical transcript, no legacy TranscriptPath.
	e := Entry{StartedAt: started, CanonicalTranscriptPath: canonicalPath}
	assert.True(t, ActivityTime(e).Equal(activity),
		"ActivityTime must use the canonical transcript mtime (%s), got %s", activity, ActivityTime(e))

	// When both exist, the canonical file wins (it is what the compactor
	// distills and what SourceStale fingerprints — same preference).
	legacyPath := filepath.Join(dir, "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("{}\n"), 0o644))
	legacyActivity := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(legacyPath, legacyActivity, legacyActivity))
	both := Entry{StartedAt: started, TranscriptPath: legacyPath, CanonicalTranscriptPath: canonicalPath}
	assert.True(t, ActivityTime(both).Equal(activity),
		"with both transcripts present, canonical mtime is preferred")

	// A canonical path that no longer stats degrades to the legacy transcript.
	dangling := Entry{StartedAt: started, TranscriptPath: legacyPath, CanonicalTranscriptPath: "/nonexistent/gone.acp.jsonl"}
	assert.True(t, ActivityTime(dangling).Equal(legacyActivity),
		"a dangling canonical path degrades to the legacy transcript mtime")
}

// TestFind_FillsCanonicalTranscript_FallsBackToLegacyFilename pins the
// transcript.acp.jsonl -> transcript.jsonl rename's back-compat contract at
// the sessions-index layer: a harp captured before the rename has ONLY the
// legacy filename under persist/, and Find (via fillCanonicalTranscript ->
// paths.ResolveHarpCanonicalTranscriptPath) must still resolve
// CanonicalTranscriptPath to it, not report the session as having no
// canonical transcript.
func TestFind_FillsCanonicalTranscript_FallsBackToLegacyFilename(t *testing.T) {
	testsupport.Isolate(t)
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	persistDir, err := paths.HarpPersistDir(e.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(persistDir, 0o755))
	legacyPath := filepath.Join(persistDir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("{}\n"), 0o644))

	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, legacyPath, found.CanonicalTranscriptPath,
		"a pre-rename session's canonical transcript must still resolve via the legacy filename fallback")
}

func TestMarkEnded(t *testing.T) {
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	now := time.Now()
	require.NoError(t, m.MarkEnded(e.HarpName, now))
	found, _ := m.Find(e.HarpName)
	require.NotNil(t, found.EndedAt)
	assert.WithinDuration(t, now.UTC(), *found.EndedAt, time.Second)
}

func TestSetSummary(t *testing.T) {
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.SetSummary(e.HarpName, "Designed bundle review on startup.", []string{"- ship the picker", "- write tests"}, 184320))
	found, _ := m.Find(e.HarpName)
	assert.Equal(t, "Designed bundle review on startup.", found.Summary)
	assert.Equal(t, []string{"- ship the picker", "- write tests"}, found.Detail)
	assert.Equal(t, int64(184320), found.SourceSize, "the source-size fingerprint must round-trip through the index")
}

func TestTranscriptStale(t *testing.T) {
	// A real file on disk lets the size comparison run; the staleness fingerprint
	// is the byte size stamped at distill time vs the live file.
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o644)) // 10 bytes

	t.Run("unknown when never stamped", func(t *testing.T) {
		stale, known := TranscriptStale(path, 0)
		assert.False(t, known, "a zero stamped size means staleness can't be determined")
		assert.False(t, stale)
	})
	t.Run("unknown when no transcript path", func(t *testing.T) {
		stale, known := TranscriptStale("", 10)
		assert.False(t, known)
		assert.False(t, stale)
	})
	t.Run("unknown when transcript missing", func(t *testing.T) {
		stale, known := TranscriptStale(filepath.Join(dir, "gone.jsonl"), 10)
		assert.False(t, known, "a stat failure degrades to can't-tell, never an error")
		assert.False(t, stale)
	})
	t.Run("current when size matches", func(t *testing.T) {
		stale, known := TranscriptStale(path, 10)
		assert.True(t, known)
		assert.False(t, stale, "size unchanged → essence still current")
	})
	t.Run("stale when transcript grew", func(t *testing.T) {
		stale, known := TranscriptStale(path, 4)
		assert.True(t, known)
		assert.True(t, stale, "live transcript is larger than the distilled slice → out of date")
	})
}

func TestEntry_SourceStale_DelegatesToFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o644)) // 11 bytes

	stale, known := Entry{TranscriptPath: path, SourceSize: 5}.SourceStale()
	assert.True(t, known)
	assert.True(t, stale)

	stale, known = Entry{TranscriptPath: path, SourceSize: 11}.SourceStale()
	assert.True(t, known)
	assert.False(t, stale)
}

// TestEntry_SourceStale_PrefersCanonicalTranscriptPath pins the
// S4 fix: once a harp has a captured canonical transcript, THAT is the file
// SourceSize was fingerprinted against (memory.transcriptSize's matching
// preference) — so staleness must compare against it, not the legacy
// TranscriptPath, or the badge would compare two unrelated files.
func TestEntry_SourceStale_PrefersCanonicalTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("0123456789"), 0o644)) // 10 bytes
	canonicalPath := filepath.Join(dir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(canonicalPath, []byte("0123456789012345678901234"), 0o644)) // 25 bytes

	e := Entry{TranscriptPath: legacyPath, CanonicalTranscriptPath: canonicalPath, SourceSize: 25}
	stale, known := e.SourceStale()
	assert.True(t, known)
	assert.False(t, stale, "stamped size (25) matches the CANONICAL file, so this must read as current")

	// The same stamped size against the legacy file's real size (10) would
	// read as stale — proving the two paths are genuinely different sources,
	// not a coincidence.
	legacyOnly := Entry{TranscriptPath: legacyPath, SourceSize: 25}
	stale, known = legacyOnly.SourceStale()
	assert.True(t, known)
	assert.True(t, stale, "sanity check: the legacy file alone does NOT match a 25-byte stamp")
}

func TestLoad_ToleratesLegacyPythonTimestamps(t *testing.T) {
	// Earlier (Python) ctxloom builds wrote timestamps as
	// datetime.isoformat(sep=' '): space separator, microseconds, "+00:00"
	// offset. Go's RFC3339-only YAML time decoder rejects them, which used to
	// fail the whole load (and block `ctxloom run` resume). Loading must now
	// parse them into normalized time.Time values.
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const legacy = `sessions:
- harp_name: generous-trustless-waltz
  session_id: a295d415-f1cf-413e-9cfc-5ba4dc425e48
  backend: claude-code
  project_dir: /home/u/proj
  started_at: 2026-05-28 16:57:20.781317+00:00
  ended_at: 2026-05-28 19:21:32.662574+00:00
`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	idx, err := m.Load()
	require.NoError(t, err)
	require.Len(t, idx.Sessions, 1)

	e := idx.Sessions[0]
	assert.Equal(t, "generous-trustless-waltz", e.HarpName)
	wantStart := time.Date(2026, 5, 28, 16, 57, 20, 781317000, time.UTC)
	assert.True(t, e.StartedAt.Equal(wantStart), "started_at: got %s want %s", e.StartedAt, wantStart)
	require.NotNil(t, e.EndedAt)
	wantEnd := time.Date(2026, 5, 28, 19, 21, 32, 662574000, time.UTC)
	assert.True(t, e.EndedAt.Equal(wantEnd), "ended_at: got %s want %s", *e.EndedAt, wantEnd)

	// Load is non-destructive: the file is untouched, with the upgrade staged.
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(onDisk), "Load must not rewrite the index")
	require.NotNil(t, m.PendingUpgrade(), "legacy index should record a pending upgrade")
	assert.Equal(t, path, m.PendingUpgrade().Path)
}

func TestCommitUpgrade_NormalizesAndClearsPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const legacy = `sessions:
- harp_name: generous-trustless-waltz
  backend: claude-code
  project_dir: /home/u/proj
  started_at: 2026-05-28 16:57:20.781317+00:00
`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	_, err = m.Load() // stages the pending upgrade
	require.NoError(t, err)
	require.NotNil(t, m.PendingUpgrade())

	require.NoError(t, m.CommitUpgrade())
	assert.Nil(t, m.PendingUpgrade(), "commit clears pending")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "started_at: 2026-05-28T16:57:20.781317Z",
		"committed index uses canonical RFC3339Nano")
	assert.NotContains(t, string(got), "781317+00:00", "legacy format gone after commit")
}

// TestCommitUpgrade_DoesNotClobberConcurrentWrite pins the commit-time
// re-stage: between Load (which stages the upgrade) and the user's consent,
// the spawned backend's MCP BindSession may rewrite the index. Committing the
// staged snapshot would silently drop that write — commit must re-read under
// the lock and, finding canonical bytes, leave them alone.
func TestCommitUpgrade_DoesNotClobberConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const legacy = `sessions:
- harp_name: generous-trustless-waltz
  backend: claude-code
  project_dir: /home/u/proj
  started_at: 2026-05-28 16:57:20.781317+00:00
`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	_, err = m.Load() // stages the pending upgrade
	require.NoError(t, err)
	require.NotNil(t, m.PendingUpgrade())

	// A concurrent writer (canonical form, like saveLocked) lands a session
	// bind during the prompt window.
	const concurrent = `sessions:
    - harp_name: generous-trustless-waltz
      session_id: bound-by-mcp
      backend: claude-code
      project_dir: /home/u/proj
      started_at: 2026-05-28T16:57:20.781317Z
`
	require.NoError(t, os.WriteFile(path, []byte(concurrent), 0o644))

	require.NoError(t, m.CommitUpgrade())
	assert.Nil(t, m.PendingUpgrade())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "bound-by-mcp",
		"the concurrent session bind must survive the upgrade commit")
}

func TestLoad_CurrentIndexHasNoPendingUpgrade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const current = `sessions:
- harp_name: generous-trustless-waltz
  backend: claude-code
  project_dir: /home/u/proj
  started_at: 2026-05-28T16:57:20.781317Z
`
	require.NoError(t, os.WriteFile(path, []byte(current), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	_, err = m.Load()
	require.NoError(t, err)
	assert.Nil(t, m.PendingUpgrade(), "a canonical index must not record a pending upgrade")
}

func TestSave_NormalizesLegacyTimestampsToRFC3339(t *testing.T) {
	// A load+save round-trip over a legacy index self-heals the on-disk
	// timestamp format to canonical RFC3339Nano.
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const legacy = `sessions:
- harp_name: generous-trustless-waltz
  backend: claude-code
  project_dir: /home/u/proj
  started_at: 2026-05-28 16:57:20.781317+00:00
`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	// AssignHarp loads and saves the whole index, rewriting every entry.
	_, err = m.AssignHarp("/home/u/proj", "claude-code")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "started_at: 2026-05-28T16:57:20.781317Z",
		"legacy timestamp should be rewritten as RFC3339Nano")
	assert.NotContains(t, got, "781317+00:00", "Python isoformat should be gone after save")
}

func TestLoad_UnparseableTimestampDoesNotBlockLoad(t *testing.T) {
	// Fault tolerance: one unrecognized timestamp must not fail the whole load,
	// and it must degrade to ~now (recent), NOT the zero time — zero sorts the
	// session below the picker's day-horizon and hides it, the opposite of what
	// graceful degradation should do.
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	const bad = `sessions:
- harp_name: busted
  project_dir: /home/u/proj
  started_at: not-a-timestamp-at-all
`
	require.NoError(t, os.WriteFile(path, []byte(bad), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	idx, err := m.Load()
	require.NoError(t, err, "unparseable timestamp must not fail the load")
	require.Len(t, idx.Sessions, 1)
	assert.Equal(t, "busted", idx.Sessions[0].HarpName)
	got := idx.Sessions[0].StartedAt
	assert.False(t, got.IsZero(), "bad timestamp must not degrade to zero time (would hide the session)")
	assert.WithinDuration(t, time.Now(), got, time.Minute, "bad timestamp degrades to ~now, keeping the session visible")
}

func TestEntry_TimestampRoundTrip(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.MarkEnded(e.HarpName, time.Now()))

	reloaded, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.WithinDuration(t, e.StartedAt, reloaded.StartedAt, time.Second)
	require.NotNil(t, reloaded.EndedAt)
}

func TestFind_MissingReturnsNilNoError(t *testing.T) {
	m := newManager(t)
	found, err := m.Find("never-existed")
	require.NoError(t, err)
	assert.Nil(t, found)
}

func TestRename(t *testing.T) {
	m := newManager(t)
	e, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.Rename(e.HarpName, "shiny-new-name"))

	// Old name no longer present.
	old, _ := m.Find(e.HarpName)
	assert.Nil(t, old)
	// New name carries the original SessionID slot (empty here) and project.
	nu, _ := m.Find("shiny-new-name")
	require.NotNil(t, nu)
	assert.Equal(t, "/proj", nu.ProjectDir)
}

func TestRename_CollisionErrors(t *testing.T) {
	m := newManager(t)
	a, _ := m.AssignHarp("/proj", "claude-code")
	b, _ := m.AssignHarp("/proj", "claude-code")
	assert.Error(t, m.Rename(a.HarpName, b.HarpName))
}

func TestRename_UnknownSourceErrors(t *testing.T) {
	m := newManager(t)
	assert.Error(t, m.Rename("does-not-exist", "shiny"))
}

func TestForget(t *testing.T) {
	m := newManager(t)
	a, _ := m.AssignHarp("/proj", "claude-code")
	b, _ := m.AssignHarp("/proj", "claude-code")

	require.NoError(t, m.Forget(a.HarpName))
	assert.Error(t, m.Forget(a.HarpName), "second forget should error since entry is gone")

	idx, err := m.Load()
	require.NoError(t, err)
	require.Len(t, idx.Sessions, 1)
	assert.Equal(t, b.HarpName, idx.Sessions[0].HarpName)
}

// TestRename_RefusesUnsafeNewName pins a safe-rename rule at the index: the new name
// becomes both an index KEY and a filesystem path component, and Rename is
// the only place a harp name arrives from user argv. Asserted on the index
// state, not the error text — a refused rename must leave the original entry
// findable and mint no entry under the traversing name.
func TestRename_RefusesUnsafeNewName(t *testing.T) {
	for _, bad := range []string{"..", "../..", "../../etc/passwd", "a/b", `a\b`, " lead", "nul\x00name"} {
		m := newManager(t)
		e, _ := m.AssignHarp("/proj", "claude-code")

		err := m.Rename(e.HarpName, bad)
		assert.Error(t, err, "Rename to %q must be refused", bad)

		still, ferr := m.Find(e.HarpName)
		require.NoError(t, ferr)
		require.NotNil(t, still, "a refused rename must leave %q in the index", e.HarpName)
		assert.Equal(t, "/proj", still.ProjectDir)

		got, ferr := m.Find(bad)
		require.NoError(t, ferr)
		assert.Nil(t, got, "a refused rename must mint no entry under %q", bad)
	}
}

// TestFindBySessionID_ResolvesCurrentBinding pins the ordinary case: a
// session id that is still the entry's live binding resolves directly.
func TestFindBySessionID_ResolvesCurrentBinding(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "current-id", "/t1"))

	found, err := m.FindBySessionID("current-id")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, entry.HarpName, found.HarpName)
}

// TestFindBySessionID_ResolvesRotatedAwayID pins the lineage lookup: a
// session id that a /clear rotated PAST (no longer the entry's SessionID, but
// still recorded in Rotations) must still resolve to the owning harp. Without
// scanning Rotations, this degrades to the pre-fix behavior — a stale hook
// payload or an old vendor transcript naming a superseded session id has no
// harp to attribute itself to at all.
func TestFindBySessionID_ResolvesRotatedAwayID(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "pre-clear-id", "/pre-clear.jsonl"))
	require.NoError(t, m.BindSession(entry.HarpName, "post-clear-id", "/post-clear.jsonl"))

	found, err := m.FindBySessionID("pre-clear-id")
	require.NoError(t, err)
	require.NotNil(t, found, "a rotated-away session id must still resolve to its harp")
	assert.Equal(t, entry.HarpName, found.HarpName)
	assert.Equal(t, "post-clear-id", found.SessionID, "the returned entry is the CURRENT entry, not a reconstructed historical one")
}

// TestFindBySessionID_UnknownIDReturnsNil pins the negative case: an id that
// names neither a current binding nor any recorded rotation resolves to
// nothing, without error.
func TestFindBySessionID_UnknownIDReturnsNil(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "some-id", "/t1"))

	found, err := m.FindBySessionID("no-such-id")
	require.NoError(t, err)
	assert.Nil(t, found)

	found, err = m.FindBySessionID("")
	require.NoError(t, err)
	assert.Nil(t, found, "an empty id must resolve to nothing, not the first entry")
}

// TestAppendRotations_InsertsBeforeExistingByRotatedAt pins the case
// `session adopt` exists for: an orphaned vendor transcript that predates
// EVERY rotation already on record (the ordinary shape for a pre-lineage-fix
// /clear, task escapable-vanity's e4cd4c8a case) must land FIRST in
// Rotations, not appended after the newest one — the harp-lifetime rebuild
// (operations.convertVendorTranscript) concatenates Rotations in array
// order, so an out-of-order slice would splice this segment into the
// conversation at the wrong point.
func TestAppendRotations_InsertsBeforeExistingByRotatedAt(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "id-2", "/t2"))
	require.NoError(t, m.BindSession(entry.HarpName, "id-3", "/t3"))
	// After the two rebinds: Rotations = [id-2], current = id-3.

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, m.AppendRotations(entry.HarpName, []Rotation{
		{SessionID: "id-1", TranscriptPath: "/t1", RotatedAt: older},
	}))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Len(t, found.Rotations, 2)
	assert.Equal(t, "id-1", found.Rotations[0].SessionID, "the adopted, chronologically-earliest rotation must sort first")
	assert.Equal(t, "id-2", found.Rotations[1].SessionID)
	assert.Equal(t, "id-3", found.SessionID, "the live binding is untouched")
}

// TestAppendRotations_SkipsSessionIDAlreadyInLineage pins the dedup guard: a
// caller that re-runs adopt over a harp it already touched (or races a live
// rebind) must not duplicate a rotation whose SessionID is already the
// current binding or already recorded.
func TestAppendRotations_SkipsSessionIDAlreadyInLineage(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "id-1", "/t1"))
	require.NoError(t, m.BindSession(entry.HarpName, "id-2", "/t2"))
	// Rotations = [id-1], current = id-2.

	require.NoError(t, m.AppendRotations(entry.HarpName, []Rotation{
		{SessionID: "id-1", TranscriptPath: "/t1", RotatedAt: time.Now().UTC()}, // already in Rotations
		{SessionID: "id-2", TranscriptPath: "/t2", RotatedAt: time.Now().UTC()}, // the current binding
		{SessionID: "id-3", TranscriptPath: "/t3", RotatedAt: time.Now().UTC()}, // genuinely new
	}))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Len(t, found.Rotations, 2, "only the genuinely new rotation is appended")
	ids := []string{found.Rotations[0].SessionID, found.Rotations[1].SessionID}
	assert.ElementsMatch(t, []string{"id-1", "id-3"}, ids)
}

// TestAppendRotations_UnknownHarpErrors mirrors BindSession's unknown-harp
// behavior: a caller naming a harp the index has never heard of gets a clear
// error, not a silently-created row.
func TestAppendRotations_UnknownHarpErrors(t *testing.T) {
	m := newManager(t)
	err := m.AppendRotations("no-such-harp", []Rotation{{SessionID: "x", RotatedAt: time.Now()}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// TestAppendRotations_EmptyIsANoOp mirrors BindSession's empty-args
// short-circuit: an empty rotations slice must never acquire the file lock
// or touch the on-disk index, so a caller that computed nothing to adopt
// (every candidate skipped) costs nothing to call through.
func TestAppendRotations_EmptyIsANoOp(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.AppendRotations(entry.HarpName, nil))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Empty(t, found.Rotations)
}

// TestAppendRotations_SurvivesManagerReload pins that the append is a real
// persisted write, not an in-memory-only mutation: a fresh Manager opened
// over the same path must see it.
func TestAppendRotations_SurvivesManagerReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	m1, err := Open(path)
	require.NoError(t, err)
	entry, err := m1.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m1.BindSession(entry.HarpName, "id-2", "/t2"))

	require.NoError(t, m1.AppendRotations(entry.HarpName, []Rotation{
		{SessionID: "id-1", TranscriptPath: "/t1", RotatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}))

	m2, err := Open(path)
	require.NoError(t, err)
	found, err := m2.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Len(t, found.Rotations, 1)
	assert.Equal(t, "id-1", found.Rotations[0].SessionID)
	assert.Equal(t, "/t1", found.Rotations[0].TranscriptPath)
}
