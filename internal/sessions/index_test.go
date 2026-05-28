package sessions

import (
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
	require.NoError(t, m.SetSummary(e.HarpName, "Designed bundle review on startup."))
	found, _ := m.Find(e.HarpName)
	assert.Equal(t, "Designed bundle review on startup.", found.Summary)
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
