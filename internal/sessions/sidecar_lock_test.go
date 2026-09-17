package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

const lockTestHarp = "swift-amber-falcon"

// lockFilesIn lists the lock-file names sitting at the sessions root.
func lockFilesIn(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lock" {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestManager_EveryMutatingMethodLocksTheHarpsSidecarLock pins, at the PUBLIC
// seam, the agreement every sidecar mutator has to keep: it takes the harp's
// cooperative lock at paths.HarpSidecarLockPath and no other name. A method
// that agreed on the mechanism but not on the name would stop excluding the
// others silently: no error, no warning, just two writers in one sidecar.
//
// AssignHarp is deliberately absent: it mints a directory, and os.Mkdir's
// EEXIST is its exclusion.
func TestManager_EveryMutatingMethodLocksTheHarpsSidecarLock(t *testing.T) {
	cases := []struct {
		name string
		call func(t *testing.T, m *Manager)
	}{
		{"BindSession", func(t *testing.T, m *Manager) {
			require.NoError(t, m.BindSession(lockTestHarp, "sess-1", ""))
		}},
		{"AppendRotations", func(t *testing.T, m *Manager) {
			require.NoError(t, m.AppendRotations(lockTestHarp, []Rotation{{SessionID: "old", RotatedAt: time.Now()}}))
		}},
		{"MarkEnded", func(t *testing.T, m *Manager) {
			require.NoError(t, m.MarkEnded(lockTestHarp, time.Now()))
		}},
		{"MarkPurged", func(t *testing.T, m *Manager) {
			require.NoError(t, m.MarkPurged(lockTestHarp, time.Now()))
		}},
		{"RecordEngineVersion", func(t *testing.T, m *Manager) {
			require.NoError(t, m.RecordEngineVersion(lockTestHarp, "2.1.0"))
		}},
		{"SetSourceEntries", func(t *testing.T, m *Manager) {
			require.NoError(t, m.SetSourceEntries(lockTestHarp, 12))
		}},
		{"Rename", func(t *testing.T, m *Manager) {
			require.NoError(t, m.Rename(lockTestHarp, "brisk-amber-otter"))
		}},
		{"Forget", func(t *testing.T, m *Manager) {
			require.NoError(t, m.Forget(lockTestHarp))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, root := openSidecarRoot(t)
			// Seeded directly, not through a Manager method, so nothing has
			// taken the lock yet.
			writeSidecar(t, root, lockTestHarp, "project_dir: /proj\nbackend: claude\nstarted_at: 2026-01-01T00:00:00Z\n")
			// The fixture must be hostile: if a lock file already existed, an
			// assertion that one is present afterwards would prove nothing.
			require.Empty(t, lockFilesIn(t, root), "no lock file may exist before the call")

			tc.call(t, m)

			want, err := paths.HarpSidecarLockPath(lockTestHarp)
			require.NoError(t, err)
			require.Equal(t, []string{filepath.Base(want)}, lockFilesIn(t, root),
				"%s must take the harp's sidecar lock and no other name", tc.name)
		})
	}
}
