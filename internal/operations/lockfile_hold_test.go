package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

func TestActiveLockfileHold(t *testing.T) {
	// These helpers exercise the active-lock hold plumbing that `bundle
	// hold`/`unhold` drive.

	mkCfg := func(t *testing.T) *config.Config {
		t.Helper()
		return config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
	}

	readActive := func(t *testing.T, cfg *config.Config) *remote.Lockfile {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.GetAppPaths()[0])
		lock, err := mgr.Load()
		require.NoError(t, err)
		return lock
	}

	writeActive := func(t *testing.T, cfg *config.Config, entries map[string]string) {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.GetAppPaths()[0])
		lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{}}
		for name, sha := range entries {
			lock.Bundles[name] = remote.LockEntry{SHA: sha, URL: "https://example.com/r"}
		}
		require.NoError(t, mgr.Save(lock))
	}

	t.Run("SetItemPin flips the flag and persists", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := SetItemPin(cfg, "r/a", true)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		require.Contains(t, active.Bundles, "r/a")
		assert.True(t, active.Bundles["r/a"].Held)
	})

	t.Run("SetItemPin idempotent on repeated true", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		_, _ = SetItemPin(cfg, "r/a", true)
		found, err := SetItemPin(cfg, "r/a", true)
		require.NoError(t, err, "second hold must not error")
		assert.True(t, found)
	})

	t.Run("SetItemPin unhold clears the flag", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		_, _ = SetItemPin(cfg, "r/a", true)
		found, err := SetItemPin(cfg, "r/a", false)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		assert.False(t, active.Bundles["r/a"].Held)
	})

	t.Run("SetItemPin unknown bundle returns false, no error", func(t *testing.T) {
		// "Not in the active lockfile" is a user-visible state, not a
		// programmer error. The CLI turns this into a friendly message.
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := SetItemPin(cfg, "r/never-existed", true)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("LoadActiveLockfile returns the active lockfile", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		lock, err := LoadActiveLockfile(cfg)
		require.NoError(t, err)
		require.NotNil(t, lock)
		assert.Len(t, lock.Bundles, 1)
	})
}
