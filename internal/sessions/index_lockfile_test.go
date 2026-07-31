package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const lockTestHarp = "swift-amber-falcon"

// seedIndexForLockTest writes an index directly to disk -- no Manager method,
// so nothing has taken the lock yet -- and returns the Manager over it. The
// started_at is deliberately non-canonical so CommitUpgrade has something
// pending and therefore reaches its lock.
func seedIndexForLockTest(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.yaml")
	body := "sessions:\n" +
		"  - harp_name: " + lockTestHarp + "\n" +
		"    project_dir: /proj\n" +
		"    started_at: 2026-01-01 00:00:00+00:00\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	return m, dir
}

// lockFilesIn lists the lock-file names sitting next to the index.
func lockFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lock" {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestManager_EveryMutatingMethodLocksTheSameFile pins, at the PUBLIC seam,
// the agreement that eight separate call sites currently have to maintain by
// hand: every mutating method must take the index's cooperative lock at
// `<index>.lock` and no other name.
//
// The pin lives above the duplication on purpose. The sites are verbatim
// identical, so a parity test between them cannot be red; and collapsing them
// introduces a new private helper, so a test naming that helper could not be
// red either -- it would simply fail to compile against the pre-collapse code.
// Pinning the observable instead means this test is unchanged by the collapse
// and fails only if a site's lock IDENTITY diverges. A method that agreed on
// the mechanism but not on the name would stop excluding the other seven
// silently: no error, no warning, just two writers in the same file.
func TestManager_EveryMutatingMethodLocksTheSameFile(t *testing.T) {
	testsupport.Isolate(t)

	cases := []struct {
		name string
		call func(t *testing.T, m *Manager)
	}{
		{"AssignHarp", func(t *testing.T, m *Manager) {
			_, err := m.AssignHarp("/proj", "claude")
			require.NoError(t, err)
		}},
		{"BindSession", func(t *testing.T, m *Manager) {
			require.NoError(t, m.BindSession(lockTestHarp, "sess-1", ""))
		}},
		{"MarkEnded", func(t *testing.T, m *Manager) {
			require.NoError(t, m.MarkEnded(lockTestHarp, time.Now()))
		}},
		{"Rename", func(t *testing.T, m *Manager) {
			require.NoError(t, m.Rename(lockTestHarp, "brisk-amber-otter"))
		}},
		{"Forget", func(t *testing.T, m *Manager) {
			require.NoError(t, m.Forget(lockTestHarp))
		}},
		{"Reconcile", func(t *testing.T, m *Manager) {
			_, err := m.Reconcile(func(Entry) bool { return true })
			require.NoError(t, err)
		}},
		{"SetSummary", func(t *testing.T, m *Manager) {
			require.NoError(t, m.SetSummary(lockTestHarp, "a summary", nil, 12))
		}},
		{"CommitUpgrade", func(t *testing.T, m *Manager) {
			_, err := m.Load() // stages the pending timestamp upgrade
			require.NoError(t, err)
			require.NotNil(t, m.PendingUpgrade(), "CommitUpgrade must have work, or it returns before locking")
			require.NoError(t, m.CommitUpgrade())
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, dir := seedIndexForLockTest(t)

			// The fixture must be hostile: if a lock file already existed, an
			// assertion that one is present afterwards would prove nothing.
			require.Empty(t, lockFilesIn(t, dir), "no lock file may exist before the call")

			tc.call(t, m)

			require.Equal(t, []string{"index.yaml.lock"}, lockFilesIn(t, dir),
				"%s must take the index lock at <index>.lock and no other name", tc.name)
		})
	}
}
