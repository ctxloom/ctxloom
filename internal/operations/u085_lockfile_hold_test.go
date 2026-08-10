package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// holdFSFixture builds a config whose filesystem is injected (an in-memory fs
// carrying an active lock.yaml with one held-able bundle entry) and returns the
// manager that reads THAT fs, so a test can tell "read the injected fs" apart
// from "fell back to the OS fs".
func holdFSFixture(t *testing.T) (*config.Config, *remote.LockfileManager) {
	t.Helper()
	fs := afero.NewMemMapFs()
	baseDir := "/injected/.ctxloom"
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{baseDir}})
	cfg.SetFS(fs)

	mgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	require.NoError(t, mgr.Save(&remote.Lockfile{
		Version: 1,
		Bundles: map[string]remote.LockEntry{
			"r/a": {SHA: "sha1", URL: "https://example.com/r"},
		},
	}))
	return cfg, mgr
}

// TestLoadActiveLockfile_ReadsInjectedFS pins the read half:
// LoadActiveLockfile built its lockfile manager without WithLockfileFS, so under
// an injected filesystem it read a DIFFERENT lock.yaml (the real one on the host)
// than every other lockfile call site in this package resolves against.
func TestLoadActiveLockfile_ReadsInjectedFS(t *testing.T) {
	cfg, _ := holdFSFixture(t)

	lock, err := LoadActiveLockfile(cfg)
	require.NoError(t, err)
	require.NotNil(t, lock)
	assert.Contains(t, lock.Bundles, "r/a",
		"the active lockfile must come from the config's filesystem, not the OS fs")
}

// TestSetItemPin_PersistsToInjectedFS pins the write half: the hold
// must land in the injected filesystem's lock.yaml. Pre-fix SetItemPin loaded an
// empty lockfile from the OS fs and reported "not found", so the user's hold was
// silently not recorded anywhere the rest of the run can see.
func TestSetItemPin_PersistsToInjectedFS(t *testing.T) {
	cfg, mgr := holdFSFixture(t)

	found, err := SetItemPin(cfg, "r/a", true)
	require.NoError(t, err)
	require.True(t, found, "the entry seeded on the injected fs must be found")

	lock, err := mgr.Load()
	require.NoError(t, err)
	require.Contains(t, lock.Bundles, "r/a")
	assert.True(t, lock.Bundles["r/a"].Held,
		"the hold must persist to the injected fs, not the OS fs")
}
