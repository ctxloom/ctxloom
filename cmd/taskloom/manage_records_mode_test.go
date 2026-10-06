package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestManageInstall_KeepsTaskloomsRecordDirOwnerOnly: taskloom's application
// records live in their own subdirectory of the home records directory, and a
// record keeps the previous value of the key it undoes verbatim — so a
// subdirectory taskloom creates is owner-only. One an older binary left loose
// is guarded by its parent, the home records root every process establishes
// owner-only at startup (paths.EnsureHomeRoots).
func TestManageInstall_KeepsTaskloomsRecordDirOwnerOnly(t *testing.T) {
	fakeHome(t)
	dir, err := recordStoreDir()
	require.NoError(t, err)
	proj := t.TempDir()
	writeUserMCP(t, proj, userMCPJSON)

	require.NoError(t, manageInstall("claude-code", proj, false, false, &bytes.Buffer{}))

	require.NotEmpty(t, taskloomRecords(t), "the install must have written a record into the directory under test")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
