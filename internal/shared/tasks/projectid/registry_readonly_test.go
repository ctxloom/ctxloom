package projectid

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Opening the registry and looking an identity up is not a write: taskloom's
// global listing asks isEstablishedProject before deciding a read's scope, and
// a directory created there would appear under HOME on a read-only command.
// The first mutation lays the directory out.
func TestOpen_LookupsLeaveTheRegistryDirUncreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects")
	m, err := Open(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)

	e, err := m.ResolveByPath("/proj")
	require.NoError(t, err)
	require.Nil(t, e)
	e, err = m.ResolveByID("vain-void-charm")
	require.NoError(t, err)
	require.Nil(t, e)
	all, err := m.EntriesAtPath("/proj")
	require.NoError(t, err)
	require.Empty(t, all)
	require.NoDirExists(t, dir, "a lookup through Open must not create the registry directory")

	minted, err := m.Mint("/proj")
	require.NoError(t, err)
	got, err := m.ResolveByID(minted.ProjectID)
	require.NoError(t, err)
	require.NotNil(t, got)
}
