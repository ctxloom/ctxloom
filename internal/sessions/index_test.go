package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestBindSession_FirstBindWinsOnDifferentID(t *testing.T) {
	m := newManager(t)
	entry, _ := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, m.BindSession(entry.HarpName, "first-id", "/orig"))

	// Storage-layer defense-in-depth: a second bind with a different
	// SessionID must not clobber the first. Caller-side short-circuits
	// already guard against this, but a TOCTOU race between Find and
	// BindSession (or a future binder that forgets the check) would
	// otherwise let a stale ID overwrite a fresh one.
	require.NoError(t, m.BindSession(entry.HarpName, "second-id", "/new"))

	found, err := m.Find(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "first-id", found.SessionID, "first bind wins")
	assert.Equal(t, "/orig", found.TranscriptPath, "transcript path also pinned to first bind")
}

func TestBindSession_UnknownHarpErrors(t *testing.T) {
	m := newManager(t)
	assert.Error(t, m.BindSession("nope-nope-nope", "uuid", ""))
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
	require.NoError(t, m.SetSummary(e.HarpName, "Designed bundle review on startup.", []string{"- ship the picker", "- write tests"}))
	found, _ := m.Find(e.HarpName)
	assert.Equal(t, "Designed bundle review on startup.", found.Summary)
	assert.Equal(t, []string{"- ship the picker", "- write tests"}, found.Detail)
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
