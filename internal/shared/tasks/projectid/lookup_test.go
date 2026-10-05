package projectid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// registryBytes is the registry file's content, "" when it does not exist,
// so a test can assert a Lookup left it exactly as it found it.
func registryBytes(t *testing.T, m *Manager) string {
	t.Helper()
	b, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

// Lookup answers what Resolve would answer for every case in which Resolve
// keeps an existing identity, and "" (no project yet) for every case in
// which Resolve would mint one — writing nothing either way.
func TestLookup_UnknownTreeIsNoProjectYetAndWritesNothing(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	id, err := m.Lookup(dir)
	require.NoError(t, err)
	require.Empty(t, id)
	require.NoDirExists(t, filepath.Dir(m.path), "a lookup must not create the registry")
	require.NoFileExists(t, markerPathOf(t, dir), "a lookup must not write a marker")
}

func TestLookup_RegisteredPathResolvesWithoutHealingAMissingMarker(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	want := mustResolve(t, m, dir).ProjectID
	require.NoError(t, os.Remove(markerPathOf(t, dir)))
	before := registryBytes(t, m)

	id, err := m.Lookup(dir)
	require.NoError(t, err)
	require.Equal(t, want, id)
	require.NoFileExists(t, markerPathOf(t, dir), "the marker self-heal is a write; a lookup does not perform it")
	require.Equal(t, before, registryBytes(t, m))
}

func TestLookup_MarkerUnknownToTheRegistryIsThatIdentityWithoutAdopting(t *testing.T) {
	src := newManager(t)
	dir := t.TempDir()
	want := mustResolve(t, src, dir).ProjectID

	fresh := newManager(t) // a lost registry / fresh machine
	id, err := fresh.Lookup(dir)
	require.NoError(t, err)
	require.Equal(t, want, id)
	require.NoDirExists(t, filepath.Dir(fresh.path), "Adopt is a write; a lookup does not perform it")
}

func TestLookup_LiveCopyIsNoProjectYetAndTheFirstWriteForks(t *testing.T) {
	m := newManager(t)
	orig := t.TempDir()
	origID := mustResolve(t, m, orig).ProjectID
	cp := t.TempDir()
	require.NoError(t, WriteMarker(cp, origID)) // the copy carries the original's marker
	before := registryBytes(t, m)

	id, err := m.Lookup(cp)
	require.NoError(t, err)
	require.Empty(t, id, "a live copy must not read the original's tasks")
	require.Equal(t, before, registryBytes(t, m))
	require.Equal(t, origID, markerOf(t, cp), "a lookup must not fork the copy's marker")

	res := mustResolve(t, m, cp)
	require.Equal(t, ActionForked, res.Action, "the first write still forks exactly as before")
	require.NotEqual(t, origID, res.ProjectID)
}

func TestLookup_MovedTreeKeepsItsIdentityWithoutRepointing(t *testing.T) {
	m := newManager(t)
	parent := t.TempDir()
	oldDir := filepath.Join(parent, "old")
	require.NoError(t, os.Mkdir(oldDir, 0o755))
	want := mustResolve(t, m, oldDir).ProjectID
	newDir := filepath.Join(parent, "new")
	require.NoError(t, os.Rename(oldDir, newDir))
	before := registryBytes(t, m)

	id, err := m.Lookup(newDir)
	require.NoError(t, err)
	require.Equal(t, want, id)
	require.Equal(t, before, registryBytes(t, m), "Repoint is a write; a lookup does not perform it")
}
