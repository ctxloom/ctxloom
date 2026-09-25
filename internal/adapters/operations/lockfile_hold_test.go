package operations

import (
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// heldRef is the dependency the hold tests pin, spelled as a user types it.
const heldRef = "https://example.com/r@bundles/a"

func TestActiveLockfileHold(t *testing.T) {
	// These helpers exercise the active-lock hold plumbing that `bundle
	// hold`/`unhold` drive.

	mkCfg := func(t *testing.T) *config.Config {
		t.Helper()
		return gatedFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
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
		lock := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{}}
		for name, sha := range entries {
			lock.Bundles[lockKeyOf(t, name)] = remote.LockEntry{SHA: sha, URL: "https://example.com/r"}
		}
		require.NoError(t, mgr.Save(lock))
	}

	t.Run("SetItemPin flips the flag and persists", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		found, err := SetItemPin(cfg, heldRef, true)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		require.Contains(t, active.Bundles, lockKeyOf(t, heldRef))
		assert.True(t, active.Bundles[lockKeyOf(t, heldRef)].Held)
	})

	t.Run("SetItemPin idempotent on repeated true", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		_, _ = SetItemPin(cfg, heldRef, true)
		found, err := SetItemPin(cfg, heldRef, true)
		require.NoError(t, err, "second hold must not error")
		assert.True(t, found)
	})

	t.Run("SetItemPin unhold clears the flag", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		_, _ = SetItemPin(cfg, heldRef, true)
		found, err := SetItemPin(cfg, heldRef, false)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		assert.False(t, active.Bundles[lockKeyOf(t, heldRef)].Held)
	})

	t.Run("SetItemPin unknown bundle returns false, no error", func(t *testing.T) {
		// "Not in the active lockfile" is a user-visible state, not a
		// programmer error. The CLI turns this into a friendly message.
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		found, err := SetItemPin(cfg, "https://example.com/r@bundles/never-existed", true)
		require.NoError(t, err)
		assert.False(t, found)
	})

	// A hold keys through the lockfile key, so it reaches the entry however the
	// user spells the repository — the key a retraction is also looked up by.
	t.Run("SetItemPin reaches the entry under another spelling of the repository", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		found, err := SetItemPin(cfg, "https://EXAMPLE.com/r/@bundles/a", true)
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, readActive(t, cfg).Bundles[lockKeyOf(t, heldRef)].Held)
	})

	t.Run("LoadActiveLockfile returns the active lockfile", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{heldRef: "sha1"})

		lock, err := LoadActiveLockfile(cfg)
		require.NoError(t, err)
		require.NotNil(t, lock)
		assert.Len(t, lock.Bundles, 1)
	})
}
