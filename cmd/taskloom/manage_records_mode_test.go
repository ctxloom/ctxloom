package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestManageInstall_KeepsTaskloomsRecordDirOwnerOnly: taskloom's application
// records live in their own subdirectory of the home records directory, and a
// record keeps the previous value of the key it undoes verbatim — so that
// subdirectory is owner-only whether taskloom creates it or finds one an older
// binary left readable to everyone.
func TestManageInstall_KeepsTaskloomsRecordDirOwnerOnly(t *testing.T) {
	for name, existing := range map[string]bool{"created": false, "tightened": true} {
		t.Run(name, func(t *testing.T) {
			fakeHome(t)
			dir, err := recordStoreDir()
			require.NoError(t, err)
			if existing {
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.Chmod(dir, 0o755))
			}
			proj := t.TempDir()
			writeUserMCP(t, proj, userMCPJSON)

			require.NoError(t, manageInstall("claude-code", proj, false, false, &bytes.Buffer{}))

			require.NotEmpty(t, taskloomRecords(t), "the install must have written a record into the directory under test")
			info, err := os.Stat(dir)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		})
	}
}
